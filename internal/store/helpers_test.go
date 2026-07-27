package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/db"
)

// fakeClock is a controllable clock for deterministic ordering/TTL tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestStore returns a store backed by a fresh in-memory migrated database and
// a controllable clock anchored at a fixed UTC instant.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	dbase, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(dbase); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbase.Close() })
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewWithClock(dbase, c.now), c
}

var ctx = context.Background()

// createProject is a test helper that creates a project and decodes the response.
func createProject(t *testing.T, s *Store, key, prompt string) Project {
	t.Helper()
	status, body, _, err := s.CreateProject(ctx, "", prompt, key)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if status != 201 {
		t.Fatalf("CreateProject status = %d, want 201 (body=%s)", status, body)
	}
	var p Project
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return p
}

// createRun is a test helper that creates a run and returns runID + iterationID.
func createRun(t *testing.T, s *Store, projectID, prompt, baseVersionID, key string) (string, string) {
	t.Helper()
	status, body, _, err := s.CreateRun(ctx, projectID, prompt, baseVersionID, key)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status != 202 {
		t.Fatalf("CreateRun status = %d, want 202 (body=%s)", status, body)
	}
	var rr struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(body, &rr); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	var iterationID string
	if err := s.db.QueryRow(`SELECT id FROM iterations WHERE run_id = ?`, rr.RunID).Scan(&iterationID); err != nil {
		t.Fatalf("lookup iteration: %v", err)
	}
	return rr.RunID, iterationID
}

// commitStable commits a stable version for an iteration and returns the version.
func commitStable(t *testing.T, s *Store, projectID, iterationID, runID string, files []FileSnapshot) Version {
	t.Helper()
	v, err := s.CommitVersion(ctx, CommitInput{
		ProjectID: projectID, IterationID: iterationID, RunID: runID, Files: files,
	})
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	return v
}

// stableVersionID reads the project's current stable_version_id ("" if NULL).
func stableVersionID(t *testing.T, s *Store, projectID string) string {
	t.Helper()
	var stable sql.NullString
	err := s.db.QueryRow(`SELECT stable_version_id FROM projects WHERE id = ?`, projectID).Scan(&stable)
	if err != nil {
		t.Fatalf("read stable: %v", err)
	}
	if !stable.Valid {
		return ""
	}
	return stable.String
}
