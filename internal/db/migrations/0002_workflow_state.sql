-- FLO-74: durable workflow/stage state snapshots.
--
-- project_workflow_states is the monotonic, project-level pointer to the latest
-- valid workflow snapshot. workflow_stage_runs keeps one immutable-attempt
-- snapshot per required stage, so retries do not erase the previous attempt's
-- evidence. workflow_state_conflicts is the observable audit trail for
-- completed/preview/stage inconsistencies discovered during recovery.

CREATE TABLE IF NOT EXISTS project_workflow_states (
    project_id       TEXT PRIMARY KEY,
    workflow_run_id  TEXT,
    status           TEXT NOT NULL
                         CHECK (status IN ('draft', 'running', 'completed', 'failed', 'cancelled', 'recovering')),
    state_version    INTEGER NOT NULL CHECK (state_version > 0),
    updated_at       TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_run_id) REFERENCES runs(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS workflow_stage_runs (
    workflow_run_id  TEXT NOT NULL,
    attempt          INTEGER NOT NULL CHECK (attempt >= 0),
    attempt_id       TEXT,
    stage_key        TEXT NOT NULL
                         CHECK (stage_key IN ('pm', 'architect', 'engineer', 'qa')),
    status           TEXT NOT NULL
                         CHECK (status IN ('waiting', 'running', 'succeeded', 'failed', 'cancelled', 'recovering')),
    started_at       TEXT,
    finished_at      TEXT,
    updated_at       TEXT NOT NULL,
    error_code       TEXT,
    PRIMARY KEY (workflow_run_id, attempt, stage_key),
    FOREIGN KEY (workflow_run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id) REFERENCES attempts(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS workflow_state_conflicts (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL,
    workflow_run_id  TEXT,
    conflict_code    TEXT NOT NULL,
    details          TEXT NOT NULL,
    first_detected_at TEXT NOT NULL,
    resolved_at      TEXT,
    UNIQUE (project_id, workflow_run_id, conflict_code),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_run_id) REFERENCES runs(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_stage_runs_latest
    ON workflow_stage_runs(workflow_run_id, attempt DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_conflicts_open
    ON workflow_state_conflicts(project_id, resolved_at);
