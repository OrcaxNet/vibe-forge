# Vibe Forge Shared Contract

This directory holds the **single source of truth** for the Vibe Forge API
contract: [`contract.json`](./contract.json). It is frozen from the §5
(接口、事件与数据约定) sections of **PRD-A v1.0**, **PRD-B v1.0** and
**PRD-C v1.0**.

Both sides of the system consume **the same file**:

- **Backend (Go)** — `contracts/contract.go` embeds `contract.json` with
  `//go:embed` and exposes typed accessors (`contracts.Load()`,
  `contracts.HTTPStatusFor(code)`, …). The embedded bytes are asserted on by
  `contracts/contract_test.go`.
- **Frontend (React/TS)** — `frontend/src/contract.ts` imports
  `contract.json` directly and re-exports typed constants.

Because neither side hand-copies the values, the frontend and backend cannot
drift. Any contract change happens here, once, and is picked up by both.

## What the contract pins

| Section | Covers | PRD source |
| --- | --- | --- |
| `states` | `Project` / `Run` / `Version` / stage-node and durable workflow enums | A §5.3, B §5.3, FLO-72 §5 |
| `stages` | `pm → architect → engineer → qa` order | A §1.3, A §3.1 |
| `events` | 8 unified SSE event names + payloads + `Last-Event-ID` replay | A/B/C §5.2 |
| `paths` | 13 REST endpoints (incl. `GET /api/health`) | A/B/C §5.1 |
| `idempotency` | `Idempotency-Key` header, 30s TTL, scope | A §5.1, A §5.3 |
| `errors` | stable `code` / `message` / `retryable` structure + HTTP map | A §5.3, C §5.1 |
| `limits` | prompt 1–4000, file 200 KB, only `/src/App.tsx` writable | A-FR-03, B §5.3 |
| `concurrency` | single active run, manual/agent mutex, optimistic lock | A-FR-04, B §5.3 |
| `storage` | SQLite + WAL + foreign_keys, UTC, UUID, migration rules | C §5.3 |
| `models` | entity field shapes (Project, Run, Version, …) | A/B/C §5 |

FLO-128 adds a server-only access gate to the shared contract:
`POST /api/auth/login`, `GET /api/auth/session`, and
`POST /api/auth/logout`. Every other API path except `GET /api/health` is
protected by default. Authentication errors use the nested
`{ "error": { "code", "message", "retryAfterSeconds" } }` shape required by
the access-gate PRD.

`GET /api/projects/:id` additionally returns the FLO-72 durable workflow
snapshot: `workflowStatus`, `workflowRunId`, monotonic `stateVersion`,
`stateUpdatedAt`, `responseUpdatedAt`, attempt-aware `stages`, stable
`preview` provenance, and explicit `consistency` conflicts.

## Editing the contract

This is a frozen baseline for Stage 1. Changes require updating the relevant
PRD §5 section first, then this file, then running both smoke suites
(`go test ./contracts/...` and `npm --prefix frontend run build`). Stage 2+
tasks build on top of this contract; they must not redefine these names.
