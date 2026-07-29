#!/usr/bin/env python3
"""Create a manual quality-score sheet from successful logical results."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

DIMENSIONS = (
    "reference_consistency",
    "prompt_adherence",
    "temporal_coherence",
    "motion_camera",
    "artifact_control",
)


def load_results(path: Path) -> list[dict[str, Any]]:
    records = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            records.append(json.loads(line))
    return records


def prepare(records: list[dict[str, Any]]) -> dict[str, Any]:
    successful: dict[str, dict[str, Any]] = {}
    for record in records:
        logical_id = record["logical_result_id"]
        if record.get("status") == "success" and logical_id not in successful:
            successful[logical_id] = record
    return {
        "schema_version": 1,
        "instructions": (
            "Replace every null with an integer 1-5, set reviewer/reviewed_at/notes, "
            "then rerun summarize.py. Never score from thumbnails alone."
        ),
        "scores": [
            {
                "result_id": logical_id,
                "delivery_output": record.get("delivery_output"),
                "reviewer": None,
                "reviewed_at": None,
                "scores": {dimension: None for dimension in DIMENSIONS},
                "notes": None,
            }
            for logical_id, record in sorted(successful.items())
        ],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError(
            f"refusing to overwrite existing score sheet: {args.output}"
        )
    document = prepare(load_results(args.results))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(document, indent=2, sort_keys=True) + "\n"
    args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
