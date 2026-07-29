#!/usr/bin/env python3
"""Summarize FLO-110 attempt evidence and evaluate the frozen gates."""

from __future__ import annotations

import argparse
import json
import math
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any

QUALITY_DIMENSIONS = {
    "reference_consistency",
    "prompt_adherence",
    "temporal_coherence",
    "motion_camera",
    "artifact_control",
}


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise TypeError(f"{path} must contain a JSON object")
    return value


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    with path.open("r", encoding="utf-8") as handle:
        for number, line in enumerate(handle, start=1):
            if not line.strip():
                continue
            value = json.loads(line)
            if not isinstance(value, dict):
                raise TypeError(f"{path}:{number} must be a JSON object")
            records.append(value)
    return records


def nearest_rank_p95(values: list[float]) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, math.ceil(0.95 * len(ordered)) - 1)
    return ordered[index]


def _media_matches(result: dict[str, Any]) -> bool:
    media = result.get("media")
    if not isinstance(media, dict):
        return False
    source = media.get("source", {})
    delivery = media.get("delivery", {})
    return (
        source.get("width") == 1280
        and source.get("height") == 704
        and source.get("frames") == 121
        and abs(float(source.get("fps", 0)) - 24.0) < 0.01
        and delivery.get("width") == 1280
        and delivery.get("height") == 720
        and delivery.get("frames") == 120
        and abs(float(delivery.get("fps", 0)) - 24.0) < 0.01
        and abs(float(delivery.get("duration_seconds", 0)) - 5.0) < 0.05
    )


def load_scores(path: Path | None) -> dict[str, dict[str, Any]]:
    if path is None or not path.exists():
        return {}
    document = load_json(path)
    records = document.get("scores")
    if not isinstance(records, list):
        raise TypeError("scores document must contain a scores array")
    result: dict[str, dict[str, Any]] = {}
    for record in records:
        if not isinstance(record, dict):
            raise TypeError("quality score records must be objects")
        result_id = record.get("result_id")
        if not isinstance(result_id, str) or not result_id:
            raise ValueError("quality score record requires result_id")
        if result_id in result:
            raise ValueError(f"duplicate quality score for {result_id}")
        if not record.get("reviewer") or not record.get("reviewed_at"):
            raise ValueError(f"quality score {result_id} lacks reviewer evidence")
        scores = record.get("scores")
        if not isinstance(scores, dict) or set(scores) != QUALITY_DIMENSIONS:
            raise ValueError(f"quality score {result_id} has the wrong dimensions")
        for dimension, value in scores.items():
            if not isinstance(value, int) or not 1 <= value <= 5:
                raise ValueError(
                    f"quality score {result_id}/{dimension} must be an integer from 1 to 5"
                )
        result[result_id] = record
    return result


