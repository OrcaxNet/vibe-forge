# Execution status

The environment, workflow, corpus, policy, runner, and offline tests are ready,
but the required hardware experiment has not run in this repository revision.

The assignment runner on 2026-07-30 was:

- macOS Darwin 25.3.0;
- Apple arm64 / M4 integrated GPU;
- no `nvidia-smi`;
- no NVIDIA Container Toolkit.

The strict preflight correctly failed on OS, architecture, NVIDIA GPU, and
container-toolkit requirements. Therefore this revision contains no claimed
Wan2.2 output, VRAM measurement, latency p95, failure rate, retry result, or
quality score. Any such number without a complete evidence bundle would be
fabricated.

To clear the blocker, run the procedure in `../README.md` on the frozen Ubuntu
24.04 / x86_64 / 24 GB NVIDIA / driver 570.124.06 host, review all 30 logical
outputs, preserve the evidence bundle, and replace this status with the report
derived from `summary.json`.
