package db

import (
	"testing"
)

// TestMigrateIdempotent asserts C-FR-08 foundation: running Migrate twice on a
// fresh in-memory database succeeds and records exactly one migration version.
func TestMigrateIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 recorded migration, got %d", n)
	}

	// Core tables from the contract models exist.
	for _, table := range []string{
		"projects", "messages", "runs", "attempts", "stage_artifacts",
		"iterations", "versions", "files", "events", "idempotency_records",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after migrate: %v", table, err)
		}
	}
}

// TestStatusCheckConstraints asserts the status enums from the contract are
// enforced at the DB layer.
func TestStatusCheckConstraints(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err := db.Exec(`INSERT INTO projects(id, title, status, created_at, updated_at)
		VALUES('p1','t','bogus','t','t')`)
	if err == nil {
		t.Error("expected CHECK constraint to reject bogus project status")
	}
}
