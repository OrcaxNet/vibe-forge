package store

import (
	"path/filepath"
	"testing"

	"github.com/OrcaxNet/vibe-forge/internal/db"
)

// TestSQLitePersistsAcrossReopen (criterion 5 + C-FR-01/05): SQLite on a declared
// path is the source of truth. Data written before a close survives a reopen
// (simulating a container restart with the volume kept). The browser holds no
// source-of-truth state; everything is queryable from the server-side DB.
func TestSQLitePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vibe-forge.db")

	// First run: open, migrate, create a project + stable version.
	d1, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d1); err != nil {
		t.Fatal(err)
	}
	s1 := New(d1)
	p := createProject(t, s1, "pk", "build an app")
	runID, iterID := createRun(t, s1, p.ID, "build an app", "", "rk")
	v := commitStable(t, s1, p.ID, iterID, runID, sampleFiles("persisted"))
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	// Second run: reopen the same file, re-run migrations (idempotent), and verify
	// all data is still queryable with no browser involvement.
	d2, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if err := db.Migrate(d2); err != nil {
		t.Fatal(err)
	}
	s2 := New(d2)

	ps, err := s2.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != p.ID {
		t.Fatalf("project not restored: %+v", ps)
	}
	if stableVersionID(t, s2, p.ID) != v.ID {
		t.Error("stableVersionId not restored after reopen")
	}
	files, err := s2.GetVersionFiles(ctx, p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if files["/src/App.tsx"] != "persisted" {
		t.Errorf("file content not restored: %v", files)
	}
}

// TestMigrationIdempotentOnFileDB (criterion 6): running Migrate repeatedly on a
// real file DB records the version exactly once and leaves data intact.
func TestMigrationIdempotentOnFileDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vibe-forge.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO projects(id,title,status,created_at,updated_at) VALUES('p','t','active','t','t')`)
	if err := db.Migrate(d); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 2 {
		t.Errorf("expected 2 recorded migrations, got %d", n)
	}
	var c int
	d.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&c)
	if c != 1 {
		t.Errorf("data lost after re-migrate: %d", c)
	}
}
