#!/usr/bin/env python3
"""Run the paired cold/warm 15-shot Wan2.2 TI2V-5B benchmark."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import Any

if __package__:
    from .generate_reference_assets import generate_all
    from .policy import load_json, sha256_file, validate_policy, verify_models
    from .preflight import collect as collect_preflight
else:
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    from generate_reference_assets import generate_all
    from policy import load_json, sha256_file, validate_policy, verify_models
    from preflight import collect as collect_preflight


ROOT = Path(__file__).resolve().parents[1]
COMPOSE_FILE = ROOT / "compose.gpu.yaml"
POLICY_FILE = ROOT / "supply-chain.json"
SHOT_FILE = ROOT / "benchmark" / "shots.json"
WORKFLOW_FILE = ROOT / "workflows" / "wan22_ti2v_5b_i2v_api.json"
EXPECTED_NODES = {
    "UNETLoader",
    "CLIPLoader",
    "VAELoader",
    "CLIPTextEncode",
    "LoadImage",
    "Wan22ImageToVideoLatent",
    "ModelSamplingSD3",
    "KSampler",
    "VAEDecode",
    "CreateVideo",
    "SaveVideo",
}
PERMANENT_ERROR_MARKERS = (
    "out of memory",
    "model not found",
    "invalid prompt",
    "does not exist in",
    "license",
    "checksum",
)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def canonical_sha256(value: Any) -> str:
    payload = json.dumps(value, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _json_request(
    url: str,
    *,
    method: str = "GET",
    payload: dict[str, Any] | None = None,
    timeout: float = 30,
) -> Any:
    data = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        body = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {error.code} from {url}: {body[:2000]}") from error


def safe_output_relative(output: dict[str, Any]) -> PurePosixPath:
    filename = output.get("filename")
    subfolder = output.get("subfolder", "")
    if not isinstance(filename, str) or not filename:
        raise RuntimeError(f"ComfyUI output is missing a filename: {output!r}")
    relative = PurePosixPath(str(subfolder)) / filename
    if relative.is_absolute() or ".." in relative.parts:
        raise RuntimeError(f"unsafe ComfyUI output path: {relative}")
    return relative


def find_saved_output(history: dict[str, Any]) -> dict[str, Any]:
    for node_output in history.get("outputs", {}).values():
        if not isinstance(node_output, dict):
            continue
        for value in node_output.values():
            if not isinstance(value, list):
                continue
            for item in value:
                if (
                    isinstance(item, dict)
                    and item.get("type") == "output"
                    and isinstance(item.get("filename"), str)
                ):
                    return item
    raise RuntimeError("completed prompt did not report a saved output file")


class ComfyClient:
    def __init__(self, base_url: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.client_id = str(uuid.uuid4())

    def wait_ready(self, timeout_seconds: float = 180) -> float:
        started = time.monotonic()
        last_error = "not attempted"
        while time.monotonic() - started < timeout_seconds:
            try:
                _json_request(f"{self.base_url}/system_stats", timeout=3)
                return time.monotonic() - started
            except (OSError, ValueError, RuntimeError) as error:
                last_error = str(error)
                time.sleep(2)
        raise TimeoutError(f"ComfyUI did not become ready: {last_error}")

    def validate_nodes(self) -> None:
        object_info = _json_request(f"{self.base_url}/object_info", timeout=30)
        missing = sorted(EXPECTED_NODES - set(object_info))
        if missing:
            raise RuntimeError(f"ComfyUI is missing required native nodes: {missing}")

    def queue(self, workflow: dict[str, Any]) -> str:
        response = _json_request(
            f"{self.base_url}/prompt",
            method="POST",
            payload={"prompt": workflow, "client_id": self.client_id},
            timeout=30,
        )
        prompt_id = response.get("prompt_id")
        if not isinstance(prompt_id, str):
            raise TypeError(f"unexpected /prompt response: {response!r}")
        return prompt_id

    def wait_history(self, prompt_id: str, timeout_seconds: float) -> dict[str, Any]:
        started = time.monotonic()
        while time.monotonic() - started < timeout_seconds:
            response = _json_request(
                f"{self.base_url}/history/{prompt_id}",
                timeout=30,
            )
            history = response.get(prompt_id) if isinstance(response, dict) else None
            if isinstance(history, dict):
                status = history.get("status", {})
                status_string = str(status.get("status_str", "")).lower()
                if status_string == "error":
                    messages = status.get("messages", [])
                    raise RuntimeError(f"ComfyUI execution failed: {messages!r}")
                if status.get("completed") is True:
                    return history
            time.sleep(1)
        raise TimeoutError(f"prompt {prompt_id} exceeded {timeout_seconds} seconds")


class TelemetrySampler:
    HEADER = (
        "observed_at,index,memory_used_mib,memory_total_mib,"
        "utilization_gpu_percent,temperature_c,power_w\n"
    )

    def __init__(
        self, output: Path, gpu_index: int, interval_seconds: float = 1.0
    ) -> None:
        self.output = output
        self.gpu_index = gpu_index
        self.interval_seconds = interval_seconds
        self.stop_event = threading.Event()
        self.thread: threading.Thread | None = None
        self.peak_vram_mib: int | None = None
        self.errors: list[str] = []

    def _sample_once(self) -> str | None:
        completed = subprocess.run(
            [
                "nvidia-smi",
                f"--id={self.gpu_index}",
                "--query-gpu=index,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw",
                "--format=csv,noheader,nounits",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if completed.returncode != 0:
            self.errors.append(completed.stderr.strip() or "nvidia-smi sampling failed")
            return None
        row = completed.stdout.strip().splitlines()[0]
        parts = [part.strip() for part in row.split(",")]
        if len(parts) != 6:
            self.errors.append(f"unexpected telemetry row: {row!r}")
            return None
        memory_used = int(float(parts[1]))
        self.peak_vram_mib = max(self.peak_vram_mib or 0, memory_used)
        return f"{utc_now()},{','.join(parts)}\n"

    def _run(self) -> None:
        self.output.parent.mkdir(parents=True, exist_ok=True)
        with self.output.open("w", encoding="utf-8") as handle:
            handle.write(self.HEADER)
            handle.flush()
            while not self.stop_event.is_set():
                try:
                    row = self._sample_once()
                    if row:
                        handle.write(row)
                        handle.flush()
                except (OSError, subprocess.TimeoutExpired, ValueError) as error:
                    self.errors.append(str(error))
                self.stop_event.wait(self.interval_seconds)

    def start(self) -> None:
        self.thread = threading.Thread(
            target=self._run, name="gpu-telemetry", daemon=False
        )
        self.thread.start()

    def stop(self) -> None:
        self.stop_event.set()
        if self.thread:
            self.thread.join(timeout=15)
            if self.thread.is_alive():
                raise RuntimeError("GPU telemetry thread did not stop")


class Benchmark:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.runtime_dir = args.runtime_dir.resolve()
        self.evidence_dir = args.evidence_dir.resolve()
        self.run_id = args.run_id or datetime.now(timezone.utc).strftime(
            "%Y%m%dT%H%M%SZ"
        )
        self.run_dir = self.evidence_dir / self.run_id
        self.results_file = self.run_dir / "results.jsonl"
        self.compose_environment = os.environ.copy()
        self.compose_environment["COMFY_RUNTIME_DIR"] = str(self.runtime_dir)
        if hasattr(os, "getuid"):
            self.compose_environment.setdefault("COMFY_UID", str(os.getuid()))
            self.compose_environment.setdefault("COMFY_GID", str(os.getgid()))
        self.client = ComfyClient(args.comfy_url)
        self.shot_document = load_json(SHOT_FILE)
        self.workflow_template = load_json(WORKFLOW_FILE)
        self.policy_document = load_json(POLICY_FILE)
        self.asset_evidence: dict[str, Any] = {}

    def compose(
        self,
        *arguments: str,
        capture: bool = False,
        check: bool = True,
    ) -> subprocess.CompletedProcess[str]:
        command = ["docker", "compose", "-f", str(COMPOSE_FILE), *arguments]
        return subprocess.run(
            command,
            cwd=ROOT,
            env=self.compose_environment,
            check=check,
            capture_output=capture,
            text=True,
        )

    def prepare(self) -> None:
        self.run_dir.mkdir(parents=True, exist_ok=False)
        for relative in (
            "models/diffusion_models",
            "models/text_encoders",
            "models/vae",
            "input",
            "output",
            "temp",
            "user",
        ):
            (self.runtime_dir / relative).mkdir(parents=True, exist_ok=True)

        preflight = collect_preflight()
        (self.run_dir / "environment.json").write_text(
            json.dumps(preflight, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        if preflight["status"] != "pass":
            raise RuntimeError(f"host preflight failed: {preflight['errors']}")

        validate_policy(self.policy_document)
        model_statuses = verify_models(self.policy_document, self.runtime_dir)
        preflight["model_verification"] = model_statuses
        (self.run_dir / "environment.json").write_text(
            json.dumps(preflight, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        if any(item["status"] != "verified" for item in model_statuses):
            raise RuntimeError(
                "model verification failed; run tools/policy.py download first: "
                + json.dumps(model_statuses)
            )

        self.asset_evidence = generate_all(
            SHOT_FILE,
            self.runtime_dir / "input",
        )
        (self.run_dir / "reference-assets.json").write_text(
            json.dumps(self.asset_evidence, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )

        up_arguments = ["up", "--detach"]
        if not self.args.skip_build:
            up_arguments.append("--build")
        up_arguments.append("comfyui")
        self.compose(*up_arguments)
        self.client.wait_ready()
        self.client.validate_nodes()
        self._append_runtime_evidence(preflight)

    def _append_runtime_evidence(self, preflight: dict[str, Any]) -> None:
        image = self.compose(
            "images",
            "--format",
            "json",
            capture=True,
        )
        python_code = (
            "import json,platform,torch;"
            "print(json.dumps({'python':platform.python_version(),"
            "'torch':torch.__version__,'cuda':torch.version.cuda,"
            "'cuda_available':torch.cuda.is_available(),"
            "'gpu':torch.cuda.get_device_name(0) if torch.cuda.is_available() else None}))"
        )
        runtime = self.compose(
            "exec",
            "-T",
            "comfyui",
            "python",
            "-c",
            python_code,
            capture=True,
        )
        preflight["container"] = {
            "compose_images": image.stdout.strip(),
            "python_runtime": json.loads(runtime.stdout),
            "comfyui_commit": self.policy_document["artifacts"][0]["revision"],
            "workflow_template_sha256": sha256_file(WORKFLOW_FILE),
            "shot_manifest_sha256": sha256_file(SHOT_FILE),
            "supply_chain_sha256": sha256_file(POLICY_FILE),
        }
        (self.run_dir / "environment.json").write_text(
            json.dumps(preflight, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        if preflight["container"]["python_runtime"]["cuda_available"] is not True:
            raise RuntimeError(
                "PyTorch inside the ComfyUI container cannot access CUDA"
            )

    def selected_shots(self) -> list[dict[str, Any]]:
        shots = self.shot_document.get("shots")
        if not isinstance(shots, list) or len(shots) != 15:
            raise RuntimeError("benchmark requires exactly 15 configured shots")
        if self.args.shot:
            requested = set(self.args.shot)
            selected = [shot for shot in shots if shot["id"] in requested]
            missing = requested - {shot["id"] for shot in selected}
            if missing:
                raise RuntimeError(f"unknown shot ids: {sorted(missing)}")
            return selected
        return shots

    def patched_workflow(
        self,
        shot: dict[str, Any],
        phase: str,
        attempt: int,
    ) -> dict[str, Any]:
        workflow = copy.deepcopy(self.workflow_template)
        workflow["4"]["inputs"]["text"] = shot["prompt"]
        workflow["6"]["inputs"]["image"] = shot["reference_image"]
        workflow["9"]["inputs"]["seed"] = int(shot["seed"])
        workflow["12"]["inputs"]["filename_prefix"] = (
            f"flo-110/{self.run_id}/{shot['id']}-{phase}-attempt-{attempt}"
        )
        return workflow

    def _asset_sha(self, shot_id: str) -> str:
        for asset in self.asset_evidence["assets"]:
            if asset["shot_id"] == shot_id:
                return asset["sha256"]
        raise RuntimeError(f"missing generated asset evidence for {shot_id}")

    def _probe_and_normalize(
        self,
        raw_relative: PurePosixPath,
        delivery_relative: PurePosixPath,
    ) -> dict[str, Any]:
        completed = self.compose(
            "exec",
            "-T",
            "comfyui",
            "python",
            "/opt/vibe-forge-tools/video_probe.py",
            f"/opt/ComfyUI/output/{raw_relative}",
            "--normalize",
            f"/opt/ComfyUI/output/{delivery_relative}",
            capture=True,
        )
        return json.loads(completed.stdout)

    def run_attempt(
        self,
        shot: dict[str, Any],
        phase: str,
        attempt: int,
        startup_seconds: float | None,
    ) -> dict[str, Any]:
        logical_result_id = f"{self.run_id}:{shot['id']}:{phase}"
        result_id = f"{logical_result_id}:attempt-{attempt}"
        workflow = self.patched_workflow(shot, phase, attempt)
        telemetry_relative = (
            Path("telemetry") / f"{shot['id']}-{phase}-attempt-{attempt}.csv"
        )
        telemetry = TelemetrySampler(
            self.run_dir / telemetry_relative,
            self.args.gpu_index,
        )
        result: dict[str, Any] = {
            "schema_version": 1,
            "result_id": result_id,
            "logical_result_id": logical_result_id,
            "run_id": self.run_id,
            "shot_id": shot["id"],
            "category": shot["category"],
            "phase": phase,
            "attempt": attempt,
            "seed": shot["seed"],
            "prompt": shot["prompt"],
            "prompt_sha256": hashlib.sha256(shot["prompt"].encode("utf-8")).hexdigest(),
            "reference_image": shot["reference_image"],
            "reference_image_sha256": self._asset_sha(shot["id"]),
            "workflow_sha256": canonical_sha256(workflow),
            "workflow": workflow,
            "baseline": self.shot_document["baseline"],
            "startup_seconds": startup_seconds,
            "telemetry_file": str(telemetry_relative),
            "submitted_at": utc_now(),
        }
        started = time.monotonic()
        telemetry.start()
        try:
            prompt_id = self.client.queue(workflow)
            result["prompt_id"] = prompt_id
            history = self.client.wait_history(prompt_id, self.args.timeout_seconds)
            result["generation_elapsed_seconds"] = time.monotonic() - started
            saved_output = find_saved_output(history)
            raw_relative = safe_output_relative(saved_output)
            raw_host_path = self.runtime_dir / "output" / raw_relative
            if not raw_host_path.is_file():
                raise RuntimeError(f"ComfyUI reported a missing output: {raw_relative}")
            delivery_relative = (
                PurePosixPath("delivery")
                / self.run_id
                / f"{shot['id']}-{phase}-attempt-{attempt}.mp4"
            )
            postprocess_started = time.monotonic()
            media = self._probe_and_normalize(raw_relative, delivery_relative)
            result["postprocess_elapsed_seconds"] = (
                time.monotonic() - postprocess_started
            )
            result.update(
                {
                    "status": "success",
                    "raw_output": str(raw_relative),
                    "delivery_output": str(delivery_relative),
                    "media": media,
                    "history_status": history.get("status"),
                }
            )
        except Exception as error:  # noqa: BLE001 - every failure must become evidence
            result.update(
                {
                    "status": "failed",
                    "error_type": type(error).__name__,
                    "error": str(error),
                }
            )
        finally:
            telemetry.stop()
            result["completed_at"] = utc_now()
            result["total_elapsed_seconds"] = time.monotonic() - started
            result["peak_vram_mib"] = telemetry.peak_vram_mib
            result["telemetry_errors"] = telemetry.errors
        self._append_result(result)
        return result

    def _append_result(self, result: dict[str, Any]) -> None:
        with self.results_file.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(result, sort_keys=True) + "\n")
            handle.flush()
            os.fsync(handle.fileno())

    def run_with_retries(
        self,
        shot: dict[str, Any],
        phase: str,
        startup_seconds: float | None,
    ) -> bool:
        for attempt in range(1, self.args.max_attempts + 1):
            result = self.run_attempt(shot, phase, attempt, startup_seconds)
            if result["status"] == "success":
                return True
            error = str(result.get("error", "")).lower()
            if any(marker in error for marker in PERMANENT_ERROR_MARKERS):
                return False
            startup_seconds = None
        return False

    def run(self) -> None:
        self.prepare()
        for shot in self.selected_shots():
            self.compose("restart", "comfyui")
            startup_seconds = self.client.wait_ready()
            self.client.validate_nodes()
            cold_succeeded = self.run_with_retries(
                shot,
                "cold",
                startup_seconds,
            )
            if not cold_succeeded:
                continue
            self.run_with_retries(shot, "warm", None)

        summary_process = subprocess.run(
            [
                sys.executable,
                str(ROOT / "tools" / "summarize.py"),
                "--results",
                str(self.results_file),
                "--shots",
                str(SHOT_FILE),
                "--scores",
                str(self.args.scores),
                "--output",
                str(self.run_dir / "summary.json"),
            ],
            check=False,
        )
        if summary_process.returncode not in (0, 2):
            raise RuntimeError(
                f"summary command failed with exit code {summary_process.returncode}"
            )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--runtime-dir",
        type=Path,
        default=ROOT / "runtime",
        help="models, inputs, outputs, and mutable ComfyUI state",
    )
    parser.add_argument(
        "--evidence-dir",
        type=Path,
        default=ROOT / "runtime" / "evidence",
    )
    parser.add_argument("--run-id", help="explicit evidence run id")
    parser.add_argument("--comfy-url", default="http://127.0.0.1:8188")
    parser.add_argument("--gpu-index", type=int, default=0)
    parser.add_argument("--timeout-seconds", type=float, default=1800)
    parser.add_argument("--max-attempts", type=int, choices=(1, 2), default=2)
    parser.add_argument("--skip-build", action="store_true")
    parser.add_argument(
        "--shot", action="append", help="run only one shot id; repeatable"
    )
    parser.add_argument(
        "--scores",
        type=Path,
        default=ROOT / "runtime" / "scores.json",
        help="manual quality score records; may be absent during the generation pass",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.max_attempts != 2:
        print(
            "warning: FLO-98 D-11 defines exactly two attempts; reduced mode is diagnostic only",
            file=sys.stderr,
        )
    try:
        Benchmark(args).run()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as error:
        print(
            json.dumps({"status": "error", "error": str(error)}, indent=2),
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
