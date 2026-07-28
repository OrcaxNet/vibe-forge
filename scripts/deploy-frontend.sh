#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repository_root"

if [ -n "$(git status --porcelain)" ]; then
  echo "Refusing to stamp a dirty worktree as a committed deployment" >&2
  exit 1
fi

revision=$(git rev-parse HEAD)
public_url=${PUBLIC_URL:-https://vf.floatflow.com}
attempts=${VERIFY_ATTEMPTS:-12}

BUILD_REVISION="$revision" docker compose build frontend
BUILD_REVISION="$revision" docker compose up -d frontend

attempt=1
while [ "$attempt" -le "$attempts" ]; do
  if node frontend/scripts/verify-deployment.mjs "$public_url" "$revision"; then
    exit 0
  fi
  if [ "$attempt" -eq "$attempts" ]; then
    break
  fi
  sleep 5
  attempt=$((attempt + 1))
done

echo "Frontend deployment did not converge to $revision after $attempts attempts" >&2
exit 1
