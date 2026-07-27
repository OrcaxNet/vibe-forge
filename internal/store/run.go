package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// CreateRun starts a new generation run for a project. It is idempotent on the
// Idempotency-Key, enforces single-active-run (409 with activeRunId) and the
// baseVersionId optimistic lock (409). It creates the run, its first attempt,
// the agent iteration and the user message for the prompt. Returns the contract
// response body {"runId": ...} with status 202.
func (s *Store) CreateRun(ctx context.Context, projectID, prompt, baseVersionID, idempotencyKey string) (status int, body []byte, replayed bool, err error) {
	res, err := s.runIdempotent(ctx, "createRun", idempotencyKey, func(tx *sql.Tx) (int, []byte, error) {
		lim := limits()
		n := utf8.RuneCountInString(prompt)
		if n < lim.PromptMinChars || n > lim.PromptMaxChars {
			return 0, nil, validation("prompt length out of range", map[string]any{
				"min": lim.PromptMinChars, "max": lim.PromptMaxChars, "actual": n,
			})
		}
		var stable sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable)
		if err != nil {
			if isNoRows(err) {
				return 0, nil, notFound("project not found")
			}
			return 0, nil, fmt.Errorf("get project for run: %w", err)
		}
		// Optimistic lock: a provided baseVersionId must equal the current stable.
		if baseVersionID != "" && (!stable.Valid || stable.String != baseVersionID) {
			return 0, nil, conflict("baseVersionId does not match current stable version", map[string]any{
				"expected": stableString(stable), "provided": baseVersionID,
			})
		}
		// Single active run: at most one queued/running run per project.
		var activeID string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM runs WHERE project_id = ? AND status IN ('queued','running') LIMIT 1`,
			projectID).Scan(&activeID)
		switch {
		case err == nil:
			return 0, nil, conflict("project already has an active run", map[string]any{
				"activeRunId": activeID,
			})
		case !isNoRows(err):
			return 0, nil, fmt.Errorf("check active run: %w", err)
		}

		var base *string
		if stable.Valid {
			b := stable.String
			base = &b
		}
		now := s.nowTS()
		runID := newID()
		attemptID := newID()
		iterationID := newID()

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runs(id, project_id, status, prompt, base_version_id, active_attempt_id, created_at, updated_at)
			 VALUES(?, ?, 'queued', ?, ?, NULL, ?, ?)`,
			runID, projectID, prompt, nullStr(base), now, now); err != nil {
			return 0, nil, fmt.Errorf("insert run: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attempts(id, run_id, sequence, status, auto_fix_round, created_at)
			 VALUES(?, ?, 1, 'queued', 0, ?)`,
			attemptID, runID, now); err != nil {
			return 0, nil, fmt.Errorf("insert attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET active_attempt_id = ? WHERE id = ?`, attemptID, runID); err != nil {
			return 0, nil, fmt.Errorf("set active attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO iterations(id, project_id, run_id, kind, base_version_id, result_version_id, prompt, created_at)
			 VALUES(?, ?, ?, 'agent', ?, NULL, ?, ?)`,
			iterationID, projectID, runID, nullStr(base), prompt, now); err != nil {
			return 0, nil, fmt.Errorf("insert iteration: %w", err)
		}
		// User message for this run's prompt (one per run; first or edit).
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages(id, project_id, role, content, created_at)
			 VALUES(?, ?, 'user', ?, ?)`,
			newID(), projectID, prompt, now); err != nil {
			return 0, nil, fmt.Errorf("insert run message: %w", err)
		}

		out, err := json.Marshal(struct {
			RunID string `json:"runId"`
		}{RunID: runID})
		if err != nil {
			return 0, nil, fmt.Errorf("marshal run: %w", err)
		}
		return 202, out, nil
	})
	if err != nil {
		return 0, nil, false, err
	}
	return res.status, res.body, res.replayed, nil
}

func stableString(n sql.NullString) any {
	if !n.Valid {
		return nil
	}
	return n.String
}

