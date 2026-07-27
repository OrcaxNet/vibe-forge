package store

import (
	"database/sql"
	"errors"
	"testing"
)

func sampleFiles(app string) []FileSnapshot {
	return []FileSnapshot{
		{Path: "/index.html", Content: "<html></html>", Readonly: true},
		{Path: "/src/main.tsx", Content: "render()", Readonly: true},
		{Path: "/src/App.tsx", Content: app, Readonly: false},
	}
}

// TestCommitVersionSuccess (C-FR-03): the success transaction writes the file
// snapshot, the iteration result and switches stableVersionId atomically; the
// re-read files hash matches.
func TestCommitVersionSuccess(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk")

	files := sampleFiles("export default () => <div/>")
	v := commitStable(t, s, p.ID, iterID, runID, files)

	if stableVersionID(t, s, p.ID) != v.ID {
		t.Fatal("stableVersionId was not switched to the new version")
	}
	if v.Status != "stable" {
		t.Errorf("version status = %q, want stable", v.Status)
	}
	// Re-read files hash matches the committed hash.
	got, err := s.GetVersionFiles(ctx, p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hashOf(got) != deref(v.FilesHash) {
		t.Errorf("re-read files hash %q != committed %q", hashOf(got), deref(v.FilesHash))
	}
	// Run + active attempt are marked succeeded.
	var runStatus string
	s.db.QueryRow(`SELECT status FROM runs WHERE id = ?`, runID).Scan(&runStatus)
	if runStatus != "succeeded" {
		t.Errorf("run status = %q, want succeeded", runStatus)
	}
	// Iteration result_version_id is set.
	var rv sql.NullString
	s.db.QueryRow(`SELECT result_version_id FROM iterations WHERE id = ?`, iterID).Scan(&rv)
	if !rv.Valid || rv.String != v.ID {
		t.Errorf("iteration result_version_id = %v, want %q", rv, v.ID)
	}
}

// TestCommitVersionRollbackOnFailure (criterion 3): when any step of the success
// transaction fails, stableVersionId is unchanged and no partial version is left.
func TestCommitVersionRollbackOnFailure(t *testing.T) {
	cases := []struct {
		name string
		fp   failPoint
	}{
		{"afterVersionInsert", failAfterVersionInsert},
		{"afterFilesInsert", failAfterFilesInsert},
		{"afterIterationUpdate", failAfterIterationUpdate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			p := createProject(t, s, "pk", "build an app")
			runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk")
			before := stableVersionID(t, s, p.ID)

			err := s.commitVersionFailing(ctx, CommitInput{
				ProjectID: p.ID, IterationID: iterID, RunID: runID, Files: sampleFiles("x"),
			}, c.fp)
			if !errors.Is(err, errInjected) {
				t.Fatalf("expected injected failure, got %v", err)
			}
			if after := stableVersionID(t, s, p.ID); after != before {
				t.Errorf("stableVersionId changed on failure: %q -> %q", before, after)
			}
			var n int
			s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE project_id = ?`, p.ID).Scan(&n)
			if n != 0 {
				t.Errorf("expected 0 versions after rollback, got %d", n)
			}
			var rv sql.NullString
			s.db.QueryRow(`SELECT result_version_id FROM iterations WHERE id = ?`, iterID).Scan(&rv)
			if rv.Valid {
				t.Error("iteration result_version_id should be NULL after rollback")
			}
		})
	}
}

// TestCommitVersionIdempotent (criterion 3): re-committing the same iteration
// returns the already-committed version without creating a duplicate.
func TestCommitVersionIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk")
	in := CommitInput{ProjectID: p.ID, IterationID: iterID, RunID: runID, Files: sampleFiles("v1")}

	v1, err := s.CommitVersion(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Second commit with DIFFERENT files still returns the same version.
	in.Files = sampleFiles("different content that should be ignored")
	v2, err := s.CommitVersion(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if v1.ID != v2.ID {
		t.Errorf("idempotent re-commit returned different version: %q vs %q", v1.ID, v2.ID)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE project_id = ?`, p.ID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 version after idempotent re-commit, got %d", n)
	}
}

// TestFailVersionKeepsStable (C-FR-04): a failed attempt is recorded for history
// but never becomes the stable version; the previous stable preview is retained.
func TestFailVersionKeepsStable(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	run1, iter1 := createRun(t, s, p.ID, "build an app", "", "rk1")
	v1 := commitStable(t, s, p.ID, iter1, run1, sampleFiles("v1"))

	run2, iter2 := createRun(t, s, p.ID, "edit it", v1.ID, "rk2")
	v2, err := s.FailVersion(ctx, FailInput{
		ProjectID: p.ID, IterationID: iter2, RunID: run2,
		Files: sampleFiles("broken"), Code: "COMPILE_FAILED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Status != "failed" {
		t.Errorf("failed version status = %q, want failed", v2.Status)
	}
	if stableVersionID(t, s, p.ID) != v1.ID {
		t.Error("stableVersionId must stay on V1 after V2 failed")
	}
	vs, err := s.ListVersions(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Status != "stable" || vs[1].Status != "failed" {
		t.Fatalf("expected [stable, failed], got %+v", vs)
	}
	// Failed version still has its file snapshot for history.
	got, err := s.GetVersionFiles(ctx, p.ID, v2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got["/src/App.tsx"] != "broken" {
		t.Errorf("failed version files not retained: %v", got)
	}
}

// TestRestoreVersion (C-FR-07): restore creates a restore iteration and atomically
// switches stableVersionId back to a previous stable version; idempotent on key.
func TestRestoreVersion(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	run1, iter1 := createRun(t, s, p.ID, "build an app", "", "rk1")
	v1 := commitStable(t, s, p.ID, iter1, run1, sampleFiles("v1"))
	run2, iter2 := createRun(t, s, p.ID, "edit", v1.ID, "rk2")
	v2 := commitStable(t, s, p.ID, iter2, run2, sampleFiles("v2"))
	if stableVersionID(t, s, p.ID) != v2.ID {
		t.Fatal("precondition: stable should be V2")
	}

	_, _, replayed, err := s.RestoreVersion(ctx, p.ID, v1.ID, "restKey")
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Error("first restore should not be a replay")
	}
	if stableVersionID(t, s, p.ID) != v1.ID {
		t.Fatal("restore did not switch stable back to V1")
	}
	// A restore iteration was recorded pointing at V1.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM iterations WHERE project_id = ? AND kind = 'restore'`, p.ID).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 restore iteration, got %d", n)
	}
	// Idempotent: same key replays without creating another restore iteration.
	_, _, replayed2, err := s.RestoreVersion(ctx, p.ID, v1.ID, "restKey")
	if err != nil {
		t.Fatal(err)
	}
	if !replayed2 {
		t.Error("second restore with same key should be a replay")
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM iterations WHERE project_id = ? AND kind = 'restore'`, p.ID).Scan(&n)
	if n != 1 {
		t.Errorf("idempotent restore created extra iterations: %d", n)
	}
}

// TestRestoreVersionRejectsNonStable: restoring a failed version is rejected.
func TestRestoreVersionRejectsNonStable(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	run1, iter1 := createRun(t, s, p.ID, "build an app", "", "rk1")
	v1 := commitStable(t, s, p.ID, iter1, run1, sampleFiles("v1"))
	run2, iter2 := createRun(t, s, p.ID, "edit", v1.ID, "rk2")
	v2, _ := s.FailVersion(ctx, FailInput{ProjectID: p.ID, IterationID: iter2, RunID: run2, Code: "COMPILE_FAILED"})

	_, _, _, err := s.RestoreVersion(ctx, p.ID, v2.ID, "restKey")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("restoring a failed version should be CONFLICT, got %v", err)
	}
}

// TestListVersionsNoCode: ListVersions returns versions without file content.
func TestListVersionsNoCode(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk")
	commitStable(t, s, p.ID, iterID, runID, sampleFiles("secret-content"))

	vs, err := s.ListVersions(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("expected 1 version, got %d", len(vs))
	}
	// Version has a hash but no content field exists on the type.
	if vs[0].FilesHash == nil {
		t.Error("expected filesHash to be set")
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// hashOf computes filesHash from a Sandpack files map.
func hashOf(m map[string]string) string {
	files := make([]FileSnapshot, 0, len(m))
	for path, content := range m {
		files = append(files, FileSnapshot{Path: path, Content: content})
	}
	return filesHash(files)
}
