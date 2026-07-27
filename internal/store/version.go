package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// errInjected is returned by commitVersionSteps at a requested fail point so
// atomicity tests can prove the whole transaction rolls back.
var errInjected = errors.New("injected failure")

type failPoint int

const (
	failNone failPoint = iota
	failAfterVersionInsert
	failAfterFilesInsert
	failAfterIterationUpdate
)

// CommitInput describes a successful version commit (C-FR-03).
type CommitInput struct {
	ProjectID   string
	IterationID string
	RunID       string // optional: mark run succeeded
	AttemptID   string // optional: mark attempt succeeded
	Files       []FileSnapshot
}

// CommitVersion performs the atomic success transaction (C-FR-03): in a single
// transaction it writes the file snapshot, the iteration result and switches
// stableVersionId to the new stable version. Any step failing rolls the whole
// transaction back so stableVersionId is never left pointing at a half-written
// version. Re-committing the same iteration is idempotent: it returns the
// already-committed version without creating a duplicate.
func (s *Store) CommitVersion(ctx context.Context, in CommitInput) (Version, error) {
	var result Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		v, err := s.commitVersionSteps(ctx, tx, in, failNone)
		if err != nil {
			return err
		}
		result = v
		return nil
	})
	return result, err
}

// commitVersionFailing runs commitVersionSteps with an injected fail point for
// atomicity tests. The injected error aborts the surrounding transaction.
func (s *Store) commitVersionFailing(ctx context.Context, in CommitInput, fp failPoint) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := s.commitVersionSteps(ctx, tx, in, fp)
		return err
	})
}

// commitVersionSteps implements the success transaction body against an open tx.
// fp injects a failure after a step (tests only); failNone is the production path.
func (s *Store) commitVersionSteps(ctx context.Context, tx *sql.Tx, in CommitInput, fp failPoint) (Version, error) {
	// Idempotency: an iteration that already produced a version replays it.
	var resultVID sql.NullString
	var baseVID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT result_version_id, base_version_id FROM iterations WHERE id = ?`,
		in.IterationID).Scan(&resultVID, &baseVID)
	if err != nil {
		if isNoRows(err) {
			return Version{}, notFound("iteration not found")
		}
		return Version{}, fmt.Errorf("load iteration: %w", err)
	}
	if resultVID.Valid {
		return s.fetchVersionTx(ctx, tx, resultVID.String)
	}
	// baseVID is the version this iteration was based on; the CAS below verifies
	// the project's stable_version_id still equals it before switching.

	// Project must exist (isolation + notFound before any write).
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, in.ProjectID).Scan(&exists); err != nil {
		if isNoRows(err) {
			return Version{}, notFound("project not found")
		}
		return Version{}, fmt.Errorf("check project: %w", err)
	}

	hash := filesHash(in.Files)
	now := s.nowTS()
	v := Version{
		ID:          newID(),
		ProjectID:   in.ProjectID,
		IterationID: in.IterationID,
		Status:      "stable",
		FilesHash:   &hash,
		CreatedAt:   now,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO versions(id, project_id, iteration_id, status, files_hash, created_at)
		 VALUES(?, ?, ?, 'stable', ?, ?)`,
		v.ID, v.ProjectID, v.IterationID, sql.NullString{String: hash, Valid: true}, now); err != nil {
		return Version{}, fmt.Errorf("insert version: %w", err)
	}
	if fp == failAfterVersionInsert {
		return Version{}, errInjected
	}

	for _, f := range in.Files {
		ro := 1
		if !f.Readonly {
			ro = 0
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO files(id, version_id, path, content, readonly) VALUES(?, ?, ?, ?, ?)`,
			newID(), v.ID, f.Path, f.Content, ro); err != nil {
			return Version{}, fmt.Errorf("insert file %q: %w", f.Path, err)
		}
	}
	if fp == failAfterFilesInsert {
		return Version{}, errInjected
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE iterations SET result_version_id = ? WHERE id = ?`, v.ID, in.IterationID); err != nil {
		return Version{}, fmt.Errorf("set iteration result: %w", err)
	}
	if fp == failAfterIterationUpdate {
		return Version{}, errInjected
	}

	// Atomic stable switch: CAS on the base version so a concurrent change cannot
	// silently overwrite stableVersionId (contract §concurrency.stableVersionAtomic
	// + optimisticLock). NULL base (first version) matches via COALESCE.
	res, err := tx.ExecContext(ctx,
		`UPDATE projects SET stable_version_id = ?, updated_at = ?
		  WHERE id = ? AND COALESCE(stable_version_id, '') = COALESCE(?, '')`,
		v.ID, now, in.ProjectID, nullStrFromNull(baseVID))
	if err != nil {
		return Version{}, fmt.Errorf("cas stable version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Version{}, conflict("baseVersionId no longer matches stable version", nil)
	}

	if in.RunID != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = 'succeeded', active_attempt_id = NULL, updated_at = ? WHERE id = ?`,
			now, in.RunID); err != nil {
			return Version{}, fmt.Errorf("mark run succeeded: %w", err)
		}
	}
	if in.AttemptID != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE attempts SET status = 'succeeded' WHERE id = ?`, in.AttemptID); err != nil {
			return Version{}, fmt.Errorf("mark attempt succeeded: %w", err)
		}
	}
	return v, nil
}

