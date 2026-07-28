package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CreateProject creates a project and its seed user message. It is idempotent on
// the Idempotency-Key: a repeat within ttlSeconds replays the original 201
// response without creating a duplicate project (contract §paths.createProject,
// §idempotency). On success it returns the JSON response body and replayed=false
// (or replayed=true on a cache hit). Application errors (validation) are
// returned as *Error and are not cached.
//
// The initialPrompt is validated after trimming (VF-P0-02: whitespace-only is a
// 422, never persisted) and is persisted as the initial user message atomically
// with the project, so the prompt is queryable from getProject before any run
// starts and survives a later run-startup failure (VF-P0-03).
func (s *Store) CreateProject(ctx context.Context, title, initialPrompt, idempotencyKey string) (status int, body []byte, replayed bool, err error) {
	res, err := s.runIdempotent(ctx, "createProject", idempotencyKey, func(tx *sql.Tx) (int, []byte, error) {
		lim := limits()
		prompt := strings.TrimSpace(initialPrompt)
		n := utf8.RuneCountInString(prompt)
		if n < lim.PromptMinChars || n > lim.PromptMaxChars {
			return 0, nil, validation("initialPrompt length out of range", map[string]any{
				"min": lim.PromptMinChars, "max": lim.PromptMaxChars, "actual": n,
			})
		}
		t := strings.TrimSpace(title)
		if t == "" {
			t = deriveTitle(prompt)
		}
		now := s.nowTS()
		p := Project{
			ID:        newID(),
			Title:     t,
			Status:    "active",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects(id, title, status, stable_version_id, created_at, updated_at)
			 VALUES(?, ?, ?, NULL, ?, ?)`,
			p.ID, p.Title, p.Status, p.CreatedAt, p.UpdatedAt); err != nil {
			return 0, nil, fmt.Errorf("insert project: %w", err)
		}
		if err := initProjectWorkflowTx(ctx, tx, p.ID, now); err != nil {
			return 0, nil, err
		}
		// Persist the initial user message atomically with the project. This is
		// the durable home of the prompt: it is queryable before any run starts
		// and survives a run-startup failure (503/429/config-missing). createRun
		// reuses this message for the first run instead of duplicating it.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages(id, project_id, role, content, created_at)
			 VALUES(?, ?, 'user', ?, ?)`,
			newID(), p.ID, prompt, now); err != nil {
			return 0, nil, fmt.Errorf("insert initial message: %w", err)
		}
		out, err := json.Marshal(p)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal project: %w", err)
		}
		return 201, out, nil
	})
	if err != nil {
		return 0, nil, false, err
	}
	return res.status, res.body, res.replayed, nil
}

// deriveTitle makes a human title from a prompt: first line, trimmed to 60 runes.
func deriveTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if i := strings.IndexByte(prompt, '\n'); i >= 0 {
		prompt = prompt[:i]
	}
	prompt = strings.TrimSpace(prompt)
	if utf8.RuneCountInString(prompt) <= 60 {
		return prompt
	}
	rs := []rune(prompt)
	return string(rs[:60]) + "…"
}

// ListProjects returns all projects ordered by updated_at desc (C-FR-02). The
// stable_version_id is included so the list can show the current stable summary.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, status, stable_version_id, created_at, updated_at
		   FROM projects
		  ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var ps []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// ProjectDetail is the aggregated getProject response (contract §paths.getProject).
type ProjectDetail struct {
	Project
	Messages          []Message            `json:"messages"`
	Runs              []Run                `json:"runs"`
	Versions          []Version            `json:"versions"`
	Artifacts         []StageArtifact      `json:"artifacts"`
	WorkflowStatus    string               `json:"workflowStatus"`
	WorkflowRunID     *string              `json:"workflowRunId"`
	StateVersion      int64                `json:"stateVersion"`
	StateUpdatedAt    string               `json:"stateUpdatedAt"`
	ResponseUpdatedAt string               `json:"responseUpdatedAt"`
	Stages            []WorkflowStageState `json:"stages"`
	Preview           WorkflowPreview      `json:"preview"`
	Consistency       WorkflowConsistency  `json:"consistency"`
	LatestRun         *Run                 `json:"latestRun,omitempty"`
	ActiveRun         *Run                 `json:"activeRun,omitempty"`
}

