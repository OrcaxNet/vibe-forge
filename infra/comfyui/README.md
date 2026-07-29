# 24 GB NVIDIA / ComfyUI / Wan2.2 baseline

This directory is the reproducible FLO-110 GPU generation profile. It contains
the environment, native ComfyUI API workflow, deny-by-default model/node BOM,
15 deterministic I2V inputs, paired cold/warm runner, telemetry collector,
manual quality contract, and summary gate evaluator.

The repository does not contain model weights or generated video. A complete
benchmark run downloads 18,144,966,705 bytes of verified model data and creates
30 logical 5-second outputs (15 cold plus 15 warm), with at most one controlled
retry per logical output.

## Frozen versions

| Component | Frozen value |
| --- | --- |
| Host OS | Ubuntu 24.04 x86_64 |
| NVIDIA Linux driver | 570.124.06, exact for comparable evidence |
| NVIDIA Container Toolkit | 1.17.8 |
| Runtime base | `pytorch/pytorch:2.8.0-cuda12.8-cudnn9-runtime` |
| Base digest | `sha256:417bd75df6365104c283ea4c1651fb3530d9eb5a4c2fafa51943cff2a94e6385` |
| CUDA / cuDNN | 12.8 / 9 |
| PyTorch | 2.8.0 |
| ComfyUI | v0.24.0, `f49bdb655707b97952dcef40e12e5af1f08d2007` |
| Custom nodes | none; policy denies all |
| Video model | `wan2.2_ti2v_5B_fp16.safetensors` |
| VAE | `wan2.2_vae.safetensors` |
| Text encoder | `umt5_xxl_fp8_e4m3fn_scaled.safetensors` |
| Native sampling | 20 steps, CFG 5, `uni_pc`, `simple`, shift 8 |
| Raw render | I2V, 1280×704, 121 frames, 24 fps |
| Delivery master | 1280×720, 120 frames, 24 fps, 5.0 seconds |

`supply-chain.json` is authoritative for revisions, byte sizes, SHA-256 values,
sources, licenses, and policy scope. `requirements.lock` pins the non-PyTorch
Python environment; `/opt/comfyui-python-packages.txt` in the built image
captures the installed package inventory.

The NVIDIA driver is host-provided rather than included in the container.
570.124.06 is the CUDA 12.8 Update 1 toolkit driver baseline. A newer driver may
run the container, but its performance is not comparable evidence for this
profile and the strict preflight rejects it.

## Clean-host preparation

Use a dedicated Ubuntu 24.04 x86_64 workstation with one NVIDIA GPU reporting at
least 23,000 MiB, 128 GB system RAM and 4 TB NVMe recommended by FLO-98, Docker
Engine with Compose v2, Git, and Python 3.10 or later.

1. Install NVIDIA Linux driver 570.124.06 from the official NVIDIA driver
   repository, reboot, and resolve Secure Boot/module-signing issues before
   continuing. Do not install a CUDA toolkit on the host. Confirm the exact
   driver:

   ```bash
   nvidia-smi --query-gpu=name,memory.total,driver_version,uuid \
     --format=csv,noheader
   ```

2. Install and configure the frozen container runtime packages:

   ```bash
   sudo -v
   ./infra/comfyui/scripts/install_nvidia_container_toolkit_ubuntu2404.sh
   ```

3. Run the strict host contract before downloading weights:

   ```bash
   python3 infra/comfyui/tools/preflight.py
   ```

The installer intentionally does not mutate the GPU driver: driver installation
can require a reboot, kernel headers, and Secure Boot signing, so it remains an
explicit workstation-provisioning step. The preflight refuses wrong OS,
architecture, VRAM, driver, toolkit, Docker, or Compose state.

## Supply-chain gate and model installation

No dependency is downloaded until the policy validates. The default commercial
allowlist is exactly Apache-2.0, MIT, and BSD-3-Clause, matching FLO-98 D-13.
Unknown artifacts, unpinned sources, invalid destinations, unexpected model
counts, and all custom nodes are rejected.

```bash
python3 infra/comfyui/tools/policy.py validate
python3 infra/comfyui/tools/policy.py download
python3 infra/comfyui/tools/policy.py verify
```

Downloads resume into `.part` files and become visible to ComfyUI only after
both byte size and SHA-256 match. A mismatched final file is never overwritten
automatically. `HF_TOKEN` is optional for authenticated Hugging Face access and
is never written to evidence.

There are two explicit non-default-license scopes:

- ComfyUI is GPL-3.0-only. This profile permits internal PoC execution but does
  not authorize redistribution of the derived container; release requires
  legal approval and GPL compliance.
- CUDA/cuDNN container components use NVIDIA terms. Internal PoC use is recorded
  in the runtime BOM; redistribution or product packaging requires separate
  legal review.

## Build and smoke

The Compose file is separate from the application stack, uses native core nodes
only, mounts models read-only, drops all Linux capabilities, enables
`no-new-privileges`, and runs its root filesystem read-only.

```bash
docker compose -f infra/comfyui/compose.gpu.yaml config --quiet
docker compose -f infra/comfyui/compose.gpu.yaml up --build -d comfyui
curl --fail http://127.0.0.1:8188/system_stats
```

