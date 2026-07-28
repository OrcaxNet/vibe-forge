package store

import (
	"context"
	"database/sql"
	"fmt"
)

// draft.go owns the agent-loop version lifecycle (FLO-60). The agent loop does
// NOT use CommitVersion directly: the Engineer first writes a *draft* version
// (file_written, visible to Sandpack but NOT the stable preview), QA validates
// it, and only on a QA pass does the draft atomically promote to stable
// (preview_ready). A failed draft is recorded for history but never becomes
// stable - stableVersionId is touched only inside the promote transaction
// (contract §concurrency.stableVersionAtomic, B-FR-07).
//
// All promote/fail transitions reuse the already-written draft version row
// (status draft -> stable|failed) so file_written.versionDraftId,
// preview_ready.versionId and the SQLite version row are one and the same id.

// CreateDraftVersion writes a draft version snapshot for an iteration and
// returns it. The version is status='draft': it is the file map Sandpack
// compiles during the run (B-FR-02/04) but it is NOT the stable preview until
// PromoteDraftVersion commits it. Used by the Engineer stage (FLO-60).
func (s *Store) CreateDraftVersion(ctx context.Context, projectID, iterationID string, files []FileSnapshot) (Version, error) {
	var result Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM iterations WHERE id = ?`, iterationID).Scan(&exists); err != nil {
			if isNoRows(err) {
				return notFound("iteration not found")
			}
			return fmt.Errorf("check iteration: %w", err)
		}
		hash := filesHash(files)
		now := s.nowTS()
		v := Version{
			ID: newID(), ProjectID: projectID, IterationID: iterationID,
			Status: "draft", FilesHash: &hash, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO versions(id, project_id, iteration_id, status, files_hash, created_at)
			 VALUES(?, ?, ?, 'draft', ?, ?)`,
			v.ID, v.ProjectID, v.IterationID, sql.NullString{String: hash, Valid: true}, now); err != nil {
			return fmt.Errorf("insert draft version: %w", err)
		}
		for _, f := range files {
			ro := 1
			if !f.Readonly {
				ro = 0
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO files(id, version_id, path, content, readonly) VALUES(?, ?, ?, ?, ?)`,
				newID(), v.ID, f.Path, f.Content, ro); err != nil {
				return fmt.Errorf("insert draft file %q: %w", f.Path, err)
			}
		}
		result = v
		return nil
	})
	return result, err
}

// PromoteDraftVersion atomically promotes a draft version to stable: in one
// transaction it flips the version status to 'stable', records the iteration
// result, and CAS-switches the project's stableVersionId from the iteration's
// base to this version (contract §concurrency.stableVersionAtomic). Any step
// failing rolls back so stableVersionId is never left pointing at a
// non-stable version. On success the run and its attempt are marked succeeded.
//
// This is the ONLY place preview_ready's versionId becomes the stable version,
// so preview_ready.versionId == project.stableVersionId holds (B-FR-07).
func (s *Store) PromoteDraftVersion(ctx context.Context, draftVersionID, runID, attemptID string) (Version, error) {
	var result Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var v Version
		var hash sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT id, project_id, iteration_id, status, files_hash, created_at
			   FROM versions WHERE id = ?`, draftVersionID).
			Scan(&v.ID, &v.ProjectID, &v.IterationID, &v.Status, &hash, &v.CreatedAt)
		if err != nil {
			if isNoRows(err) {
				return notFound("draft version not found")
			}
			return fmt.Errorf("load draft version: %w", err)
		}
		if v.Status != "draft" {
			// Idempotent: a draft already promoted/failed replays its state.
			result = v
			return nil
		}
		// Load the iteration to get the base version for the CAS.
		var resultVID sql.NullString
		var baseVID sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT result_version_id, base_version_id FROM iterations WHERE id = ?`, v.IterationID).
			Scan(&resultVID, &baseVID); err != nil {
			if isNoRows(err) {
				return notFound("iteration not found")
			}
			return fmt.Errorf("load iteration: %w", err)
		}

		now := s.nowTS()
		if _, err := tx.ExecContext(ctx,
			`UPDATE versions SET status = 'stable' WHERE id = ?`, v.ID); err != nil {
			return fmt.Errorf("promote version: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE iterations SET result_version_id = ? WHERE id = ?`, v.ID, v.IterationID); err != nil {
			return fmt.Errorf("set iteration result: %w", err)
		}
		// Atomic stable switch: CAS on the base version so a concurrent change
		// cannot silently overwrite stableVersionId. NULL base (first version)
		// matches via COALESCE.
		res, err := tx.ExecContext(ctx,
			`UPDATE projects SET stable_version_id = ?, updated_at = ?
			  WHERE id = ? AND COALESCE(stable_version_id, '') = COALESCE(?, '')`,
			v.ID, now, v.ProjectID, nullStrFromNull(baseVID))
		if err != nil {
			return fmt.Errorf("cas stable version: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("baseVersionId no longer matches stable version", nil)
		}
		if runID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET status = 'succeeded', active_attempt_id = NULL, updated_at = ? WHERE id = ?`,
				now, runID); err != nil {
				return fmt.Errorf("mark run succeeded: %w", err)
			}
		}
		if attemptID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE attempts SET status = 'succeeded' WHERE id = ?`, attemptID); err != nil {
				return fmt.Errorf("mark attempt succeeded: %w", err)
			}
		}
		if runID != "" {
			if err := bumpWorkflowForRunTx(ctx, tx, runID, "completed", now); err != nil {
				return err
			}
		}
		v.Status = "stable"
		result = v
		return nil
	})
	return result, err
}