// FailInput describes a failed version (C-FR-04). A failed version is recorded
// for history but NEVER becomes the stable version.
type FailInput struct {
	ProjectID   string
	IterationID string
	RunID       string
	AttemptID   string
	Files       []FileSnapshot // optional snapshot for history
	Code        string         // sanitized run failure code (no keys/stacks)
}

// FailVersion records a failed version and marks the run/attempt failed, without
// touching stableVersionId (C-FR-04). Idempotent on the iteration.
func (s *Store) FailVersion(ctx context.Context, in FailInput) (Version, error) {
	var result Version
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var resultVID sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT result_version_id FROM iterations WHERE id = ?`, in.IterationID).Scan(&resultVID)
		if err != nil {
			if isNoRows(err) {
				return notFound("iteration not found")
			}
			return fmt.Errorf("load iteration: %w", err)
		}
		if resultVID.Valid {
			v, err := s.fetchVersionTx(ctx, tx, resultVID.String)
			if err != nil {
				return err
			}
			result = v
			return nil
		}
		var hash sql.NullString
		if len(in.Files) > 0 {
			h := filesHash(in.Files)
			hash = sql.NullString{String: h, Valid: true}
		}
		now := s.nowTS()
		v := Version{
			ID: newID(), ProjectID: in.ProjectID, IterationID: in.IterationID,
			Status: "failed", FilesHash: ptrIfValid(hash), CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO versions(id, project_id, iteration_id, status, files_hash, created_at)
			 VALUES(?, ?, ?, 'failed', ?, ?)`,
			v.ID, v.ProjectID, v.IterationID, hash, now); err != nil {
			return fmt.Errorf("insert failed version: %w", err)
		}
		for _, f := range in.Files {
			ro := 1
			if !f.Readonly {
				ro = 0
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO files(id, version_id, path, content, readonly) VALUES(?, ?, ?, ?, ?)`,
				newID(), v.ID, f.Path, f.Content, ro); err != nil {
				return fmt.Errorf("insert file %q: %w", f.Path, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE iterations SET result_version_id = ? WHERE id = ?`, v.ID, in.IterationID); err != nil {
			return fmt.Errorf("set iteration result: %w", err)
		}
		// Intentionally do NOT update projects.stable_version_id (C-FR-04).
		if in.RunID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET status = 'failed', active_attempt_id = NULL, updated_at = ? WHERE id = ?`,
				now, in.RunID); err != nil {
				return fmt.Errorf("mark run failed: %w", err)
			}
		}
		if in.AttemptID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE attempts SET status = 'failed' WHERE id = ?`, in.AttemptID); err != nil {
				return fmt.Errorf("mark attempt failed: %w", err)
			}
		}
		result = v
		return nil
	})
	return result, err
}

