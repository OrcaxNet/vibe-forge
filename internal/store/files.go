package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/OrcaxNet/vibe-forge/internal/compile"
)

// WriteFile performs a manual edit of the single writable file (/src/App.tsx),
// creating a manual iteration and running the SAME compile gate as the agent
// loop (FLO-77, contract §paths.writeFile + §concurrency.stableVersionAtomic).
//
// The new version is created as a 'draft' (validating) version first; only a
// compile pass promotes it to 'stable' and CAS-switches the project's
// stableVersionId. A compile fail marks the version 'failed' and leaves
// stableVersionId on the previous stable - so invalid hand-edited code can never
// become the server-authoritative stable version or replace the working preview.
//
// It enforces the manual/agent mutex (409 if an active run is in flight) and the
// baseVersionId optimistic lock. Idempotent on the key. On a compile pass it
// returns the manual iteration JSON with status 202; on a compile fail it
// returns 422 VALIDATION_ERROR carrying the structured compile errors (the
// failed version is still recorded for history).
func (s *Store) WriteFile(ctx context.Context, projectID, content, baseVersionID, idempotencyKey string) (status int, body []byte, replayed bool, err error) {
	res, err := s.runIdempotent(ctx, "writeFile", idempotencyKey, func(tx *sql.Tx) (int, []byte, error) {
		lim := limits()
		if len(content) > lim.FileContentMaxBytes {
			return 0, nil, validation("file content too large", map[string]any{
				"maxBytes": lim.FileContentMaxBytes, "actual": len(content),
			})
		}
		var stable sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable)
		if err != nil {
			if isNoRows(err) {
				return 0, nil, notFound("project not found")
			}
			return 0, nil, fmt.Errorf("get project: %w", err)
		}
		// Manual/agent mutex: no active run may be in flight.
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
		if !stable.Valid {
			return 0, nil, conflict("manual edit requires a stable version", nil)
		}
		// Optimistic lock.
		if baseVersionID != "" && stable.String != baseVersionID {
			return 0, nil, conflict("baseVersionId does not match stable version", map[string]any{
				"expected": stable.String, "provided": baseVersionID,
			})
		}
		// Build the new snapshot from the base version, overriding App.tsx.
		files, err := s.readVersionFilesTx(ctx, tx, stable.String)
		if err != nil {
			return 0, nil, err
		}
		writable := lim.WritableFilePath
		files = applyOverride(files, writable, content, false)

		// Compile gate (FLO-77): run the same structural compile check as the
		// agent loop on the hand-edited App.tsx BEFORE touching stableVersionId.
		compileResult := compile.Validate(content)

		now := s.nowTS()
		iterID := newID()
		base := stable.String
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO iterations(id, project_id, run_id, kind, base_version_id, result_version_id, prompt, created_at)
			 VALUES(?, ?, NULL, 'manual', ?, NULL, NULL, ?)`,
			iterID, projectID, base, now); err != nil {
			return 0, nil, fmt.Errorf("insert manual iteration: %w", err)
		}
		// Create the new version as a 'draft' (validating) version first
		// (acceptance 1: same lifecycle as the agent path).
		hash := filesHash(files)
		vID := newID()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO versions(id, project_id, iteration_id, status, files_hash, created_at)
			 VALUES(?, ?, ?, 'draft', ?, ?)`,
			vID, projectID, iterID, sql.NullString{String: hash, Valid: true}, now); err != nil {
			return 0, nil, fmt.Errorf("insert manual version: %w", err)
		}
		for _, f := range files {
			ro := 1
			if !f.Readonly {
				ro = 0
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO files(id, version_id, path, content, readonly) VALUES(?, ?, ?, ?, ?)`,
				newID(), vID, f.Path, f.Content, ro); err != nil {
				return 0, nil, fmt.Errorf("insert manual file %q: %w", f.Path, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE iterations SET result_version_id = ? WHERE id = ?`, vID, iterID); err != nil {
			return 0, nil, fmt.Errorf("set iteration result: %w", err)
		}

		if compileResult.Pass {
			// Only a successful compile promotes to stable and CAS-switches
			// stableVersionId (acceptance 2, B-FR-07).
			if _, err := tx.ExecContext(ctx,
				`UPDATE versions SET status = 'stable' WHERE id = ?`, vID); err != nil {
				return 0, nil, fmt.Errorf("promote manual version: %w", err)
			}
			casRes, err := tx.ExecContext(ctx,
				`UPDATE projects SET stable_version_id = ?, updated_at = ?
				  WHERE id = ? AND COALESCE(stable_version_id, '') = COALESCE(?, '')`,
				vID, now, projectID, base)
			if err != nil {
				return 0, nil, fmt.Errorf("cas stable version: %w", err)
			}
			n, err := casRes.RowsAffected()
			if err != nil {
				return 0, nil, fmt.Errorf("read stable version update count: %w", err)
			}
			if n == 0 {
				return 0, nil, conflict("baseVersionId no longer matches stable version", nil)
			}
			// The stable preview is part of ProjectDetail's comparable workflow
			// snapshot. Advance stateVersion in this same success transaction so
			// clients can order a concurrent pre-save response behind this one.
			if err := bumpProjectWorkflowSnapshotTx(ctx, tx, projectID, now); err != nil {
				return 0, nil, err
			}
			resultVID := vID
			iter := Iteration{
				ID: iterID, ProjectID: projectID, Kind: "manual",
				BaseVersionID: &base, ResultVersionID: &resultVID, CreatedAt: now,
			}
			out, err := json.Marshal(iter)
			if err != nil {
				return 0, nil, fmt.Errorf("marshal iteration: %w", err)
			}
			return 202, out, nil
		}

		// Compile failed: mark the version 'failed' and DO NOT touch
		// stableVersionId. The previous stable and its preview stay authoritative
		// (acceptance 2). The failed version is recorded for history.
		if _, err := tx.ExecContext(ctx,
			`UPDATE versions SET status = 'failed' WHERE id = ?`, vID); err != nil {
			return 0, nil, fmt.Errorf("fail manual version: %w", err)
		}
		// Return 422 VALIDATION_ERROR with the structured compile errors so the UI
		// can surface them and offer a correct/retry. The failed version is
		// committed with this transaction. 422 is intentionally not cached by the
		// idempotency ledger (contract §idempotency), so a client that fixes the
		// code and retries re-runs the gate against the new content.
		errBody, mErr := json.Marshal(contractErrorResponse{
			Code:      "VALIDATION_ERROR",
			Message:   formatCompileMessage(compileResult.Errors),
			Retryable: false,
			Details: map[string]any{
				"errors":      compileResult.Errors,
				"compilePass": false,
			},
		})
		if mErr != nil {
			return 0, nil, fmt.Errorf("marshal compile error: %w", mErr)
		}
		return 422, errBody, nil
	})
	if err != nil {
		return 0, nil, false, err
	}
	return res.status, res.body, res.replayed, nil
}

