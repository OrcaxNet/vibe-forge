-- 0001_init.sql - Vibe Forge schema (Stage 2 / FLO-55 final).
--
-- Single source of truth for the data model. Mirrors the entity shapes in
-- contracts/contract.json (§models) and PRD-C. This is the finalized schema the
-- migration runner applies on startup; PRD-C requires migration failure to stop
-- startup (no silent skip) and re-running the runner to be idempotent.
--
-- All statements are IF NOT EXISTS so a statement-level re-run is safe; the
-- runner additionally records each version in schema_migrations so a version is
-- applied at most once per database.
--
-- PRAGMAs (journal_mode=WAL, foreign_keys=ON) are set in the DSN by db.Open, not
-- here: they cannot be changed from inside the transaction the runner wraps each
-- migration in.

CREATE TABLE IF NOT EXISTS projects (
    id                TEXT PRIMARY KEY,
    title             TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'active'
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
    id             TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL,
    sequence        INTEGER NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued'
                         CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'interrupted')),
    auto_fix_round  INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
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
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
    FOREIGN KEY (result_version_id) REFERENCES versions(id)
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

-- Idempotency-key ledger (contract §idempotency). Stores the original 2xx
-- response so a repeated Idempotency-Key within ttlSeconds replays the exact
-- result with no duplicate side effects. GC'd by created_at (db.GCIdempotency).
CREATE TABLE IF NOT EXISTS idempotency_records (
    key           TEXT NOT NULL,
    endpoint      TEXT NOT NULL,
    status_code   INTEGER NOT NULL,
    response_body TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    PRIMARY KEY (key, endpoint)
);

-- Single active run per project (contract §concurrency.singleActiveRun). At most
-- one row per project may hold a queued/running status; a second insert fails
-- with a UNIQUE violation which the store maps to 409 CONFLICT.
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_one_active
    ON runs(project_id) WHERE status IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS idx_runs_project       ON runs(project_id);
CREATE INDEX IF NOT EXISTS idx_runs_status        ON runs(status);
CREATE INDEX IF NOT EXISTS idx_events_run_seq     ON events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_versions_project   ON versions(project_id);
CREATE INDEX IF NOT EXISTS idx_versions_status    ON versions(status);
CREATE INDEX IF NOT EXISTS idx_iterations_project ON iterations(project_id);
CREATE INDEX IF NOT EXISTS idx_messages_project   ON messages(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_files_version      ON files(version_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_version_path ON files(version_id, path);
CREATE INDEX IF NOT EXISTS idx_idempotency_created ON idempotency_records(created_at);
