#!/bin/sh
set -eu

suffix="$$"
network_name="vf-auth-boundary-${suffix}"
backend_name="vf-auth-boundary-backend-${suffix}"
frontend_name="vf-auth-boundary-frontend-${suffix}"
backend_image="vibe-forge-backend:auth-boundary-${suffix}"
frontend_image="vibe-forge-frontend:auth-boundary-${suffix}"

cleanup() {
  docker rm -f "$frontend_name" "$backend_name" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
  docker image rm "$frontend_image" "$backend_image" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_for_health() {
  health_url="$1"
  attempt=0
  until curl --silent --fail "$health_url" >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
      echo "timed out waiting for $health_url" >&2
      docker inspect --format '{{.State.Status}} {{.State.Error}}' "$backend_name" >&2 || true
      docker logs "$backend_name" >&2 || true
      return 1
    fi
    sleep 1
  done
}

assert_rate_limit_sequence() {
  login_url="$1"
  attempt=1
  while [ "$attempt" -le 6 ]; do
    status="$(
      curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --header 'Content-Type: application/json' \
        --header "X-Forwarded-For: 198.51.100.${attempt}" \
        --header "X-Real-IP: 203.0.113.${attempt}" \
        --data-binary '{"password":"wrong"}' \
        "$login_url"
    )"
    expected=401
    if [ "$attempt" -eq 6 ]; then
      expected=429
    fi
    if [ "$status" -ne "$expected" ]; then
      echo "attempt $attempt at $login_url returned $status, want $expected" >&2
      return 1
    fi
    attempt=$((attempt + 1))
  done
}

docker network create "$network_name" >/dev/null
subnet="$(
  docker network inspect \
    --format '{{(index .IPAM.Config 0).Subnet}}' \
    "$network_name"
)"
network_address="${subnet%/*}"
proxy_ip="${network_address%.*}.10"

docker build --quiet -f Dockerfile.backend -t "$backend_image" . >/dev/null
docker build --quiet -f Dockerfile.frontend -t "$frontend_image" . >/dev/null

docker run -d \
  --name "$backend_name" \
  --network "$network_name" \
  --network-alias backend \
  -p 127.0.0.1::8787 \
  -e PORT=8787 \
  -e DATABASE_PATH=/data/smoke.db \
  -e APP_ENV=development \
  -e APP_ACCESS_PASSWORD=smoke-access-value-not-for-deployment \
  -e APP_AUTH_SESSION_SECRET=smoke-session-secret-at-least-thirty-two-bytes-long \
  -e "APP_AUTH_TRUSTED_PROXY_CIDRS=${proxy_ip}/32" \
  -e ANTHROPIC_API_KEY=smoke-key \
  -e ANTHROPIC_MODEL=smoke-model \
  "$backend_image" >/dev/null

docker run -d \
  --name "$frontend_name" \
  --network "$network_name" \
  --ip "$proxy_ip" \
  -p 127.0.0.1::80 \
  "$frontend_image" >/dev/null

backend_port="$(
  docker port "$backend_name" 8787/tcp |
    sed -n 's/^127\.0\.0\.1://p'
)"
frontend_port="$(
  docker port "$frontend_name" 80/tcp |
    sed -n 's/^127\.0\.0\.1://p'
)"
backend_url="http://127.0.0.1:${backend_port}"
frontend_url="http://127.0.0.1:${frontend_port}"

wait_for_health "${backend_url}/api/health"
assert_rate_limit_sequence "${backend_url}/api/auth/login"

docker restart "$backend_name" >/dev/null
backend_port="$(
  docker port "$backend_name" 8787/tcp |
    sed -n 's/^127\.0\.0\.1://p'
)"
backend_url="http://127.0.0.1:${backend_port}"
wait_for_health "${backend_url}/api/health"
assert_rate_limit_sequence "${frontend_url}/api/auth/login"

echo "auth proxy boundary smoke passed: direct and nginx paths returned 401 x5, then 429"