// GetProject returns the aggregated project detail. Per C-FR-02 ("打开 A 后 A
// 置顶"), viewing a project touches its updated_at so it rises to the top of the
// recent list. A nonexistent id returns ErrNotFound and never leaks another
// project's data.
func (s *Store) GetProject(ctx context.Context, id string) (*ProjectDetail, error) {
	d := &ProjectDetail{}
	now := s.nowTS()

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		p, err := scanProject(tx.QueryRowContext(ctx,
			`SELECT id, title, status, stable_version_id, created_at, updated_at
			   FROM projects WHERE id = ?`, id))
		if err != nil {
			if isNoRows(err) {
				return notFound("project not found")
			}
			return fmt.Errorf("get project: %w", err)
		}
		// Touch recency (C-FR-02). A 0-row update would mean the project vanished
		// mid-tx; treat as not found.
		res, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, now, id)
		if err != nil {
			return fmt.Errorf("touch project: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("project not found")
		}
		p.UpdatedAt = now
		d.Project = p

		if err := scanMessages(ctx, tx, id, &d.Messages); err != nil {
			return err
		}
		if err := scanRuns(ctx, tx, id, &d.Runs); err != nil {
			return err
		}
		if err := scanVersions(ctx, tx, id, &d.Versions); err != nil {
			return err
		}
		if err := scanArtifacts(ctx, tx, id, &d.Artifacts); err != nil {
			return err
		}
		if err := s.loadWorkflowSnapshotTx(ctx, tx, d); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// inTx runs fn inside a transaction. The deferred rollback is a no-op after a
// successful commit.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// scanProject scans a project row from a row-like scanner.
func scanProject(r interface {
	Scan(dest ...any) error
}) (Project, error) {
	var p Project
	var stable sql.NullString
	if err := r.Scan(&p.ID, &p.Title, &p.Status, &stable, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	if stable.Valid {
		v := stable.String
		p.StableVersionID = &v
	}
	return p, nil
}

func scanMessages(ctx context.Context, tx *sql.Tx, projectID string, out *[]Message) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, project_id, role, content, created_at
		   FROM messages WHERE project_id = ? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}
		*out = append(*out, m)
	}
	return rows.Err()
}

func scanRuns(ctx context.Context, tx *sql.Tx, projectID string, out *[]Run) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, project_id, status, prompt, base_version_id, active_attempt_id, created_at, updated_at
		   FROM runs WHERE project_id = ? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r Run
		var base, active sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Status, &r.Prompt, &base, &active, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return fmt.Errorf("scan run: %w", err)
		}
		if base.Valid {
			v := base.String
			r.BaseVersionID = &v
		}
		if active.Valid {
			v := active.String
			r.ActiveAttemptID = &v
		}
		*out = append(*out, r)
	}
	return rows.Err()
}

func scanVersions(ctx context.Context, tx *sql.Tx, projectID string, out *[]Version) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, project_id, iteration_id, status, files_hash, created_at
		   FROM versions WHERE project_id = ? ORDER BY created_at ASC`, projectID)
	if err != nil {
		return fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v Version
		var hash sql.NullString
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.IterationID, &v.Status, &hash, &v.CreatedAt); err != nil {
			return fmt.Errorf("scan version: %w", err)
		}
		if hash.Valid {
			h := hash.String
			v.FilesHash = &h
		}
		*out = append(*out, v)
	}
	return rows.Err()
}

func scanArtifacts(ctx context.Context, tx *sql.Tx, projectID string, out *[]StageArtifact) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT sa.id, sa.run_id, sa.attempt_id, sa.stage, sa.artifact_type, sa.artifact_ref, sa.created_at
		   FROM stage_artifacts sa
		   JOIN runs r ON r.id = sa.run_id
		  WHERE r.project_id = ? ORDER BY sa.created_at ASC`, projectID)
	if err != nil {
		return fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a StageArtifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.AttemptID, &a.Stage, &a.ArtifactType, &a.ArtifactRef, &a.CreatedAt); err != nil {
			return fmt.Errorf("scan artifact: %w", err)
		}
		*out = append(*out, a)
	}
	return rows.Err()
}