// FailDraftVersion marks a draft version failed and records the iteration
// result, WITHOUT touching stableVersionId (C-FR-04 / B-FR-06). The run and
// attempt are marked failed. Used when QA exhausts auto-repair rounds.
func (s *Store) FailDraftVersion(ctx context.Context, draftVersionID, runID, attemptID string) (Version, error) {
	var result Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var v Version
		var hash sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT id, project_id, iteration_id, status, files_hash, created_at
			   FROM versions WHERE id = ?`, draftVersionID).
			Scan(&v.ID, &v.ProjectID, &v.IterationID, &v.Status, &hash, &v.CreatedAt)
		if err != nil {
			if isNoRows(err) {
				return notFound("draft version not found")
			}
			return fmt.Errorf("load draft version: %w", err)
		}
		if v.Status != "draft" {
			result = v
			return nil
		}
		now := s.nowTS()
		if _, err := tx.ExecContext(ctx,
			`UPDATE versions SET status = 'failed' WHERE id = ?`, v.ID); err != nil {
			return fmt.Errorf("fail version: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE iterations SET result_version_id = ? WHERE id = ?`, v.ID, v.IterationID); err != nil {
			return fmt.Errorf("set iteration result: %w", err)
		}
		// Intentionally do NOT update projects.stable_version_id (C-FR-04).
		if runID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET status = 'failed', active_attempt_id = NULL, updated_at = ? WHERE id = ?`,
				now, runID); err != nil {
				return fmt.Errorf("mark run failed: %w", err)
			}
		}
		if attemptID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE attempts SET status = 'failed' WHERE id = ?`, attemptID); err != nil {
				return fmt.Errorf("mark attempt failed: %w", err)
			}
		}
		if runID != "" {
			if err := bumpWorkflowForRunTx(ctx, tx, runID, "failed", now); err != nil {
				return err
			}
		}
		v.Status = "failed"
		result = v
		return nil
	})
	return result, err
}

// BeginAttempt flips a run's active attempt from 'queued' to 'running' and the
// run to 'running' (FLO-60). CreateRun leaves the first attempt 'queued'; the
// agent loop calls this when it starts driving the attempt. It is a no-op for an
// attempt already running (retry path). It does NOT create a new attempt.
func (s *Store) BeginAttempt(ctx context.Context, runID, attemptID string) error {
	now := s.nowTS()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE attempts SET status = 'running' WHERE id = ? AND status = 'queued'`,
			attemptID); err != nil {
			return fmt.Errorf("begin attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = 'running', updated_at = ? WHERE id = ?`,
			now, runID); err != nil {
			return fmt.Errorf("begin run: %w", err)
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "running", now)
	})
}

// FailRun is the generic run-failure path used when a run fails before any draft
// version exists (e.g. a PM-stage upstream error) or independent of a version
// (TIMEOUT / 429 / 5xx). It marks the run 'failed', clears active_attempt_id,
// and marks the active attempt 'failed' - WITHOUT touching stableVersionId
// (C-FR-04). Use FailDraftVersion when a draft version should be failed too.
func (s *Store) FailRun(ctx context.Context, runID string) error {
	now := s.nowTS()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE attempts SET status = 'failed'
			  WHERE run_id = ? AND status IN ('running','queued')`, runID); err != nil {
			return fmt.Errorf("fail active attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = 'failed', active_attempt_id = NULL, updated_at = ?
			  WHERE id = ?`, now, runID); err != nil {
			return fmt.Errorf("fail run: %w", err)
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "failed", now)
	})
}