// RestoreVersion atomically switches stableVersionId back to an existing stable
// version and records a restore iteration (C-FR-07). Idempotent on the key. The
// version must be stable and belong to the project; no active run may be in
// flight (manual/agent mutex).
func (s *Store) RestoreVersion(ctx context.Context, projectID, versionID, idempotencyKey string) (status int, body []byte, replayed bool, err error) {
	res, err := s.runIdempotent(ctx, "restoreVersion", idempotencyKey, func(tx *sql.Tx) (int, []byte, error) {
		var status string
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM versions WHERE id = ? AND project_id = ?`, versionID, projectID).Scan(&status)
		if err != nil {
			if isNoRows(err) {
				return 0, nil, notFound("version not found")
			}
			return 0, nil, fmt.Errorf("load version: %w", err)
		}
		if status != "stable" {
			return 0, nil, conflict("only stable versions can be restored", nil)
		}
		// Manual/agent mutex: no active run.
		var activeID string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM runs WHERE project_id = ? AND status IN ('queued','running') LIMIT 1`,
			projectID).Scan(&activeID)
		switch {
		case err == nil:
			return 0, nil, conflict("project has an active run", map[string]any{"activeRunId": activeID})
		case !isNoRows(err):
			return 0, nil, fmt.Errorf("check active run: %w", err)
		}
		var stable sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable); err != nil {
			if isNoRows(err) {
				return 0, nil, notFound("project not found")
			}
			return 0, nil, fmt.Errorf("load project: %w", err)
		}
		now := s.nowTS()
		iterID := newID()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO iterations(id, project_id, run_id, kind, base_version_id, result_version_id, prompt, created_at)
			 VALUES(?, ?, NULL, 'restore', ?, ?, NULL, ?)`,
			iterID, projectID, stable, versionID, now); err != nil {
			return 0, nil, fmt.Errorf("insert restore iteration: %w", err)
		}
		// Atomic switch to the restored version (CAS on current stable).
		res, err := tx.ExecContext(ctx,
			`UPDATE projects SET stable_version_id = ?, updated_at = ?
			  WHERE id = ? AND COALESCE(stable_version_id, '') = COALESCE(?, '')`,
			versionID, now, projectID, stable)
		if err != nil {
			return 0, nil, fmt.Errorf("cas restore: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, nil, conflict("stable version changed during restore", nil)
		}
		iter := Iteration{
			ID: iterID, ProjectID: projectID, Kind: "restore",
			ResultVersionID: &versionID, CreatedAt: now,
		}
		if stable.Valid {
			b := stable.String
			iter.BaseVersionID = &b
		}
		out, err := json.Marshal(iter)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal iteration: %w", err)
		}
		return 202, out, nil
	})
	if err != nil {
		return 0, nil, false, err
	}
	return res.status, res.body, res.replayed, nil
}

// ListVersions returns the versions for a project (C-FR-02/04). No file content
// is returned by default (contract §paths.listVersions).
func (s *Store) ListVersions(ctx context.Context, projectID string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, iteration_id, status, files_hash, created_at
		   FROM versions WHERE project_id = ? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	var vs []Version
	for rows.Next() {
		var v Version
		var hash sql.NullString
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.IterationID, &v.Status, &hash, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		if hash.Valid {
			h := hash.String
			v.FilesHash = &h
		}
		vs = append(vs, v)
	}
	return vs, rows.Err()
}

// GetVersionFiles returns the Sandpack files map {path: content} for a version,
// scoped to the project (404 if the version is unknown or belongs elsewhere).
func (s *Store) GetVersionFiles(ctx context.Context, projectID, versionID string) (map[string]string, error) {
	var one int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM versions WHERE id = ? AND project_id = ?`, versionID, projectID).Scan(&one); err != nil {
		if isNoRows(err) {
			return nil, notFound("version not found")
		}
		return nil, fmt.Errorf("check version: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, content FROM files WHERE version_id = ? ORDER BY path ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("list version files: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		out[path] = content
	}
	return out, rows.Err()
}

// fetchVersionTx loads one version inside a transaction (used for idempotent replay).
func (s *Store) fetchVersionTx(ctx context.Context, tx *sql.Tx, versionID string) (Version, error) {
	var v Version
	var hash sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, project_id, iteration_id, status, files_hash, created_at
		   FROM versions WHERE id = ?`, versionID).
		Scan(&v.ID, &v.ProjectID, &v.IterationID, &v.Status, &hash, &v.CreatedAt)
	if err != nil {
		if isNoRows(err) {
			return Version{}, notFound("version not found")
		}
		return Version{}, fmt.Errorf("fetch version: %w", err)
	}
	if hash.Valid {
		h := hash.String
		v.FilesHash = &h
	}
	return v, nil
}

// filesHash is the deterministic SHA-256 of a version's files (path+content),
// sorted by path. Used to verify a re-read snapshot matches (C-FR-03).
func filesHash(files []FileSnapshot) string {
	sorted := make([]FileSnapshot, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func nullStrFromNull(n sql.NullString) sql.NullString { return n }

func ptrIfValid(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}
