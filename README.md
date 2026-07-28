# Vibe Forge

Turn one idea into a running React app. Vibe Forge moves the request through one
real, serial agent loop — **PM → Architect → Engineer → QA** — streams progress
over SSE, previews the result in **Sandpack**, and persists projects and versions
in **SQLite**.

[Open the live demo](https://vf.floatflow.com)
· [View the public repository](https://github.com/OrcaxNet/vibe-forge)
· [Inspect the deployed frontend revision](https://github.com/OrcaxNet/vibe-forge/commit/c5d56cff9dc5139c09d2b1772e86f32c6051bb74)

> The anonymous demo uses a Cloudflare named tunnel with the fixed
> `vf.floatflow.com` hostname and runs on a local OrbStack host. The URL remains
> stable across tunnel restarts, but the single-host deployment is not a
> highly available production service; see
> [Known limitations](#known-limitations-and-next-steps).

## What you can demo

1. Describe an app on the home page or select an example prompt.
2. Watch the server-driven Build Pulse advance through PM, Architect, Engineer,
   and QA.
3. Use the generated app in the sandboxed Sandpack preview.
4. Inspect `/src/App.tsx`, versions, and stage artifacts.
5. Request a natural-language change without losing the previous stable version.
6. Refresh or restart the backend and recover the project from SQLite.

The historical formal 10-prompt release gate ran against
`ccb172608ebab7eb05294ad9c178d556fb8a795c` and completed 10/10 fixed prompts
successfully. Every run produced four ordered stage artifacts, `file_written`,
`preview_ready`, `run_completed`, and a matching stable version. The longest run
took 141.9 seconds, so allow roughly 1.5–2.5 minutes for a full generation.
This SHA is a test baseline, not the current deployment. The frontend is
deployed from `c5d56cff9dc5139c09d2b1772e86f32c6051bb74`; the backend remains
deployed from `1353d82424b304c6fc2cd55d6c4336ee9e718e36`.

## Architecture

```text
Browser
  ├─ React workbench ── REST + persisted SSE ── Go monolith
  └─ sandboxed Sandpack preview                    ├─ single agent loop
                                                    └─ SQLite volume
```

- **Frontend:** React, TypeScript, Vite, and Tailwind. The workbench contains the
  home page, Build Pulse, preview, file editor, and version history.
- **Agent loop:** one conversation progresses strictly through PM → Architect →
  Engineer → QA. Each successful stage has a queryable artifact. QA performs the
  compile gate and can send a failure back to Engineer for at most two repairs.
- **Preview:** Sandpack is the only MVP preview path. A candidate version is
  compiled in a sandboxed iframe; the last stable preview stays mounted until
  `preview_ready`.
- **Persistence:** SQLite in a named Docker volume is the only source of truth.
  Projects, messages, runs, attempts, events, artifacts, files, and versions are
  server-persisted. There is no IndexedDB persistence.
- **Contract:** `contracts/contract.json` defines states, REST paths, SSE events,
  idempotency, limits, and stable errors. Go embeds it and the frontend imports
  it directly.

Deliberate MVP choices:

- One agent loop keeps context and recovery behavior observable; this is not four
  independently scheduled agents.
- Only generated `/src/App.tsx` is writable. The React/Tailwind scaffold is
  fixed, with no shell access, path traversal, or runtime package installation.
- Sandpack avoids per-project preview containers, and SQLite keeps local-first
  recovery simple. There is no Traefik or per-project Vite/Docker runtime.

## Cold start on OrbStack

### Prerequisites

- macOS with [OrbStack](https://orbstack.dev/) running, or Docker Engine with
  Compose v2
- Git
- Model credentials for one of the modes below

The container build supplies Go and Node; host installations are needed only for
running the developer test commands.

```bash
git clone https://github.com/OrcaxNet/vibe-forge.git
cd vibe-forge
cp .env.example .env
```

Edit `.env` and configure exactly one model authentication mode:

| Mode | Required values | Use when |
| --- | --- | --- |
| Direct Anthropic | `ANTHROPIC_API_KEY` | Calling the Anthropic API directly |
| Compatible gateway | `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, and the gateway's `ANTHROPIC_MODEL` | Calling a bearer-token Anthropic-compatible gateway |

Never commit `.env`; it is ignored by Git. Start the stack:

```bash
docker compose up --build -d
docker compose ps
curl -i http://127.0.0.1:8787/api/health
```

Open <http://localhost:5173>. A ready installation returns HTTP 200 with
`database.status: "ok"` and `model.status: "configured"`.

If credentials are absent or incomplete, the backend still starts so that its
health and persistence surfaces can be inspected, but `/api/health` returns 503
with `model.status: "not_configured"` and new agent runs are rejected. Fix
`.env`, then apply it with:

```bash
docker compose up -d --force-recreate backend
```

The first build downloads the Go, Node, Alpine, and nginx base images. Subsequent
builds reuse their caches. Host ports default to `5173` and `8787`; change
`FRONTEND_PORT` or `BACKEND_PORT` in `.env` if they are occupied.

### Core smoke

1. Confirm `/api/health` is HTTP 200 and both dependencies are ready.
2. Open the home page and submit: `Build a habit tracker where I can add, check,
   and delete habits.`
3. Wait for all four Build Pulse stages and a usable preview.
4. Add, complete, and delete one habit in the generated app.
5. Request: `Change the primary color to indigo without removing any features.`
6. Confirm a second stable version appears, then refresh the page and verify the
   preview and version history recover.

Useful diagnostics:

```bash
docker compose logs --tail=100 backend
docker compose restart backend
curl -fsS http://127.0.0.1:8787/api/health
```

`docker compose restart` and `docker compose down` followed by
`docker compose up -d` preserve the SQLite volume. A run interrupted by a
restart becomes `interrupted`, releases its active lock, and can be retried as a
new attempt.

> **Data deletion warning:** `docker compose down -v` deletes the
> `vibe-forge-db` volume and all projects, messages, files, runs, and versions.
> Use `docker compose down` without `-v` to stop the stack and keep data.

## Environment variables

All local values belong in `.env`; `.env.example` contains safe placeholders.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `ANTHROPIC_API_KEY` | One auth mode | empty | Direct API authentication |
| `ANTHROPIC_AUTH_TOKEN` | Gateway mode | empty | Gateway bearer token |
| `ANTHROPIC_BASE_URL` | Gateway mode | empty | Anthropic-compatible gateway base URL |
| `ANTHROPIC_MODEL` | No | `claude-sonnet-5` | Model identifier expected by the selected provider |
| `BACKEND_PORT` | No | `8787` | Backend host port |
| `FRONTEND_PORT` | No | `5173` | Frontend host port |
| `BUILD_REVISION` | No | `local` | Git SHA embedded in the frontend build manifest |
| `DATABASE_PATH` | No | `/data/vibe-forge.db` | SQLite path inside the backend container |

Credentials are injected only into the backend container. The frontend bundle,
generated app, API errors, and lifecycle logs must never contain a key, token,
or private upstream URL.

## Developer verification

Backend (Go 1.26):

```bash
go test -race ./...
go vet ./...
```

Frontend (Node 22 and a local Chrome/Chromium for the browser smoke):

```bash
cd frontend
npm ci
npm run smoke
npm run test:e2e
npm run test:preview-gate # 20 isolated cold-start contexts; >=95% attempt-0
```

Repository and Compose checks:

```bash
docker compose config --quiet
git status --short
```

The committed reproducibility files are `go.sum` and
`frontend/package-lock.json`. Production containers use multi-stage builds; no
runtime `npm install` occurs.

### Deployment traceability

The PR #20 release rebuilt only the frontend. The backend was not rebuilt and
continues to run the preceding deployed revision:

| Component | Deployed Git revision |
| --- | --- |
| Frontend | [`c5d56cff9dc5139c09d2b1772e86f32c6051bb74`](https://github.com/OrcaxNet/vibe-forge/commit/c5d56cff9dc5139c09d2b1772e86f32c6051bb74) |
| Backend | [`1353d82424b304c6fc2cd55d6c4336ee9e718e36`](https://github.com/OrcaxNet/vibe-forge/commit/1353d82424b304c6fc2cd55d6c4336ee9e718e36) |

The public [`/build-info.json`](https://vf.floatflow.com/build-info.json) is the
authoritative frontend artifact check. It contains the injected Git revision, a
SHA-256 digest over the runtime assets, and the individual asset digests.
Compose readiness checks the manifest revision instead of accepting any
responsive nginx process. The backend revision is confirmed by the coordinated
FLO-68 deployment record and its post-deployment acceptance; `/api/health`
verifies runtime health but does not itself expose a Git SHA.

Use the release wrapper for a production frontend update:

```bash
./scripts/deploy-frontend.sh
```

It first rejects a dirty worktree, then injects the checked-out full Git SHA,
rebuilds and restarts only the frontend, and repeatedly fetches the public
manifest and every listed asset until their revision and SHA-256 digests match
or the deployment fails. Set `PUBLIC_URL` to verify another origin. A read-only
verification can also be run separately:

```bash
cd frontend
npm run verify:deployment -- https://vf.floatflow.com "$(git rev-parse HEAD)"
```

## Repository layout

```text
contracts/             shared contract imported by both applications
cmd/server/            Go entrypoint, startup reconciliation, redacted logging
internal/api/          REST, SSE replay, retry, and compile-result handlers
internal/agent/        PM → Architect → Engineer → QA loop and bounded repair
internal/db/           SQLite WAL/foreign-key setup and embedded migrations
internal/store/        projects, runs, events, artifacts, files, and versions
frontend/              React/TypeScript workbench and browser smoke
scaffold/              fixed files supplied to generated Sandpack apps
compose.yaml           local-first frontend, backend, and persistent volume
```

## Delivery status

Completed MVP:

- Public repository, MIT license, pinned frontend dependency lockfile, and Go
  module checksums
- Home page, responsive project workbench, input validation, and server-driven
  Build Pulse
- Single four-stage agent loop, persisted ordered SSE with `Last-Event-ID`
  replay, bounded compile repair, and sanitized failures
- Sandpack stable-preview switching, file inspection/manual editing, version
  history and restore
- SQLite migrations, idempotency, project isolation, restart reconciliation,
  and persistent OrbStack volume
- Anonymous HTTPS demo at a fixed hostname and formal 10-prompt release gate
  with 0 open P0 defects

Explicitly outside the MVP:

- Login, per-user workspaces, quotas, billing, and production-grade rate limits
- A highly available, multi-host production deployment or managed SLA
- Multi-file generation, arbitrary dependencies, shell access, and per-project
  containers
- Independent multi-agent scheduling and collaboration

## Known limitations and next steps

1. **Single-host availability (P1).** The fixed `vf.floatflow.com` hostname is
   routed through a Cloudflare named tunnel, but the application, tunnel, and
   SQLite volume still run on one local OrbStack host. Host restarts, local
   network outages, or tunnel downtime can temporarily make the demo
   unavailable. Refreshing or reconnecting after recovery resumes the persisted
   SSE stream and does not stop an otherwise-running backend generation. The
   availability follow-up should move the stack and data to a managed or
   redundant environment, then add authentication and request limits before
   broader sharing.
2. **Sandpack remains a remote runtime dependency.** Stable previews load the
   CodeSandbox bundler from the public network. Vibe Forge now makes the
   optional Tailwind Play CDN non-blocking, records client/iframe readiness
   timings in `[preview-runtime]` diagnostics, and automatically retries one
   cold-start timeout before showing an actionable error. A prolonged Sandpack
   or network outage can still leave preview unavailable; project data and the
   last stable version remain intact, and **仅重试预览** retries without
   regenerating the app.
3. **Anonymous access.** Anyone with the demo URL can create projects and spend
   shared model quota. The self-hosted demo should be stopped when unattended:

   ```bash
   docker stop vibe-forge-named vibe-forge-frontend vibe-forge-backend
   ```

## Submission

| Deliverable | Location |
| --- | --- |
| Public demo | <https://vf.floatflow.com> |
| Public source | <https://github.com/OrcaxNet/vibe-forge> |
| Deployed frontend revision | [`c5d56cff9dc5139c09d2b1772e86f32c6051bb74`](https://github.com/OrcaxNet/vibe-forge/commit/c5d56cff9dc5139c09d2b1772e86f32c6051bb74) |
| Deployed backend revision | [`1353d82424b304c6fc2cd55d6c4336ee9e718e36`](https://github.com/OrcaxNet/vibe-forge/commit/1353d82424b304c6fc2cd55d6c4336ee9e718e36) |
| Setup, architecture, smoke, status, and limitations | This README |

The project is released under the [MIT License](./LICENSE). Direct dependency
versions and integrity hashes are declared in `frontend/package-lock.json`,
`go.mod`, and `go.sum`.
