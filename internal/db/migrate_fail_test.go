package db

import (
	"testing"
)

// TestMigrateFailureIsSurfaced (criterion 6): when a migration cannot be applied,
// Migrate returns the error and does NOT record the version (no silent skip). The
// startup path (api.New -> main log.Fatalf) therefore refuses to serve.
//
// We force a failure by pre-seeding two active runs for one project before the
// partial UNIQUE index idx_runs_one_active is created; the index build then fails.
func TestMigrateFailureIsSurfaced(t *testing.T) {
	dbase, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	for _, stmt := range []string{
		`CREATE TABLE projects(id TEXT PRIMARY KEY, title TEXT, status TEXT, stable_version_id TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE runs(id TEXT PRIMARY KEY, project_id TEXT, status TEXT, prompt TEXT, base_version_id TEXT, active_attempt_id TEXT, created_at TEXT, updated_at TEXT)`,
		`INSERT INTO projects(id,title,status,created_at,updated_at) VALUES('p','t','active','t','t')`,
		`INSERT INTO runs(id,project_id,status,prompt,created_at,updated_at) VALUES('r1','p','queued','x','t','t')`,
		`INSERT INTO runs(id,project_id,status,prompt,created_at,updated_at) VALUES('r2','p','queued','x','t','t')`,
	} {
		if _, err := dbase.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	if err := Migrate(dbase); err == nil {
		t.Fatal("expected Migrate to fail when the unique index cannot be built")
	}

	// The failed migration must not be recorded as applied.
	var n int
	if err := dbase.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 0 {
		t.Errorf("failed migration was recorded (%d); silent skip is forbidden", n)
	}
}
