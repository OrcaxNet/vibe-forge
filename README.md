# Vibe Forge

Turn a one-line idea into a running React app through a **single agent loop**
of four real, serial stages — `pm → architect → engineer → qa` — observed live
via SSE, previewed in-browser with **Sandpack**, and persisted to **SQLite** as
the single source of truth.

> **Stage 4.** The full local-first loop is wired: workbench UI (FLO-54), SQLite
> persistence/API (FLO-55), Sandpack preview + file/version workflow (FLO-56),
> the single agent loop + SSE + auto-repair (FLO-60), and OrbStack local
> integration + restart recovery (FLO-59). A fresh clone is usable on OrbStack
> with one command; a run interrupted by a restart is reconciled to
> `interrupted` and retried with a new attempt. Online deploy (FLO-57) and the
> final submission pack (FLO-62) are the remaining stages.

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

One command brings up the frontend, backend and SQLite volume on OrbStack:

```bash
cp .env.example .env        # then edit .env and set model credentials
docker compose up --build
```

- Frontend: http://localhost:5173
- Backend health: http://localhost:8787/api/health

A fresh clone is usable within a couple of minutes once base images are cached
(`--build` compiles the Go binary and Vite assets). The first build pulls the
`golang`, `node` and `nginx` base images; subsequent builds are fast.

The agent loop needs model credentials - pick **one** auth mode in `.env`:

- **Mode A (default):** `ANTHROPIC_API_KEY=sk-...` (direct Anthropic API).
- **Mode B (gateway):** `ANTHROPIC_AUTH_TOKEN` + `ANTHROPIC_BASE_URL` for an
  Anthropic-compatible platform proxy (also set `ANTHROPIC_MODEL` to the name
  your gateway expects).

Until credentials are set, `/api/health` returns `503` with a
structurally-correct, sanitized body (`database: ok`, `model: not_configured`)
and the backend logs an actionable warning naming the exact variables to set.
Run creation is rejected until the model is configured; the rest of the API
(projects, versions, files) still serves.

> ⚠️ `docker compose down -v` **deletes the SQLite volume and all project
> data.** Use `docker compose down` (without `-v`) to keep data across restarts.
> `docker compose restart` and `down`/`up` (no `-v`) both preserve the volume;
> see [Restart recovery](#restart-recovery) below.

## Restart recovery

SQLite on the mounted volume is the single source of truth, so a restart never
loses committed data. A run that is mid-flight (`queued`/`running`) when the
backend stops is reconciled on the next startup:

- On boot the backend flips every `queued`/`running` run to `interrupted`
  (`reconciled N interrupted run(s)` log line) and releases the per-project
  active-run lock. It never resumes a run as if it never stopped - an
  interrupted run is surfaced to the client as a retryable `run_failed` over
  SSE.
- `POST /api/runs/:id/retry` starts a **new attempt** (retry never fakes
  "continue"); it is idempotent on `Idempotency-Key`, so a repeated retry does
  not create a second attempt.
- `docker compose restart` and `down`/`up` (without `-v`) both preserve
  projects, messages, stage artifacts, files and `stableVersionId`; terminal
  runs (`succeeded`/`failed`/`interrupted`) are never resurrected.

## Environment variables

See [`.env.example`](./.env.example). Required (one mode): `ANTHROPIC_API_KEY`,
or `ANTHROPIC_AUTH_TOKEN` + `ANTHROPIC_BASE_URL`. Optional: `ANTHROPIC_MODEL`,
`PORT`/`DATABASE_PATH` (overridable via compose). The backend logs an actionable
warning at startup when no credentials are present.

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
cmd/server/       backend entrypoint (startup, reconcile, redacting logger)
internal/api/     HTTP router + persistence REST API + SSE/retry/compile-result
internal/agent/   single agent loop (PM->Architect->Engineer->QA) + SSE + auto-repair
internal/db/      SQLite open (WAL, foreign_keys) + idempotent migration runner + 0001_init.sql
internal/store/   SQLite store: projects, runs, versions, files; idempotency + atomic version commit
internal/logredact/ secret-scrubbing logger for run-lifecycle lines
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

**Done (Stage 1–4):** public repo, workbench UI, lockfiles, license,
`compose.yaml`, `.env.example`, frozen shared contract, SQLite store (WAL +
foreign_keys + idempotent migration), persistence REST API, stable Sandpack
preview, files-map integrity verification, App.tsx editing, version
history/restore and manual-vs-agent locking, single agent loop (PM -> Architect
-> Engineer -> QA) with SSE stream + `Last-Event-ID` replay and bounded
auto-repair, OrbStack local integration with restart recovery (interrupted-run
reconcile + retry), bearer-token gateway auth, sanitized run-lifecycle logging;
all backed by automated tests.

**Not done (later stages):** online deploy (FLO-57) and final submission pack
(FLO-62).

## Frontend preview dependencies

| Dependency | Pinned version | License | Purpose |
| --- | --- | --- | --- |
| `@codesandbox/sandpack-react` | `2.20.0` | Apache-2.0 | The only MVP browser compiler/runtime and preview surface |
| `react` / `react-dom` | `18.3.1` | MIT | Workbench and generated-app runtime |
| `playwright-core` | `1.62.0` (dev only) | Apache-2.0 | Local Chrome smoke tests; never shipped to production |

## License

[MIT](./LICENSE).
