package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// WriteFile performs a manual edit of the single writable file (/src/App.tsx),
// creating a manual iteration and atomically committing a new stable version
// (B-FR / contract §paths.writeFile). It enforces the manual/agent mutex (409 if
// an active run is in flight) and the baseVersionId optimistic lock. Idempotent
// on the key. Returns the manual iteration JSON with status 202.
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

		now := s.nowTS()
		iterID := newID()
		base := stable.String
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO iterations(id, project_id, run_id, kind, base_version_id, result_version_id, prompt, created_at)
			 VALUES(?, ?, NULL, 'manual', ?, NULL, NULL, ?)`,
			iterID, projectID, base, now); err != nil {
			return 0, nil, fmt.Errorf("insert manual iteration: %w", err)
		}
		v, err := s.commitVersionSteps(ctx, tx, CommitInput{
			ProjectID: projectID, IterationID: iterID, Files: files,
		}, failNone)
		if err != nil {
			return 0, nil, err
		}
		iter := Iteration{
			ID: iterID, ProjectID: projectID, Kind: "manual",
			BaseVersionID: &base, ResultVersionID: &v.ID, CreatedAt: now,
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

// FileTree is the listFiles response: the stable version's file tree with
// readonly markers (contract §paths.listFiles). Draft entries are added by
// FLO-56/FLO-60 during an active run.
type FileTree struct {
	StableVersionID *string         `json:"stableVersionId"`
	Files           []FileTreeEntry `json:"files"`
	WritableFilePath string         `json:"writableFilePath"`
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
