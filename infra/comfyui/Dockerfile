# syntax=docker/dockerfile:1.7

ARG PYTORCH_IMAGE=pytorch/pytorch:2.8.0-cuda12.8-cudnn9-runtime@sha256:417bd75df6365104c283ea4c1651fb3530d9eb5a4c2fafa51943cff2a94e6385
ARG TARGETPLATFORM=linux/amd64

FROM --platform=${TARGETPLATFORM} ${PYTORCH_IMAGE} AS dependencies

ADD --checksum=sha256:93e86751a0567e19db55dc34e3061663390b44fd5f9a1a10d3e6a37c35c484a4 \
    https://github.com/Comfy-Org/ComfyUI/archive/f49bdb655707b97952dcef40e12e5af1f08d2007.tar.gz \
    /tmp/comfyui.tar.gz

RUN mkdir -p /opt/ComfyUI \
    && tar --extract --gzip --file /tmp/comfyui.tar.gz \
        --directory /opt/ComfyUI --strip-components=1 \
    && rm /tmp/comfyui.tar.gz

COPY infra/comfyui/requirements.lock /tmp/requirements.lock

RUN python -m venv --system-site-packages /opt/comfyui-venv \
    && /opt/comfyui-venv/bin/python -m pip install \
        --no-cache-dir \
        --disable-pip-version-check \
        --no-deps \
        -r /tmp/requirements.lock \
    && /opt/comfyui-venv/bin/python -c \
        "import torch; assert torch.__version__.split('+')[0] == '2.8.0'; assert torch.version.cuda == '12.8'" \
    && /opt/comfyui-venv/bin/python -m pip check \
    && /opt/comfyui-venv/bin/python -m pip freeze \
        > /opt/comfyui-python-packages.txt

FROM --platform=${TARGETPLATFORM} ${PYTORCH_IMAGE} AS runtime

LABEL org.opencontainers.image.title="Vibe Forge ComfyUI Wan2.2 baseline" \
      org.opencontainers.image.source="https://github.com/OrcaxNet/vibe-forge" \
      org.opencontainers.image.version="comfyui-v0.24.0-pytorch-2.8.0-cu128" \
      org.opencontainers.image.revision="f49bdb655707b97952dcef40e12e5af1f08d2007" \
      org.opencontainers.image.licenses="GPL-3.0-only"

RUN groupadd --gid 10001 comfy \
    && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/comfy comfy

COPY --from=dependencies --chown=10001:10001 /opt/ComfyUI /opt/ComfyUI
COPY --from=dependencies --chown=10001:10001 /opt/comfyui-venv /opt/comfyui-venv
COPY --from=dependencies --chown=10001:10001 /opt/comfyui-python-packages.txt /opt/comfyui-python-packages.txt
COPY --chown=10001:10001 infra/comfyui/tools/video_probe.py /opt/vibe-forge-tools/video_probe.py

ENV PATH="/opt/comfyui-venv/bin:${PATH}" \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HOME=/home/comfy \
    HF_HOME=/opt/ComfyUI/user/huggingface \
    XDG_CACHE_HOME=/opt/ComfyUI/user/cache \
    TORCH_HOME=/opt/ComfyUI/user/torch \
    TRITON_CACHE_DIR=/opt/ComfyUI/user/triton

WORKDIR /opt/ComfyUI
USER 10001:10001

EXPOSE 8188

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=12 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8188/system_stats', timeout=2).read()"]

ENTRYPOINT ["python", "main.py"]
CMD ["--listen", "0.0.0.0", "--port", "8188", "--disable-auto-launch"]