def summarize(
    records: list[dict[str, Any]],
    shot_document: dict[str, Any],
    score_records: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    shots = shot_document.get("shots")
    if not isinstance(shots, list) or len(shots) != 15:
        raise ValueError("shot manifest must contain exactly 15 shots")
    shot_by_id = {shot["id"]: shot for shot in shots}
    expected = {
        f"{record_run_id}:{shot['id']}:{phase}"
        for record_run_id in {str(record.get("run_id")) for record in records}
        for shot in shots
        for phase in ("cold", "warm")
    }
    run_ids = {str(record.get("run_id")) for record in records}
    if len(run_ids) > 1:
        raise ValueError(
            f"results must contain exactly one run_id, got {sorted(run_ids)}"
        )
    run_id = next(iter(run_ids), None)
    if run_id is None:
        expected = set()

    grouped: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for record in records:
        logical_id = record.get("logical_result_id")
        if not isinstance(logical_id, str):
            raise TypeError("every attempt requires logical_result_id")
        if record.get("shot_id") not in shot_by_id:
            raise ValueError(f"unknown shot_id in results: {record.get('shot_id')}")
        grouped[logical_id].append(record)
    for attempts in grouped.values():
        attempts.sort(key=lambda item: int(item["attempt"]))

    final_results: dict[str, dict[str, Any]] = {}
    for logical_id, attempts in grouped.items():
        successful = [item for item in attempts if item.get("status") == "success"]
        final_results[logical_id] = successful[0] if successful else attempts[-1]

    missing = sorted(expected - set(grouped))
    unexpected = sorted(set(grouped) - expected)
    successful_results = [
        record for record in final_results.values() if record.get("status") == "success"
    ]
    first_attempt_successes = sum(
        1
        for attempts in grouped.values()
        if attempts and attempts[0].get("status") == "success"
    )
    expected_count = len(expected)
    within_two_successes = len(successful_results)
    first_attempt_rate = (
        first_attempt_successes / expected_count if expected_count else 0.0
    )
    within_two_rate = within_two_successes / expected_count if expected_count else 0.0

    elapsed_by_phase: dict[str, list[float]] = {"cold": [], "warm": []}
    for record in successful_results:
        phase = record["phase"]
        elapsed_by_phase[phase].append(
            float(
                record.get("generation_elapsed_seconds", record.get("elapsed_seconds"))
            )
        )
    all_elapsed = elapsed_by_phase["cold"] + elapsed_by_phase["warm"]
    peak_values = [
        int(record["peak_vram_mib"])
        for record in successful_results
        if record.get("peak_vram_mib") is not None
    ]
    media_failures = sorted(
        record["logical_result_id"]
        for record in successful_results
        if not _media_matches(record)
    )

    quality_records = {
        logical_id: score_records[logical_id]
        for logical_id in final_results
        if logical_id in score_records
        and final_results[logical_id].get("status") == "success"
    }
    quality_coverage = len(quality_records) / expected_count if expected_count else 0.0
    consistency_fraction = (
        sum(
            record["scores"]["reference_consistency"] >= 4
            for record in quality_records.values()
        )
        / len(quality_records)
        if quality_records
        else None
    )
    motion_ids = {
        logical_id
        for logical_id, result in final_results.items()
        if result.get("category") == "motion_camera"
    }
    scored_motion = [
        quality_records[logical_id]
        for logical_id in motion_ids
        if logical_id in quality_records
    ]
    motion_fraction = (
        sum(record["scores"]["motion_camera"] >= 4 for record in scored_motion)
        / len(scored_motion)
        if scored_motion
        else None
    )
    severe_artifact_fraction = (
        sum(
            record["scores"]["artifact_control"] <= 2
            for record in quality_records.values()
        )
        / len(quality_records)
        if quality_records
        else None
    )

    gates = shot_document["baseline"]["gates_from_flo_98"]
    p95_all = nearest_rank_p95(all_elapsed)
    gate_results: dict[str, bool | None] = {
        "complete_30_logical_results": expected_count == 30
        and not missing
        and not unexpected,
        "all_results_succeeded": within_two_successes == expected_count == 30,
        "media_spec": not media_failures and within_two_successes == 30,
        "p95_elapsed_seconds": (
            p95_all <= gates["p95_elapsed_seconds_max"] if p95_all is not None else None
        ),
        "first_attempt_success_rate": (
            first_attempt_rate >= gates["first_attempt_success_rate_min"]
            if expected_count
            else None
        ),
        "within_two_attempts_success_rate": (
            within_two_rate >= gates["within_two_attempts_success_rate_min"]
            if expected_count
            else None
        ),
        "quality_score_coverage": (
            True if expected_count and quality_coverage == 1.0 else None
        ),
        "reference_consistency": (
            consistency_fraction >= gates["reference_consistency_score_4_fraction_min"]
            if consistency_fraction is not None
            else None
        ),
        "motion_camera": (
            motion_fraction >= gates["motion_camera_score_4_fraction_min"]
            if motion_fraction is not None
            else None
        ),
        "severe_artifacts": (
            severe_artifact_fraction <= gates["severe_artifact_fraction_max"]
            if severe_artifact_fraction is not None
            else None
        ),
    }
    if any(value is False for value in gate_results.values()):
        conclusion = "fail"
    elif any(value is None for value in gate_results.values()):
        conclusion = "incomplete"
    elif all(value is True for value in gate_results.values()):
        conclusion = "pass"
    else:
        conclusion = "incomplete"

    failed_results = [
        {
            "logical_result_id": logical_id,
            "attempts": len(grouped[logical_id]),
            "last_error": result.get("error"),
        }
        for logical_id, result in sorted(final_results.items())
        if result.get("status") != "success"
    ]

    return {
        "schema_version": 1,
        "run_id": run_id,
        "conclusion": conclusion,
        "p95_method": "nearest-rank",
        "coverage": {
            "configured_shots": len(shots),
            "configured_categories": Counter(shot["category"] for shot in shots),
            "expected_logical_results": expected_count,
            "observed_logical_results": len(grouped),
            "successful_logical_results": within_two_successes,
            "missing_logical_results": missing,
            "unexpected_logical_results": unexpected,
            "attempt_records": len(records),
            "retry_count": sum(
                max(0, len(attempts) - 1) for attempts in grouped.values()
            ),
        },
        "performance": {
            "target_seconds": gates["p95_elapsed_seconds_max"],
            "overall_p95_seconds": p95_all,
            "cold_p95_seconds": nearest_rank_p95(elapsed_by_phase["cold"]),
            "warm_p95_seconds": nearest_rank_p95(elapsed_by_phase["warm"]),
            "cold_samples": len(elapsed_by_phase["cold"]),
            "warm_samples": len(elapsed_by_phase["warm"]),
            "peak_vram_mib": max(peak_values) if peak_values else None,
        },
        "reliability": {
            "first_attempt_successes": first_attempt_successes,
            "first_attempt_success_rate": first_attempt_rate,
            "within_two_attempts_successes": within_two_successes,
            "within_two_attempts_success_rate": within_two_rate,
            "failure_rate": 1.0 - within_two_rate if expected_count else None,
            "failed_results": failed_results,
        },
        "media": {
            "raw_contract": "1280x704, 121 frames, 24fps",
            "delivery_contract": "1280x720, 120 frames, 24fps, 5.0 seconds",
            "failed_results": media_failures,
        },
        "quality": {
            "scored_results": len(quality_records),
            "expected_results": expected_count,
            "coverage": quality_coverage,
            "reference_consistency_score_4_fraction": consistency_fraction,
            "motion_camera_score_4_fraction": motion_fraction,
            "severe_artifact_fraction": severe_artifact_fraction,
        },
        "gates": gate_results,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--shots", type=Path, required=True)
    parser.add_argument("--scores", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    records = load_jsonl(args.results)
    shot_document = load_json(args.shots)
    scores = load_scores(args.scores)
    summary = summarize(records, shot_document, scores)
    payload = json.dumps(summary, indent=2, sort_keys=True) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")
    return 0 if summary["conclusion"] == "pass" else 2


if __name__ == "__main__":
    raise SystemExit(main())