// RetryRun starts a fresh attempt for a failed/interrupted run so the agent loop
// can re-drive it (FLO-60). It is idempotent on the Idempotency-Key. A run that
// is not failed/interrupted is not retryable (409). On success it creates a new
// attempt (auto_fix_round 0, status running), sets it active, flips the run to
// running, and returns 202 {"attemptId": ...}. The caller launches Loop.Run.
func (s *Store) RetryRun(ctx context.Context, runID, idempotencyKey string) (status int, body []byte, replayed bool, err error) {
	res, err := s.runIdempotent(ctx, "retryRun", idempotencyKey, func(tx *sql.Tx) (int, []byte, error) {
		var runStatus string
		err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).Scan(&runStatus)
		if err != nil {
			if isNoRows(err) {
				return 0, nil, notFound("run not found")
			}
			return 0, nil, fmt.Errorf("get run for retry: %w", err)
		}
		if runStatus != "failed" && runStatus != "interrupted" {
			return 0, nil, conflict("run is not retryable", map[string]any{
				"status": runStatus, "runId": runID,
			})
		}
		var maxSeq int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM attempts WHERE run_id = ?`, runID).Scan(&maxSeq); err != nil {
			return 0, nil, fmt.Errorf("next attempt seq: %w", err)
		}
		now := s.nowTS()
		attemptID := newID()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attempts(id, run_id, sequence, status, auto_fix_round, created_at)
			 VALUES(?, ?, ?, 'running', 0, ?)`,
			attemptID, runID, maxSeq+1, now); err != nil {
			return 0, nil, fmt.Errorf("insert retry attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET active_attempt_id = ?, status = 'running', updated_at = ? WHERE id = ?`,
			attemptID, now, runID); err != nil {
			return 0, nil, fmt.Errorf("activate retry attempt: %w", err)
		}
		out, err := json.Marshal(struct {
			AttemptID string `json:"attemptId"`
		}{AttemptID: attemptID})
		if err != nil {
			return 0, nil, fmt.Errorf("marshal retry: %w", err)
		}
		return 202, out, nil
	})
	if err != nil {
		return 0, nil, false, err
	}
	return res.status, res.body, res.replayed, nil
}

// nullStr converts a *string to a sql.NullString (NULL when nil).
func nullStr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

// GetActiveRun returns the project's active (queued/running) run id, or "" if none.
func (s *Store) GetActiveRun(ctx context.Context, projectID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM runs WHERE project_id = ? AND status IN ('queued','running') LIMIT 1`,
		projectID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if isNoRows(err) {
		return "", nil
	}
	return "", fmt.Errorf("get active run: %w", err)
}

// AppendMessage records a chat message (typically an assistant turn from the
// agent loop). Used by FLO-60.
func (s *Store) AppendMessage(ctx context.Context, projectID, role, content string) (Message, error) {
	if role != "user" && role != "assistant" {
		return Message{}, validation("role must be user or assistant", nil)
	}
	m := Message{ID: newID(), ProjectID: projectID, Role: role, Content: content, CreatedAt: s.nowTS()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages(id, project_id, role, content, created_at) VALUES(?, ?, ?, ?, ?)`,
		m.ID, m.ProjectID, m.Role, m.Content, m.CreatedAt)
	if err != nil {
		return Message{}, fmt.Errorf("insert message: %w", err)
	}
	return m, nil
}

// RecordStageArtifact binds a real artifact to a Build Pulse node (FLO-60). A
// node is considered succeeded once its artifact exists.
func (s *Store) RecordStageArtifact(ctx context.Context, runID, attemptID, stage, artifactType, artifactRef string) (StageArtifact, error) {
	a := StageArtifact{
		ID: newID(), RunID: runID, AttemptID: attemptID, Stage: stage,
		ArtifactType: artifactType, ArtifactRef: artifactRef, CreatedAt: s.nowTS(),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO stage_artifacts(id, run_id, attempt_id, stage, artifact_type, artifact_ref, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.RunID, a.AttemptID, a.Stage, a.ArtifactType, a.ArtifactRef, a.CreatedAt)
	if err != nil {
		return StageArtifact{}, fmt.Errorf("insert stage artifact: %w", err)
	}
	return a, nil
}

// AppendEvent persists one SSE event with a monotonic per-run seq (FLO-60
// streams these and replays by Last-Event-ID).
func (s *Store) AppendEvent(ctx context.Context, runID, eventType string, payload map[string]any) (Event, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	e := Event{ID: newID(), RunID: runID, Type: eventType, Payload: payload, CreatedAt: s.nowTS()}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var seq int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
			return fmt.Errorf("next event seq: %w", err)
		}
		e.Seq = seq
		_, err := tx.ExecContext(ctx,
			`INSERT INTO events(id, run_id, seq, type, payload, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
			e.ID, e.RunID, e.Seq, e.Type, string(payloadBytes), e.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

// ListEvents returns events for a run with seq > afterSeq (0 = all), in seq
// order. FLO-60 uses this for Last-Event-ID replay.
func (s *Store) ListEvents(ctx context.Context, runID string, afterSeq int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, seq, type, payload, created_at
		   FROM events WHERE run_id = ? AND seq > ? ORDER BY seq ASC`, runID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.RunID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, fmt.Errorf("unmarshal event payload: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// SetRunStatus transitions a run (and optionally its active attempt) to a new
// status. Used by FLO-60 to drive queued->running->succeeded/failed.
func (s *Store) SetRunStatus(ctx context.Context, runID, status string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`,
		status, s.nowTS(), runID); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

// MarkActiveRunsInterrupted transitions every queued/running run to interrupted.
// Called on startup so a crashed run never stays "active" (C-FR-06/07, wired by
// FLO-59). Returns the count of runs reconciled.
func (s *Store) MarkActiveRunsInterrupted(ctx context.Context) (int, error) {
	now := s.nowTS()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'interrupted', updated_at = ? WHERE status IN ('queued','running')`, now)
	if err != nil {
		return 0, fmt.Errorf("reconcile interrupted runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
