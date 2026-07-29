from __future__ import annotations

import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from tools.generate_reference_assets import generate_all
from tools.policy import PolicyError, load_json, validate_policy
from tools.summarize import nearest_rank_p95, summarize


class PolicyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.policy = load_json(ROOT / "supply-chain.json")

    def test_committed_policy_is_valid(self) -> None:
        artifacts = validate_policy(self.policy)
        self.assertEqual(5, len(artifacts))
        self.assertEqual(
            3,
            sum(artifact["kind"] == "model" for artifact in artifacts),
        )

    def test_unknown_license_is_denied(self) -> None:
        policy = copy.deepcopy(self.policy)
        policy["artifacts"][1]["license"] = "LicenseRef-Unknown"
        with self.assertRaisesRegex(PolicyError, "without a scoped exception"):
            validate_policy(policy)

    def test_model_destination_traversal_is_denied(self) -> None:
        policy = copy.deepcopy(self.policy)
        policy["artifacts"][1]["destination"] = "../outside/model.safetensors"
        with self.assertRaisesRegex(PolicyError, "unsafe artifact destination"):
            validate_policy(policy)

    def test_custom_nodes_cannot_be_enabled(self) -> None:
        policy = copy.deepcopy(self.policy)
        policy["policy"]["custom_nodes_allowed"] = True
        with self.assertRaisesRegex(PolicyError, "custom nodes must be denied"):
            validate_policy(policy)


class ReferenceAssetTests(unittest.TestCase):
    def test_generator_is_deterministic_and_balanced(self) -> None:
        manifest = ROOT / "benchmark" / "shots.json"
        with (
            tempfile.TemporaryDirectory() as first,
            tempfile.TemporaryDirectory() as second,
        ):
            first_evidence = generate_all(manifest, Path(first))
            second_evidence = generate_all(manifest, Path(second))

        first_hashes = {
            item["shot_id"]: item["sha256"] for item in first_evidence["assets"]
        }
        second_hashes = {
            item["shot_id"]: item["sha256"] for item in second_evidence["assets"]
        }
        self.assertEqual(first_hashes, second_hashes)
        self.assertEqual(15, len(first_hashes))
        self.assertTrue(
            all(
                item["width"] == 1280 and item["height"] == 704
                for item in first_evidence["assets"]
            )
        )


def successful_record(
    run_id: str,
    shot: dict[str, object],
    phase: str,
    elapsed: float,
) -> dict[str, object]:
    logical_id = f"{run_id}:{shot['id']}:{phase}"
    return {
        "run_id": run_id,
        "logical_result_id": logical_id,
        "result_id": f"{logical_id}:attempt-1",
        "shot_id": shot["id"],
        "category": shot["category"],
        "phase": phase,
        "attempt": 1,
        "status": "success",
        "elapsed_seconds": elapsed,
        "peak_vram_mib": 19_500,
        "media": {
            "source": {
                "width": 1280,
                "height": 704,
                "frames": 121,
                "fps": 24.0,
            },
            "delivery": {
                "width": 1280,
                "height": 720,
                "frames": 120,
                "fps": 24.0,
                "duration_seconds": 5.0,
            },
        },
    }


class SummaryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.shots = json.loads(
            (ROOT / "benchmark" / "shots.json").read_text(encoding="utf-8")
        )

    def test_nearest_rank_p95_uses_maximum_for_15_samples(self) -> None:
        self.assertEqual(
            15.0, nearest_rank_p95([float(value) for value in range(1, 16)])
        )

    def test_complete_passing_evidence_passes_all_gates(self) -> None:
        records = []
        scores = {}
        for index, shot in enumerate(self.shots["shots"]):
            for phase in ("cold", "warm"):
                record = successful_record("run-1", shot, phase, 300.0 + index)
                records.append(record)
                scores[record["logical_result_id"]] = {
                    "result_id": record["logical_result_id"],
                    "reviewer": "qa",
                    "reviewed_at": "2026-07-30T00:00:00Z",
                    "notes": "offline fixture",
                    "scores": {
                        "reference_consistency": 4,
                        "prompt_adherence": 4,
                        "temporal_coherence": 4,
                        "motion_camera": 4,
                        "artifact_control": 4,
                    },
                }

        summary = summarize(records, self.shots, scores)

        self.assertEqual("pass", summary["conclusion"])
        self.assertEqual(30, summary["coverage"]["successful_logical_results"])
        self.assertEqual(314.0, summary["performance"]["overall_p95_seconds"])
        self.assertTrue(all(summary["gates"].values()))

    def test_missing_quality_evidence_is_incomplete_not_passed(self) -> None:
        records = [
            successful_record("run-2", shot, phase, 300.0)
            for shot in self.shots["shots"]
            for phase in ("cold", "warm")
        ]

        summary = summarize(records, self.shots, {})

        self.assertEqual("incomplete", summary["conclusion"])
        self.assertIsNone(summary["gates"]["quality_score_coverage"])

    def test_excess_first_attempt_retries_fail_reliability_gate(self) -> None:
        records = [
            successful_record("run-3", shot, phase, 300.0)
            for shot in self.shots["shots"]
            for phase in ("cold", "warm")
        ]
        retry_ids = {record["logical_result_id"] for record in records[:5]}
        amended = []
        for record in records:
            if record["logical_result_id"] not in retry_ids:
                amended.append(record)
                continue
            failed = copy.deepcopy(record)
            failed["status"] = "failed"
            failed["error"] = "transient"
            amended.append(failed)
            succeeded = copy.deepcopy(record)
            succeeded["attempt"] = 2
            succeeded["result_id"] = f"{record['logical_result_id']}:attempt-2"
            amended.append(succeeded)

        summary = summarize(amended, self.shots, {})

        self.assertFalse(summary["gates"]["first_attempt_success_rate"])
        self.assertLess(summary["reliability"]["first_attempt_success_rate"], 0.85)


if __name__ == "__main__":
    unittest.main()