// NewAttempt starts a fresh attempt for a run (auto-repair round or retry). It
// appends an attempt with sequence = max+1 and the given auto_fix_round, sets it
// as the run's active attempt, and flips the run to running. The caller drives
// the agent loop for the new attempt (FLO-60).
func (s *Store) NewAttempt(ctx context.Context, runID string, autoFixRound int) (Attempt, error) {
	var a Attempt
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var runExists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, runID).Scan(&runExists); err != nil {
			if isNoRows(err) {
				return notFound("run not found")
			}
			return fmt.Errorf("check run: %w", err)
		}
		var maxSeq int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM attempts WHERE run_id = ?`, runID).Scan(&maxSeq); err != nil {
			return fmt.Errorf("next attempt seq: %w", err)
		}
		now := s.nowTS()
		a = Attempt{
			ID: newID(), RunID: runID, Sequence: maxSeq + 1,
			Status: "running", AutoFixRound: autoFixRound, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attempts(id, run_id, sequence, status, auto_fix_round, created_at)
			 VALUES(?, ?, ?, 'running', ?, ?)`,
			a.ID, a.RunID, a.Sequence, a.AutoFixRound, a.CreatedAt); err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET active_attempt_id = ?, status = 'running', updated_at = ? WHERE id = ?`,
			a.ID, now, runID); err != nil {
			return fmt.Errorf("set active attempt: %w", err)
		}
		if err := initAttemptStagesTx(ctx, tx, runID, a.ID, a.Sequence, now); err != nil {
			return err
		}
		return bumpWorkflowForRunTx(ctx, tx, runID, "running", now)
	})
	return a, err
}

// GetRun loads a run by id (ErrNotFound if missing).
func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	var r Run
	var base, active sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, status, prompt, base_version_id, active_attempt_id, created_at, updated_at
		   FROM runs WHERE id = ?`, runID).
		Scan(&r.ID, &r.ProjectID, &r.Status, &r.Prompt, &base, &active, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return Run{}, notFound("run not found")
		}
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	if base.Valid {
		b := base.String
		r.BaseVersionID = &b
	}
	if active.Valid {
		a := active.String
		r.ActiveAttemptID = &a
	}
	return r, nil
}

// GetAttempt loads an attempt by id.
func (s *Store) GetAttempt(ctx context.Context, attemptID string) (Attempt, error) {
	var a Attempt
	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, sequence, status, auto_fix_round, created_at
		   FROM attempts WHERE id = ?`, attemptID).
		Scan(&a.ID, &a.RunID, &a.Sequence, &a.Status, &a.AutoFixRound, &a.CreatedAt)
	if err != nil {
		if isNoRows(err) {
			return Attempt{}, notFound("attempt not found")
		}
		return Attempt{}, fmt.Errorf("get attempt: %w", err)
	}
	return a, nil
}

// GetActiveAttempt returns the run's active attempt (ErrNotFound if none).
func (s *Store) GetActiveAttempt(ctx context.Context, runID string) (Attempt, error) {
	var attemptID string
	err := s.db.QueryRowContext(ctx,
		`SELECT active_attempt_id FROM runs WHERE id = ?`, runID).Scan(&attemptID)
	if err != nil {
		if isNoRows(err) {
			return Attempt{}, notFound("run not found")
		}
		return Attempt{}, fmt.Errorf("load run for active attempt: %w", err)
	}
	if attemptID == "" {
		return Attempt{}, notFound("run has no active attempt")
	}
	return s.GetAttempt(ctx, attemptID)
}

// GetAttemptBySequence returns a run's attempt with the given sequence number
// (1-based). ErrNotFound if the run/sequence does not exist. Used by the
// compile-result callback to address a specific attempt.
func (s *Store) GetAttemptBySequence(ctx context.Context, runID string, sequence int) (Attempt, error) {
	var a Attempt
	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, sequence, status, auto_fix_round, created_at
		   FROM attempts WHERE run_id = ? AND sequence = ?`, runID, sequence).
		Scan(&a.ID, &a.RunID, &a.Sequence, &a.Status, &a.AutoFixRound, &a.CreatedAt)
	if err != nil {
		if isNoRows(err) {
			return Attempt{}, notFound("attempt not found")
		}
		return Attempt{}, fmt.Errorf("get attempt by sequence: %w", err)
	}
	return a, nil
}

