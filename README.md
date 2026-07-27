# Vibe Forge

Turn a one-line idea into a running React app through a **single agent loop**
of four real, serial stages — `pm → architect → engineer → qa` — observed live
via SSE, previewed in-browser with **Sandpack**, and persisted to **SQLite** as
the single source of truth.

> **Stage 3 frontend.** The repository now includes the workbench UI (FLO-54),
> SQLite persistence/API (FLO-55), and the Sandpack preview, file editor and
> version workflow (FLO-56). The agent loop/SSE backend (FLO-60) remains the
> active integration dependency. Today you get a runnable frontend + backend,
> `GET /api/health`, idempotent migrations, the project/run/version/file REST
> API, and a real in-browser stable-preview path.

## Architecture (main path)

- **Frontend** — React + TypeScript + Vite + Tailwind. The workbench includes
  home, Build Pulse, preview, files and versions. `/src/App.tsx` is the only
  manually writable generated file; active agent runs lock manual saving.
- **Backend** — Go monolith. Serves `/api/health` and the persistence-layer REST
  API (projects, runs, versions, files) backed by the SQLite store; the agent-loop
  endpoints (SSE, retry, compile-result) land in FLO-60.
- **Preview** — **Sandpack** (in-browser) is the only MVP preview path. The
  frontend verifies the server `filesHash`, compiles a candidate in a sandboxed
  iframe, and keeps the previous interactive preview mounted until the candidate
  is ready. No per-project Docker/Vite preview containers or traefik.
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
npm ci
npm run smoke          # unit tests + tsc typecheck + vite production build
npm run test:e2e       # mocked-contract Chrome smoke incl. Sandpack + 375px
npm run dev            # dev server on :5173 (proxies /api to :8787)
```

## Repository layout

```
contracts/        shared contract (single source of truth) — JSON + Go embed + README
  contract.json   ← frozen from PRD-A/B/C §5
  contract.go     ← backend typed accessors (go:embed)
  contract_test.go
cmd/server/       backend entrypoint
internal/api/     HTTP router + persistence REST API (projects, runs, versions, files)
internal/db/      SQLite open (WAL, foreign_keys) + idempotent migration runner + 0001_init.sql
internal/store/   SQLite store: projects, runs, versions, files; idempotency + atomic version commit
frontend/         React + TS + Vite + Tailwind workbench
  src/App.tsx     ← home, Build Pulse and workspace orchestration
  src/WorkspacePanel.tsx ← Sandpack preview, file editor and version history
  src/workspace.ts ← files-map hashing, normalization and preview guards
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

**Done (Stage 1–3 frontend):** public repo, workbench UI, lockfiles, license,
`compose.yaml`, `.env.example`, frozen shared contract, SQLite store (WAL +
foreign_keys + migration), persistence REST API, stable Sandpack preview,
files-map integrity verification, App.tsx editing, version history/restore and
manual-vs-agent locking; all backed by automated tests.

**Not done (later stages):** single agent loop, live SSE/compile-result backend
wiring, online deploy and final QA.

## Frontend preview dependencies

| Dependency | Pinned version | License | Purpose |
| --- | --- | --- | --- |
| `@codesandbox/sandpack-react` | `2.20.0` | Apache-2.0 | The only MVP browser compiler/runtime and preview surface |
| `react` / `react-dom` | `18.3.1` | MIT | Workbench and generated-app runtime |
| `playwright-core` | `1.62.0` (dev only) | Apache-2.0 | Local Chrome smoke tests; never shipped to production |

## License

[MIT](./LICENSE).
