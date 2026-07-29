#!/usr/bin/env python3
"""Collect and enforce the FLO-110 24 GB NVIDIA host contract."""

from __future__ import annotations

import argparse
import json
import platform
import re
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

REQUIRED_DRIVER = "570.124.06"
REQUIRED_TOOLKIT = "1.17.8"
MINIMUM_VRAM_MIB = 23_000


def _run(command: list[str]) -> dict[str, Any]:
    try:
        completed = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        return {
            "command": command,
            "exit_code": None,
            "stdout": "",
            "stderr": str(error),
        }
    return {
        "command": command,
        "exit_code": completed.returncode,
        "stdout": completed.stdout.strip(),
        "stderr": completed.stderr.strip(),
    }


def _version_tuple(value: str) -> tuple[int, ...]:
    return tuple(int(part) for part in re.findall(r"\d+", value))


def _read_os_release() -> dict[str, str]:
    result: dict[str, str] = {}
    path = Path("/etc/os-release")
    if not path.exists():
        return result
    for line in path.read_text(encoding="utf-8").splitlines():
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        result[key] = value.strip().strip('"')
    return result


def collect() -> dict[str, Any]:
    errors: list[str] = []
    warnings: list[str] = []
    machine = platform.machine().lower()
    system = platform.system().lower()
    os_release = _read_os_release()

    if system != "linux":
        errors.append(f"host OS must be Linux, got {platform.system()}")
    if machine not in {"x86_64", "amd64"}:
        errors.append(f"host architecture must be x86_64, got {platform.machine()}")
    if os_release and (
        os_release.get("ID") != "ubuntu" or os_release.get("VERSION_ID") != "24.04"
    ):
        errors.append(
            "host distribution must be Ubuntu 24.04 "
            f"(got {os_release.get('ID')} {os_release.get('VERSION_ID')})"
        )

    gpu_result = _run(
        [
            "nvidia-smi",
            "--query-gpu=index,name,memory.total,driver_version,uuid",
            "--format=csv,noheader,nounits",
        ]
    )
    gpus: list[dict[str, Any]] = []
    if gpu_result["exit_code"] != 0:
        errors.append("nvidia-smi is unavailable or failed")
    else:
        for line in gpu_result["stdout"].splitlines():
            parts = [part.strip() for part in line.split(",")]
            if len(parts) != 5:
                errors.append(f"unexpected nvidia-smi row: {line!r}")
                continue
            index, name, memory, driver, uuid = parts
            gpu = {
                "index": int(index),
                "name": name,
                "memory_total_mib": int(float(memory)),
                "driver_version": driver,
                "uuid": uuid,
            }
            gpus.append(gpu)
        if not gpus:
            errors.append("no NVIDIA GPU was reported")
        for gpu in gpus:
            if gpu["memory_total_mib"] < MINIMUM_VRAM_MIB:
                errors.append(
                    f"GPU {gpu['index']} has {gpu['memory_total_mib']} MiB; "
                    f"at least {MINIMUM_VRAM_MIB} MiB is required"
                )
            if _version_tuple(gpu["driver_version"]) != _version_tuple(REQUIRED_DRIVER):
                errors.append(
                    f"GPU {gpu['index']} driver must be {REQUIRED_DRIVER}; "
                    f"got {gpu['driver_version']}"
                )

    docker_result = _run(["docker", "version", "--format", "{{json .}}"])
    if docker_result["exit_code"] != 0:
        errors.append("Docker Engine is unavailable")
    compose_result = _run(["docker", "compose", "version", "--short"])
    if compose_result["exit_code"] != 0:
        errors.append("Docker Compose v2 is unavailable")

    toolkit_result = _run(["nvidia-ctk", "--version"])
    if toolkit_result["exit_code"] != 0:
        errors.append("nvidia-ctk is unavailable")
    elif REQUIRED_TOOLKIT not in toolkit_result["stdout"]:
        errors.append(
            f"NVIDIA Container Toolkit must be {REQUIRED_TOOLKIT}; "
            f"got {toolkit_result['stdout']!r}"
        )

    if shutil.which("git") is None:
        warnings.append(
            "git is unavailable; source revision evidence will be incomplete"
        )

    return {
        "schema_version": 1,
        "collected_at": datetime.now(timezone.utc).isoformat(),
        "status": "pass" if not errors else "fail",
        "contract": {
            "host_os": "Ubuntu 24.04",
            "host_architecture": "x86_64",
            "minimum_vram_mib": MINIMUM_VRAM_MIB,
            "required_driver": REQUIRED_DRIVER,
            "nvidia_container_toolkit": REQUIRED_TOOLKIT,
        },
        "host": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "os_release": os_release,
        },
        "gpus": gpus,
        "commands": {
            "nvidia_smi": gpu_result,
            "docker": docker_result,
            "docker_compose": compose_result,
            "nvidia_container_toolkit": toolkit_result,
        },
        "errors": errors,
        "warnings": warnings,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    report = collect()
    payload = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(payload, encoding="utf-8")
    print(payload, end="")
    return 0 if report["status"] == "pass" else 2


if __name__ == "__main__":
    raise SystemExit(main())
