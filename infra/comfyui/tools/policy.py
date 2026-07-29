#!/usr/bin/env python3
"""Validate the deny-by-default BOM and fetch checksum-pinned model files."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path, PurePosixPath
from typing import Any


class PolicyError(RuntimeError):
    """Raised when an artifact violates the supply-chain policy."""


def load_json(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise PolicyError(f"{path} must contain a JSON object")
    return value


def sha256_file(path: Path, chunk_size: int = 8 * 1024 * 1024) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(chunk_size):
            digest.update(chunk)
    return digest.hexdigest()


def _scoped_exception_ids(policy: dict[str, Any]) -> set[str]:
    ids: set[str] = set()
    for exception in policy.get("scoped_exceptions", []):
        if not isinstance(exception, dict):
            raise PolicyError("policy.scoped_exceptions entries must be objects")
        if exception.get("redistribution_allowed") is not False:
            raise PolicyError("scoped exceptions must explicitly deny redistribution")
        if exception.get("release_requires_legal_approval") is not True:
            raise PolicyError(
                "scoped exceptions must require legal approval for release"
            )
        artifact_ids = exception.get("artifact_ids")
        if not isinstance(artifact_ids, list) or not artifact_ids:
            raise PolicyError("scoped exception must name at least one artifact")
        ids.update(str(item) for item in artifact_ids)
    return ids


def _safe_destination(value: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or not path.parts:
        raise PolicyError(f"unsafe artifact destination: {value!r}")
    if path.parts[0] != "models":
        raise PolicyError(f"model destination must be below models/: {value!r}")
    return path


def validate_policy(document: dict[str, Any]) -> list[dict[str, Any]]:
    if document.get("schema_version") != 1:
        raise PolicyError("unsupported supply-chain schema_version")

    policy = document.get("policy")
    if not isinstance(policy, dict):
        raise PolicyError("missing policy object")
    if policy.get("default_action") != "deny":
        raise PolicyError("policy.default_action must be deny")
    if policy.get("custom_nodes_allowed") is not False:
        raise PolicyError("custom nodes must be denied for the native baseline")

    allowed_licenses = set(policy.get("allowed_licenses", []))
    if allowed_licenses != {"Apache-2.0", "BSD-3-Clause", "MIT"}:
        raise PolicyError("default license allowlist must match FLO-98 D-13")
    scoped_exception_ids = _scoped_exception_ids(policy)

    artifacts = document.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise PolicyError("artifacts must be a non-empty list")

    seen_ids: set[str] = set()
    seen_destinations: set[str] = set()
    model_count = 0
    for artifact in artifacts:
        if not isinstance(artifact, dict):
            raise PolicyError("artifact entries must be objects")
        artifact_id = artifact.get("id")
        if not isinstance(artifact_id, str) or not artifact_id:
            raise PolicyError("every artifact requires a non-empty id")
        if artifact_id in seen_ids:
            raise PolicyError(f"duplicate artifact id: {artifact_id}")
        seen_ids.add(artifact_id)

        if artifact.get("allowed") is not True:
            raise PolicyError(f"artifact is not explicitly allowed: {artifact_id}")
        license_id = artifact.get("license")
        if (
            license_id not in allowed_licenses
            and artifact_id not in scoped_exception_ids
        ):
            raise PolicyError(
                f"artifact {artifact_id} has license {license_id!r} without a scoped exception"
            )
        if not artifact.get("license_evidence"):
            raise PolicyError(f"artifact {artifact_id} lacks license evidence")

        source = artifact.get("source")
        if not isinstance(source, str) or not source.startswith("https://"):
            raise PolicyError(f"artifact {artifact_id} must use an HTTPS source")

        if artifact.get("kind") == "model":
            model_count += 1
            destination = str(_safe_destination(str(artifact.get("destination", ""))))
            if destination in seen_destinations:
                raise PolicyError(f"duplicate model destination: {destination}")
            seen_destinations.add(destination)
            expected_sha = artifact.get("sha256")
            if (
                not isinstance(expected_sha, str)
                or len(expected_sha) != 64
                or any(char not in "0123456789abcdef" for char in expected_sha)
            ):
                raise PolicyError(f"artifact {artifact_id} has an invalid SHA-256")
            if (
                not isinstance(artifact.get("size_bytes"), int)
                or artifact["size_bytes"] <= 0
            ):
                raise PolicyError(f"artifact {artifact_id} has an invalid size")
            revision = artifact.get("source_revision")
            if not isinstance(revision, str) or revision not in source:
                raise PolicyError(
                    f"artifact {artifact_id} source is not revision-pinned"
                )

    if model_count != 3:
        raise PolicyError(
            f"native Wan2.2 baseline must contain exactly 3 model files, got {model_count}"
        )
    unknown_exceptions = scoped_exception_ids - seen_ids
    if unknown_exceptions:
        raise PolicyError(
            f"scoped exceptions reference unknown artifacts: {sorted(unknown_exceptions)}"
        )
    return artifacts


def model_artifacts(document: dict[str, Any]) -> list[dict[str, Any]]:
    return [
        artifact
        for artifact in validate_policy(document)
        if artifact.get("kind") == "model"
    ]


def verify_models(document: dict[str, Any], runtime_dir: Path) -> list[dict[str, Any]]:
    statuses: list[dict[str, Any]] = []
    for artifact in model_artifacts(document):
        destination = runtime_dir / _safe_destination(artifact["destination"])
        status: dict[str, Any] = {
            "id": artifact["id"],
            "path": str(destination),
            "expected_size_bytes": artifact["size_bytes"],
            "expected_sha256": artifact["sha256"],
        }
        if not destination.is_file():
            status.update(
                {"status": "missing", "actual_size_bytes": None, "actual_sha256": None}
            )
        else:
            actual_size = destination.stat().st_size
            actual_sha = (
                sha256_file(destination)
                if actual_size == artifact["size_bytes"]
                else None
            )
            valid = (
                actual_size == artifact["size_bytes"]
                and actual_sha == artifact["sha256"]
            )
            status.update(
                {
                    "status": "verified" if valid else "invalid",
                    "actual_size_bytes": actual_size,
                    "actual_sha256": actual_sha,
                }
            )
        statuses.append(status)
    return statuses


def _download_artifact(artifact: dict[str, Any], runtime_dir: Path) -> Path:
    destination = runtime_dir / _safe_destination(artifact["destination"])
    destination.parent.mkdir(parents=True, exist_ok=True)

    if destination.exists():
        if (
            destination.stat().st_size == artifact["size_bytes"]
            and sha256_file(destination) == artifact["sha256"]
        ):
            print(f"verified existing {artifact['id']}", file=sys.stderr)
            return destination
        raise PolicyError(
            f"{destination} exists but does not match the allowlist; move it aside before retrying"
        )

    partial = destination.with_name(f"{destination.name}.part")
    offset = partial.stat().st_size if partial.exists() else 0
    if offset > artifact["size_bytes"]:
        raise PolicyError(f"partial download is larger than expected: {partial}")

    headers = {"User-Agent": "vibe-forge-flo-110/1"}
    token = os.environ.get("HF_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if offset:
        headers["Range"] = f"bytes={offset}-"

    request = urllib.request.Request(artifact["source"], headers=headers)
    try:
        response = urllib.request.urlopen(request, timeout=60)
    except urllib.error.HTTPError as error:
        raise PolicyError(
            f"download failed for {artifact['id']}: HTTP {error.code}"
        ) from error

    status = getattr(response, "status", response.getcode())
    if offset and status != 206:
        response.close()
        raise PolicyError(
            f"server ignored resume request for {artifact['id']}; remove {partial} and retry"
        )

    mode = "ab" if offset else "wb"
    next_report = offset + 256 * 1024 * 1024
    started = time.monotonic()
    written = offset
    with response, partial.open(mode) as handle:
        while chunk := response.read(8 * 1024 * 1024):
            handle.write(chunk)
            written += len(chunk)
            if written >= next_report:
                elapsed = max(time.monotonic() - started, 0.001)
                rate_mib = (written - offset) / elapsed / 1024 / 1024
                print(
                    f"{artifact['id']}: {written}/{artifact['size_bytes']} bytes "
                    f"({rate_mib:.1f} MiB/s)",
                    file=sys.stderr,
                )
                next_report += 256 * 1024 * 1024

    if partial.stat().st_size != artifact["size_bytes"]:
        raise PolicyError(
            f"size mismatch for {artifact['id']}: "
            f"{partial.stat().st_size} != {artifact['size_bytes']}"
        )
    actual_sha = sha256_file(partial)
    if actual_sha != artifact["sha256"]:
        raise PolicyError(
            f"SHA-256 mismatch for {artifact['id']}: {actual_sha} != {artifact['sha256']}"
        )
    partial.replace(destination)
    return destination


def download_models(document: dict[str, Any], runtime_dir: Path) -> list[Path]:
    return [
        _download_artifact(artifact, runtime_dir)
        for artifact in model_artifacts(document)
    ]


def _default_policy_path() -> Path:
    return Path(__file__).resolve().parents[1] / "supply-chain.json"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "command",
        choices=("validate", "verify", "download"),
        help="policy action",
    )
    parser.add_argument("--policy", type=Path, default=_default_policy_path())
    parser.add_argument(
        "--runtime-dir",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "runtime",
    )
    args = parser.parse_args()

    try:
        document = load_json(args.policy)
        validate_policy(document)
        if args.command == "validate":
            result: Any = {"status": "valid", "policy": str(args.policy)}
        elif args.command == "verify":
            statuses = verify_models(document, args.runtime_dir)
            result = {"status": "verified", "models": statuses}
            if any(item["status"] != "verified" for item in statuses):
                print(json.dumps(result, indent=2, sort_keys=True))
                return 2
        else:
            paths = download_models(document, args.runtime_dir)
            result = {"status": "downloaded", "paths": [str(path) for path in paths]}
    except (OSError, ValueError, PolicyError) as error:
        print(
            json.dumps({"status": "error", "error": str(error)}, indent=2),
            file=sys.stderr,
        )
        return 1

    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
