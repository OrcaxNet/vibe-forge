#!/bin/sh
set -eu

control_plane_url="${VIDEO_CONTROL_PLANE_URL:-http://127.0.0.1:18080}"
provider_url="${VIDEO_PROVIDER_ADAPTER_URL:-http://127.0.0.1:8090}"

info="$(curl -fsS "${control_plane_url}/video-api/v1/system/info")"
printf '%s' "${info}" | grep -Fq '"generationExecution":"remote-provider-api"'
printf '%s' "${info}" | grep -Fq '"gpuRequired":false'

ready="$(curl -fsS "${control_plane_url}/health/ready")"
printf '%s' "${ready}" | grep -Fq '"status":"ready"'

provider_status="$(curl -fsS "${control_plane_url}/video-api/v1/providers/status")"
printf '%s' "${provider_status}" | grep -Fq '"mode":"dry-run"'
test "$(printf '%s' "${provider_status}" | grep -o '"liveConfigured":false' | wc -l | tr -d ' ')" = "4"

payload='{"schemaVersion":"v1","jobId":"smoke-job","runId":"smoke-run","capability":"video.primary","inputHash":"0000000000000000000000000000000000000000000000000000000000000000","modelSnapshot":{"capabilityAlias":"video.primary","provider":"mock","modelId":"fixture-video-v1","routeVersion":"mock-routes-v1","capabilityHash":"0000000000000000000000000000000000000000000000000000000000000000"},"request":{"prompt":"smoke","durationSeconds":5},"budgetReservation":{"reservationId":"smoke-budget","currency":"CNY","amountMinor":150,"pricingVersion":"mock-pricing-v1","confirmedBy":"smoke-reviewer"},"traceId":"smoke-trace"}'
first="$(curl -fsS -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-job' --data "${payload}" "${provider_url}/v1/jobs")"
second="$(curl -fsS -H 'Content-Type: application/json' -H 'Idempotency-Key: smoke-job' --data "${payload}" "${provider_url}/v1/jobs")"
first_task="$(printf '%s' "${first}" | sed -n 's/.*"upstreamTaskId":"\([^"]*\)".*/\1/p')"
second_task="$(printf '%s' "${second}" | sed -n 's/.*"upstreamTaskId":"\([^"]*\)".*/\1/p')"
test -n "${first_task}"
test "${first_task}" = "${second_task}"
completed="$(curl -fsS "${provider_url}/v1/jobs/smoke-job")"
printf '%s' "${completed}" | grep -Fq '"state":"SUCCEEDED"'
printf '%s' "${completed}" | grep -Eq '"uri":"cas://sha256/[0-9a-f]{64}"'

postgres_container="${VIDEO_POSTGRES_CONTAINER:-vibe-forge-video-pipeline-postgres-1}"
migration_version="$(docker exec "${postgres_container}" psql -U video -d video_pipeline -Atc 'SELECT version FROM public.schema_migrations;')"
migration_dirty="$(docker exec "${postgres_container}" psql -U video -d video_pipeline -Atc 'SELECT dirty FROM public.schema_migrations;')"
test "${migration_version}" = "1"
test "${migration_dirty}" = "f"
table_count="$(docker exec "${postgres_container}" psql -U video -d video_pipeline -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema='video_pipeline';")"
test "${table_count}" -ge 42

temporal_container="${VIDEO_TEMPORAL_CONTAINER:-vibe-forge-video-pipeline-temporal-1}"
temporal_ip="$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${temporal_container}")"
workflow_id="video-smoke-$(date +%s)"
temporal_address="${temporal_ip}:7233"

docker exec "${temporal_container}" tctl --address "${temporal_address}" workflow start \
  --taskqueue video-production-v1 \
  --workflow_id "${workflow_id}" \
  --workflow_type video.production.episode.v1 \
  --workflowidreusepolicy RejectDuplicate \
  --execution_timeout 300 \
  --input '{"schemaVersion":"v1","seriesId":"series-smoke","episodeRevisionId":"episode-revision-smoke","shotSpecRevisionIds":["shot-revision-smoke"],"generationProfileRef":"profile-revision-smoke","gate2DecisionId":"gate2-smoke","providerRoute":{"capabilityAlias":"video.primary","provider":"mock","modelId":"fixture-video-v1","routeVersion":"mock-routes-v1","capabilityHash":"0000000000000000000000000000000000000000000000000000000000000000"},"budgetApprovalId":"budget-smoke","budgetMaximumMinor":500,"budgetCurrency":"CNY","traceId":"trace-smoke"}' >/dev/null
docker exec "${temporal_container}" tctl --address "${temporal_address}" workflow signal \
  --workflow_id "${workflow_id}" \
  --name video.production.gate3-decision.v1 \
  --input '{"decisionId":"gate3-smoke","approved":true,"actorId":"reviewer-smoke"}' >/dev/null
workflow_result="$(docker exec "${temporal_container}" tctl --address "${temporal_address}" workflow observe --workflow_id "${workflow_id}")"
printf '%s' "${workflow_result}" | grep -Fq '"state":"LOCKED"'

echo "video-pipeline smoke passed"