If the Linux user is not UID/GID 1000, export `COMFY_UID` and `COMFY_GID`.
Set `COMFY_RUNTIME_DIR` to an absolute path to keep multi-terabyte output on a
dedicated volume. Set `NVIDIA_VISIBLE_DEVICES` and `--gpu-index` together when
the workstation has more than one GPU.

The API workflow is
`workflows/wan22_ti2v_5b_i2v_api.json`. It is a server-ready conversion of the
official Comfy-Org Wan2.2 5B template pinned in the BOM. Startup validation
fetches `/object_info` and rejects missing native node classes before sampling.

## Full paired benchmark

Run the entire experiment from the repository root:

```bash
python3 infra/comfyui/tools/benchmark.py
```

Useful controlled options:

```bash
# Diagnostic one-shot run; not acceptance evidence.
python3 infra/comfyui/tools/benchmark.py --shot single-01

# Reuse an already built image.
python3 infra/comfyui/tools/benchmark.py --skip-build
```

For every shot, the runner:

1. restarts ComfyUI and waits for HTTP readiness;
2. runs the cold prompt, which includes model load/offload time;
3. runs the same seed, prompt and reference image without restart as the warm
   sample;
4. samples VRAM/utilization/temperature/power once per second;
5. polls ComfyUI history to terminal state;
6. probes the raw MP4 in the frozen container;
7. creates and probes an exact 1280×720 delivery master;
8. fsyncs one immutable JSONL attempt record.

A 121-frame Wan-native output is the smallest valid 5-second-class latent shape
because the temporal length is `4n+1`. Wan's native 720p-class height is 704,
which is divisible by 32. Delivery normalization keeps frames 1–120 and pads
eight black pixels above and below; it does not crop or stretch generated
content. Both raw and delivery hashes remain in the evidence.

Cold and warm are paired per shot. If cold generation never succeeds, no warm
result is recorded because no valid hot model state exists. FLO-98 D-11 permits
two attempts total. Missing models, invalid workflow, checksum/license failures,
and deterministic OOM are not retried unchanged.

## Quality review and summary

Generation automatically writes a preliminary `summary.json`. It remains
`incomplete` until every successful logical result has a manual score.

Create a score sheet without overwriting prior review:

```bash
python3 infra/comfyui/tools/prepare_scores.py \
  --results infra/comfyui/runtime/evidence/RUN_ID/results.jsonl \
  --output infra/comfyui/runtime/scores.json
```

Review the actual delivery MP4s and fill every null field according to
`benchmark/quality-rubric.json`. Then regenerate the conclusion:

```bash
python3 infra/comfyui/tools/summarize.py \
  --results infra/comfyui/runtime/evidence/RUN_ID/results.jsonl \
  --shots infra/comfyui/benchmark/shots.json \
  --scores infra/comfyui/runtime/scores.json \
  --output infra/comfyui/runtime/evidence/RUN_ID/summary.json
```

The summary uses nearest-rank p95. For 15 samples, p95 is the slowest sample.
The measured value is queue-to-ComfyUI-completion generation time: cold samples
include model loading/offload setup, while HTTP startup and deterministic MP4
normalization are recorded separately and excluded.
It evaluates the frozen FLO-98 gates:

- 5-second 720p delivery p95 ≤ 720 seconds;
- first-attempt success ≥ 85%;
- success within two attempts ≥ 95%;
- reference-consistency score ≥4 for at least 90%;
- motion/camera score ≥4 for at least 80% of the motion subset;
- severe-artifact score ≤2 for under 10%;
- all raw and delivery media match their frame, fps, duration, and size
  contracts.

FLO-109 owns the final gold-set rubric and may add stricter gates. FLO-110 does
not silently lower the frozen values.

## Evidence layout

Generated data is ignored by Git:

```text
infra/comfyui/runtime/
├── models/                         # three checksum-verified weight files
├── input/
│   ├── *.ppm                       # 15 deterministic synthetic references
│   └── reference-assets.json
├── output/
│   ├── flo-110/RUN_ID/...          # raw 1280x704 MP4
│   └── delivery/RUN_ID/...         # normalized 1280x720 MP4
├── scores.json                     # signed/manual quality observations
└── evidence/RUN_ID/
    ├── environment.json            # host, GPU, driver, container and hashes
    ├── reference-assets.json       # input lineage
    ├── results.jsonl               # every attempt, including failures
    ├── summary.json                 # p95, reliability, quality and gates
    └── telemetry/*.csv             # per-attempt GPU samples
```

Copy `benchmark/REPORT_TEMPLATE.md` into the evidence bundle and populate it
only from those machine-readable files. Preserve the complete bundle in CAS or
attach a compressed evidence set to the issue; local paths alone are not
delivery evidence.

## Offline verification

These checks require neither model weights nor a GPU:

```bash
python3 -m unittest discover -s infra/comfyui/tests -v
python3 -m compileall -q infra/comfyui/tools
python3 infra/comfyui/tools/policy.py validate
docker compose -f infra/comfyui/compose.gpu.yaml config --quiet
```

## Hard failure disposition

Do not substitute another video model or custom node inside this profile. If the
real run breaches p95, reliability, media, quality, or license gates, report the
measured bottleneck and verify one change at a time in this order:

1. 480p proxy plus deterministic final super-resolution;
2. reviewed FP8/native offload profile;
3. larger GPU;
4. separately reviewed Diffusers activity.

Any adopted change is a new versioned Generation Profile with new hashes and a
new benchmark. It does not rewrite this baseline's evidence.
