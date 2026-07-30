.PHONY: test video-bootstrap video-up video-up-tools video-down video-logs video-smoke video-secret-scan video-test

VIDEO_ENV := video-pipeline/.env.video
VIDEO_COMPOSE := docker compose --env-file $(VIDEO_ENV) -f video-pipeline/compose.yaml

test:
	go test ./...

video-bootstrap:
	@test -f $(VIDEO_ENV) || cp video-pipeline/.env.video.example $(VIDEO_ENV)

video-up: video-bootstrap
	$(VIDEO_COMPOSE) up --build --wait

video-up-tools: video-bootstrap
	$(VIDEO_COMPOSE) --profile tools up --build --wait

video-down: video-bootstrap
	$(VIDEO_COMPOSE) down

video-logs: video-bootstrap
	$(VIDEO_COMPOSE) logs --tail=200

video-smoke:
	./video-pipeline/scripts/smoke.sh

video-secret-scan:
	./video-pipeline/scripts/check-secrets.sh

video-test:
	go test -race ./internal/videopipeline/... ./video-pipeline/contracts
	go vet ./internal/videopipeline/... ./cmd/video-control-plane ./cmd/video-mock-provider ./cmd/video-orchestrator-worker
	docker compose --env-file video-pipeline/.env.video.example -f video-pipeline/compose.yaml config --quiet
	./video-pipeline/scripts/check-secrets.sh