// contractErrorResponse mirrors the API error shape (code/message/retryable/
// details) so a store method that must commit a transaction AND return a non-2xx
// response - a manual edit that records a failed version then rejects the save
// with the compile errors - can hand the handler a pre-serialized body to write
// verbatim. It is the store-side equivalent of api.APIError / writeStoreErr.
type contractErrorResponse struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// formatCompileMessage turns a compile gate's structured errors into one
// human-displayable, sanitized line for the 422 message field (no keys/stacks -
// C-NFR-03). It lists at most the first few errors.
func formatCompileMessage(errs []compile.Error) string {
	if len(errs) == 0 {
		return "App.tsx 编译失败，请修正后重试。"
	}
	const max = 3
	out := "App.tsx 编译失败："
	for i, e := range errs {
		if i >= max {
			out += fmt.Sprintf("（另 %d 处错误）", len(errs)-max)
			break
		}
		if i > 0 {
			out += "；"
		}
		out += fmt.Sprintf("第 %d 行 %s", e.Line, e.Message)
	}
	return out
}

// FileTree is the listFiles response: the stable version's file tree with
// readonly markers (contract §paths.listFiles). Draft entries are added by
// FLO-56/FLO-60 during an active run.
type FileTree struct {
	StableVersionID  *string         `json:"stableVersionId"`
	Files            []FileTreeEntry `json:"files"`
	WritableFilePath string          `json:"writableFilePath"`
}

// FileTreeEntry is one node in the file tree.
type FileTreeEntry struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Readonly bool   `json:"readonly"`
}

// ListFiles returns the current stable version's file tree. If the project has no
// stable version yet, the tree is empty.
func (s *Store) ListFiles(ctx context.Context, projectID string) (*FileTree, error) {
	var stable sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable)
	if err != nil {
		if isNoRows(err) {
			return nil, notFound("project not found")
		}
		return nil, fmt.Errorf("get project: %w", err)
	}
	tree := &FileTree{WritableFilePath: limits().WritableFilePath}
	if stable.Valid {
		v := stable.String
		tree.StableVersionID = &v
		rows, err := s.db.QueryContext(ctx,
			`SELECT path, content, readonly FROM files WHERE version_id = ? ORDER BY path ASC`, v)
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e FileTreeEntry
			var ro int
			if err := rows.Scan(&e.Path, &e.Content, &ro); err != nil {
				return nil, fmt.Errorf("scan file: %w", err)
			}
			e.Readonly = ro == 1
			tree.Files = append(tree.Files, e)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

// readVersionFilesTx loads a version's files as a snapshot inside a transaction.
func (s *Store) readVersionFilesTx(ctx context.Context, tx *sql.Tx, versionID string) ([]FileSnapshot, error) {
	rows, err := tx.QueryContext(ctx,
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

// applyOverride returns files with the writable path replaced by the new content.
// If the path is not yet present it is appended.
func applyOverride(files []FileSnapshot, path, content string, readonly bool) []FileSnapshot {
	for i, f := range files {
		if f.Path == path {
			files[i] = FileSnapshot{Path: path, Content: content, Readonly: readonly}
			return files
		}
	}
	return append(files, FileSnapshot{Path: path, Content: content, Readonly: readonly})
}
