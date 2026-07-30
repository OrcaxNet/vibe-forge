#!/bin/sh
set -eu

secret_assignments='(VIDEO_ARK_API_KEY|VIDEO_CLAUDE_API_KEY|VIDEO_DOUBAO_TTS_ACCESS_TOKEN|ANTHROPIC_API_KEY|Authorization|Proxy-Authorization)=[^<[:space:]][^[:space:]]*'
if git grep -nE "${secret_assignments}" -- . ':(exclude)video-pipeline/scripts/check-secrets.sh' >/dev/null 2>&1; then
  echo "tracked files contain a forbidden credential assignment" >&2
  git grep -nE "${secret_assignments}" -- . ':(exclude)video-pipeline/scripts/check-secrets.sh' >&2
  exit 1
fi

if git grep -nE '(^|[^A-Za-z0-9])(sk-ant-|Bearer[[:space:]]+[A-Za-z0-9+/_.=-]{16,})' -- . ':(exclude)video-pipeline/scripts/check-secrets.sh' >/dev/null 2>&1; then
  echo "tracked files contain a key/token-shaped value" >&2
  exit 1
fi

git check-ignore -q video-pipeline/.env.video
grep -Fq 'video-pipeline/.env.video' .dockerignore

echo "video-pipeline tracked secret scan passed"
