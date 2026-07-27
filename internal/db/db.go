// Package db owns the SQLite connection and schema migration. It is the
// foundation for Stage 2 (FLO-55), which adds the full data model, transactions
// and idempotency semantics on top of this skeleton.
//
// Design notes (from PRD-C §5.3):
//   - SQLite is the single source of truth; WAL + foreign_keys are enabled.
//   - Migrations are versioned and tracked in schema_migrations; re-running the
//     runner is idempotent and never re-applies a version.
//   - A migration failure MUST stop startup (no silent skip).
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open opens (or creates) the SQLite database at path and enables WAL +
// foreign_keys. Use ":memory:" for tests.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite serializes writes; a small pool is enough and keeps memory bounded.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// Migrate applies all embedded migrations that have not yet been recorded in
// schema_migrations. It is idempotent: running it twice applies each version at
// most once. Any error aborts the transaction for that version and is returned
// so the caller can refuse to start.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	versions, err := loadMigrationVersions()
	if err != nil {
		return err
	}
	for _, v := range versions {
		applied, err := isApplied(db, v)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		stmt, err := migrationFS.ReadFile(filepath.Join("migrations", v+".sql"))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", v, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", v, err)
		}
		if _, err := tx.Exec(string(stmt)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", v, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			v, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", v, err)
		}
	}
	return nil
}

func loadMigrationVersions() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		versions = append(versions, strings.TrimSuffix(e.Name(), ".sql"))
	}
	sort.Strings(versions)
	return versions, nil
}

func isApplied(db *sql.DB, version string) (bool, error) {
	var one int
	err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, version).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}
	return true, nil
}
