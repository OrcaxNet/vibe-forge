# FLO-110 Wan2.2 TI2V-5B benchmark report

This file is a reporting contract, not benchmark evidence. Populate it only
from one complete `summary.json`, `environment.json`, `results.jsonl`, the
telemetry CSV files, and the corresponding reviewed `scores.json`.

## Result

- Run ID:
- Date:
- Conclusion: pass / fail / incomplete
- GPU and driver:
- Container image ID:
- ComfyUI revision:
- Model SHA-256 verification: pass / fail

## Coverage

- Single-person: 5 cold + 5 warm
- Two-person/prop: 5 cold + 5 warm
- Motion/camera: 5 cold + 5 warm
- Attempt records:
- Missing or failed logical results:

## Performance and reliability

| Metric | FLO-98 gate | Measured | Result |
| --- | ---: | ---: | --- |
| 5-second shot overall p95 | ≤720 s | | |
| Cold p95 | observation | | |
| Warm p95 | observation | | |
| Peak VRAM | ≤available VRAM | | |
| First-attempt success | ≥85% | | |
| Success within two attempts | ≥95% | | |
| Failure rate | derived | | |

State the nearest-rank p95 sample count, whether model loading is included, and
the difference between cold and warm behavior.

## Media contract

- Raw Wan render: 1280×704, 121 frames, 24 fps.
- Delivery master: 1280×720, 120 frames, 24 fps, 5.0 seconds.
- Normalization: discard frame 121 and add 8 black pixels at the top and bottom;
  do not crop or stretch the generated content.
- Playback/codec failures:

## Quality

| Metric | FLO-98 gate | Measured | Result |
| --- | ---: | ---: | --- |
| Score coverage | 100% | | |
| Reference-consistency score ≥4 | ≥90% | | |
| Motion/camera score ≥4 on motion subset | ≥80% | | |
| Severe artifact score ≤2 | <10% | | |

List recurring identity drift, prop duplication, temporal artifacts, failed
actions, and camera-control defects. Link each observation to a result ID.

## Retries and failures

For each retry, record the first failure, whether it was infrastructure or
creative quality, the permitted parameter difference, and the second result.
Deterministic OOM, missing model, invalid workflow, checksum, and license errors
must not be retried unchanged.

## Bottleneck and decision impact

- Primary bottleneck:
- Verified optimization candidate:
- Verification experiment:
- Effect on the frozen Wan2.2/720p/24fps/I2V baseline:
- Required fallback if a hard gate fails:

Do not silently substitute another model. The FLO-98 fallback order is:
480p proxy plus final SR, FP8/offload, a larger GPU, then a separately reviewed
Diffusers activity or explicit baseline decision.

## Supply-chain disposition

- Unknown/unallowlisted artifacts: must be zero.
- Custom nodes: must be zero.
- ComfyUI GPL-3.0 and NVIDIA runtime components are scoped to internal PoC use;
  record legal approval before distributing a derived container.
