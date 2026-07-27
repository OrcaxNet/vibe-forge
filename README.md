# Vibe Forge

Turn a one-line idea into a running React app through a **single agent loop**
of four real, serial stages — `pm → architect → engineer → qa` — observed live
via SSE, previewed in-browser with **Sandpack**, and persisted to **SQLite** as
the single source of truth.

> **Stage 1 skeleton.** This repository is the engineering baseline and frozen
> shared contract. The real workbench UI (FLO-54), data model/API (FLO-55),
> Sandpack editor (FLO-56) and agent loop (FLO-60) are built on top of it in
> later stages. Today you get: a public repo, a runnable frontend + backend, a
> `GET /api/health` endpoint, a SQLite migration skeleton, and the one contract
> file both sides consume.

## Architecture (main path)

- **Frontend** — React + TypeScript + Vite + Tailwind. The workbench (home,
  project workspace, Build Pulse). In Stage 1 it renders the four stages and
  pings `/api/health`.
- **Backend** — Go monolith. Serves `/api/health` for real in Stage 1; the other
  contract paths are `501` stubs replaced by FLO-55 / FLO-60.
- **Preview** — **Sandpack** (in-browser) is the only MVP preview path. No
  per-project Docker/Vite preview containers, no traefik.
- **Persistence** — **SQLite** on a declared volume is the only source of truth.
  No IndexedDB.
- **Contract** — `contracts/contract.json` is the single source of truth for
  state enums, REST paths, SSE events, idempotency and error structure. The Go
  backend embeds it (`//go:embed`); the React frontend imports it directly. They
  cannot drift.

## Quick start (OrbStack / Docker, local-first)

```bash
cp .env.example .env        # then edit .env and set ANTHROPIC_API_KEY
docker compose up --build
```

- Frontend: http://localhost:5173
- Backend health: http://localhost:8787/api/health

Until `ANTHROPIC_API_KEY` is set, `/api/health` returns `503` with a
structurally-correct, sanitized body (`database: ok`, `model: not_configured`).
That is the intended "not ready" state for the skeleton.

> ⚠️ `docker compose down -v` **deletes the SQLite volume and all project
> data.** Use `docker compose down` (without `-v`) to keep data across restarts.

## Environment variables

See [`.env.example`](./.env.example). Required: `ANTHROPIC_API_KEY`. The backend
logs an actionable warning at startup when it is missing.

## Smoke commands

Backend (Go):

```bash
go test ./...                         # unit tests incl. contract + migration idempotency
go run ./cmd/server &                 # start on :8787
curl -s http://127.0.0.1:8787/api/health   # 503 + structured JSON
```

Frontend (Node):

```bash
cd frontend
npm install
npm run smoke          # tsc typecheck + vite production build
npm run dev            # dev server on :5173 (proxies /api to :8787)
```

## Repository layout

```
contracts/        shared contract (single source of truth) — JSON + Go embed + README
  contract.json   ← frozen from PRD-A/B/C §5
  contract.go     ← backend typed accessors (go:embed)
  contract_test.go
cmd/server/       backend entrypoint
internal/api/     HTTP router + /api/health (501 stubs for the rest)
internal/db/      SQLite open + idempotent migration runner + 0001_init.sql
frontend/         React + TS + Vite + Tailwind workbench
  src/App.tsx     ← Stage 1 business component (reads the contract)
  src/contract.ts ← typed frontend view of contracts/contract.json
scaffold/         fixed React + Tailwind scaffold for Sandpack (only /src/App.tsx writable)
compose.yaml      backend + frontend + SQLite volume + healthchecks
Dockerfile.backend / Dockerfile.frontend   multi-stage, no runtime npm install
.env.example      required + optional env vars
```

## Contract at a glance

| Section | Covers |
| --- | --- |
| `states` | `Project` (draft/active/archived), `Run` (queued/running/succeeded/failed/interrupted), `Version` (draft/validating/stable/failed) |
| `events` | 8 unified SSE events + `Last-Event-ID` replay (`run_started`, `stage_started`, `stage_artifact`, `message_delta`, `file_written`, `preview_ready`, `run_failed`, `run_completed`) |
| `paths` | 13 REST endpoints incl. `GET /api/health` |
| `idempotency` | `Idempotency-Key` header, 30s TTL |
| `errors` | stable `code` / `message` / `retryable` + HTTP map |
| `limits` | prompt 1–4000 chars, file 200 KB, only `/src/App.tsx` writable |

See [`contracts/README.md`](./contracts/README.md) for the full breakdown.

## Status

**Done (Stage 1):** public repo, frontend + backend skeleton, lockfiles, license,
`compose.yaml`, `.env.example`, SQLite migration skeleton, runnable
`/api/health`, frozen shared contract consumed by both sides.

**Not done (later stages):** workbench UI, real data model/transactions/API,
Sandpack editor, single agent loop, SSE, online deploy, QA.

## License

[MIT](./LICENSE).
