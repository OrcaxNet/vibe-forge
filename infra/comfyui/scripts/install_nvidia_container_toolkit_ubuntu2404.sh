#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]] || [[ "$(uname -m)" != "x86_64" ]]; then
  echo "This installer supports only Linux x86_64." >&2
  exit 1
fi

source /etc/os-release
if [[ "${ID:-}" != "ubuntu" ]] || [[ "${VERSION_ID:-}" != "24.04" ]]; then
  echo "This installer requires Ubuntu 24.04." >&2
  exit 1
fi

toolkit_version="1.17.8-1"
keyring="/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg"
list_file="/etc/apt/sources.list.d/nvidia-container-toolkit.list"

curl --fail --silent --show-error --location \
  https://nvidia.github.io/libnvidia-container/gpgkey \
  | sudo gpg --dearmor --yes --output "${keyring}"
curl --fail --silent --show-error --location \
  https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
  | sed "s#deb https://#deb [signed-by=${keyring}] https://#g" \
  | sudo tee "${list_file}" >/dev/null

sudo apt-get update
sudo apt-get install --yes \
  "nvidia-container-toolkit=${toolkit_version}" \
  "nvidia-container-toolkit-base=${toolkit_version}" \
  "libnvidia-container-tools=${toolkit_version}" \
  "libnvidia-container1=${toolkit_version}"
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker

nvidia-ctk --version
docker run --rm --gpus all \
  --platform linux/amd64 \
  nvidia/cuda:12.8.1-base-ubuntu24.04@sha256:e711c99333fdfe8ae1e677b4972be6c5021f0128a1d31f775c7e58d88921b6a9 \
  nvidia-smi
