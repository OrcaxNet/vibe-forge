-- 0001_init.sql — Vibe Forge initial schema skeleton.
-- Mirrors the entity shapes in contracts/contract.json (§5 models) and the
-- PRD-C data model. This is a foundation for Stage 2 (FLO-55), which owns the
-- final transaction boundaries, indexes and tests.
--
-- Statements are idempotent (IF NOT EXISTS) so the runner is safe to re-apply.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version     TEXT PRIMARY KEY,
    applied_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
    id                TEXT PRIMARY KEY,
    title             TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'active', 'archived')),
    stable_version_id TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    FOREIGN KEY (stable_version_id) REFERENCES versions(id)
);

CREATE TABLE IF NOT EXISTS messages (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS runs (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'queued'
                          CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'interrupted')),
    prompt            TEXT NOT NULL,
    base_version_id   TEXT,
    active_attempt_id TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS attempts (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL,
    sequence      INTEGER NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'interrupted')),
    auto_fix_round INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS stage_artifacts (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL,
    attempt_id    TEXT NOT NULL,
    stage         TEXT NOT NULL CHECK (stage IN ('pm', 'architect', 'engineer', 'qa')),
    artifact_type TEXT NOT NULL,
    artifact_ref  TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id) REFERENCES attempts(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS iterations (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    run_id            TEXT,
    kind              TEXT NOT NULL CHECK (kind IN ('agent', 'manual', 'restore')),
    base_version_id   TEXT,
    result_version_id TEXT,
    prompt            TEXT,
    created_at        TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS versions (
    id           TEXT PRIMARY KEY,
    project_id   TEXT NOT NULL,
    iteration_id TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'validating', 'stable', 'failed')),
    files_hash   TEXT,
    created_at   TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (iteration_id) REFERENCES iterations(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS files (
    id         TEXT PRIMARY KEY,
    version_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    content    TEXT NOT NULL,
    readonly   INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (version_id) REFERENCES versions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS events (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    type       TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_runs_project        ON runs(project_id);
CREATE INDEX IF NOT EXISTS idx_runs_status         ON runs(status);
CREATE INDEX IF NOT EXISTS idx_events_run_seq      ON events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_versions_project    ON versions(project_id);
CREATE INDEX IF NOT EXISTS idx_versions_status     ON versions(status);
CREATE INDEX IF NOT EXISTS idx_iterations_project  ON iterations(project_id);

-- Idempotency-key ledger (see contract §idempotency). FLO-55 owns the GC/TTL.
CREATE TABLE IF NOT EXISTS idempotency_records (
    key            TEXT NOT NULL,
    endpoint       TEXT NOT NULL,
    response_hash  TEXT,
    created_at     TEXT NOT NULL,
    PRIMARY KEY (key, endpoint)
);