// RecordCompileResult idempotently records a QA compile-result artifact for an
// attempt (the Sandpack compile-result callback, FLO-60). If an identical
// (attempt, ref) artifact already exists it is returned without inserting, so
// repeated callbacks do not create duplicates. This does NOT change run state;
// the agent loop's own QA gate drives promotion/failure.
func (s *Store) RecordCompileResult(ctx context.Context, runID, attemptID, ref string) (StageArtifact, error) {
	var existing StageArtifact
	err := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, attempt_id, stage, artifact_type, artifact_ref, created_at
		   FROM stage_artifacts
		  WHERE run_id = ? AND attempt_id = ? AND stage = 'qa'
		    AND artifact_type = 'compile_result' AND artifact_ref = ?
		  LIMIT 1`, runID, attemptID, ref).
		Scan(&existing.ID, &existing.RunID, &existing.AttemptID, &existing.Stage, &existing.ArtifactType, &existing.ArtifactRef, &existing.CreatedAt)
	if err == nil {
		return existing, nil
	}
	if !isNoRows(err) {
		return StageArtifact{}, fmt.Errorf("dedup compile result: %w", err)
	}
	return s.RecordStageArtifact(ctx, runID, attemptID, "qa", "compile_result", ref)
}

// GetRunIteration returns the agent iteration for a run (the iteration created
// in CreateRun). ErrNotFound if the run has none.
func (s *Store) GetRunIteration(ctx context.Context, runID string) (Iteration, error) {
	var it Iteration
	var runID_ sql.NullString
	var base, result sql.NullString
	var prompt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, run_id, kind, base_version_id, result_version_id, prompt, created_at
		   FROM iterations WHERE run_id = ? ORDER BY created_at ASC LIMIT 1`, runID).
		Scan(&it.ID, &it.ProjectID, &runID_, &it.Kind, &base, &result, &prompt, &it.CreatedAt)
	if err != nil {
		if isNoRows(err) {
			return Iteration{}, notFound("iteration not found for run")
		}
		return Iteration{}, fmt.Errorf("get run iteration: %w", err)
	}
	if runID_.Valid {
		r := runID_.String
		it.RunID = &r
	}
	if base.Valid {
		b := base.String
		it.BaseVersionID = &b
	}
	if result.Valid {
		v := result.String
		it.ResultVersionID = &v
	}
	if prompt.Valid {
		p := prompt.String
		it.Prompt = &p
	}
	return it, nil
}

// ListStageArtifacts returns the stage artifacts for a run in creation order,
// so a client can rebuild the Build Pulse and resolve each node's artifactRef
// (A-FR-05/06: every succeeded node has a queryable artifactRef).
func (s *Store) ListStageArtifacts(ctx context.Context, runID string) ([]StageArtifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, attempt_id, stage, artifact_type, artifact_ref, created_at
		   FROM stage_artifacts WHERE run_id = ? ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list stage artifacts: %w", err)
	}
	defer rows.Close()
	var out []StageArtifact
	for rows.Next() {
		var a StageArtifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.AttemptID, &a.Stage, &a.ArtifactType, &a.ArtifactRef, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan stage artifact: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetProjectStable returns the project's current stable_version_id (nil if none).
func (s *Store) GetProjectStable(ctx context.Context, projectID string) (*string, error) {
	var stable sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable)
	if err != nil {
		if isNoRows(err) {
			return nil, notFound("project not found")
		}
		return nil, fmt.Errorf("get project stable: %w", err)
	}
	if !stable.Valid {
		return nil, nil
	}
	v := stable.String
	return &v, nil
}

// GetVersionFilesSnapshot loads a version's files as a snapshot (path/content/
// readonly). Used to seed the Engineer's context from the previous stable
// version on a second-edit run (B-FR-08).
func (s *Store) GetVersionFilesSnapshot(ctx context.Context, versionID string) ([]FileSnapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, content, readonly FROM files WHERE version_id = ? ORDER BY path ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("read version files: %w", err)
	}
	defer rows.Close()
	var out []FileSnapshot
	for rows.Next() {
		var f FileSnapshot
		var ro int
		if err := rows.Scan(&f.Path, &f.Content, &ro); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		f.Readonly = ro == 1
		out = append(out, f)
	}
	return out, rows.Err()
}
