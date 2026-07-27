package store

import (
	"errors"
	"time"
	"testing"
)

// TestProjectIsolation (criterion 4): two projects' messages, versions, files and
// stage artifacts never cross. Querying one project with another's version id is
// a 404, not a leak.
func TestProjectIsolation(t *testing.T) {
	s, clk := newTestStore(t)
	a := createProject(t, s, "ka", "build A")
	clk.advance(1 * time.Second)
	b := createProject(t, s, "kb", "build B")

	// A -> VA, B -> VB with distinct content.
	runA, iterA := createRun(t, s, a.ID, "build A", "", "rka")
	vA := commitStable(t, s, a.ID, iterA, runA, sampleFiles("AAA"))
	clk.advance(1 * time.Second)
	runB, iterB := createRun(t, s, b.ID, "build B", "", "rkb")
	vB := commitStable(t, s, b.ID, iterB, runB, sampleFiles("BBB"))

	// Versions do not cross.
	va, _ := s.ListVersions(ctx, a.ID)
	vb, _ := s.ListVersions(ctx, b.ID)
	if len(va) != 1 || va[0].ID != vA.ID {
		t.Errorf("project A versions = %+v, want only %q", va, vA.ID)
	}
	if len(vb) != 1 || vb[0].ID != vB.ID {
		t.Errorf("project B versions = %+v, want only %q", vb, vB.ID)
	}

	// Cross-project version file access is NOT_FOUND (no leak).
	if _, err := s.GetVersionFiles(ctx, a.ID, vB.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("A reading B's version should be NOT_FOUND, got %v", err)
	}
	if _, err := s.GetVersionFiles(ctx, b.ID, vA.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B reading A's version should be NOT_FOUND, got %v", err)
	}

	// Files content is isolated.
	fa, _ := s.GetVersionFiles(ctx, a.ID, vA.ID)
	if fa["/src/App.tsx"] != "AAA" {
		t.Errorf("A file content = %q, want AAA", fa["/src/App.tsx"])
	}
	fb, _ := s.GetVersionFiles(ctx, b.ID, vB.ID)
	if fb["/src/App.tsx"] != "BBB" {
		t.Errorf("B file content = %q, want BBB", fb["/src/App.tsx"])
	}

	// Messages are isolated per project.
	da, _ := s.GetProject(ctx, a.ID)
	db, _ := s.GetProject(ctx, b.ID)
	for _, m := range da.Messages {
		if m.ProjectID != a.ID {
			t.Errorf("A leaked message %+v", m)
		}
	}
	for _, m := range db.Messages {
		if m.ProjectID != b.ID {
			t.Errorf("B leaked message %+v", m)
		}
	}

	// Stage artifacts are isolated.
	attA := attemptID(t, s, runA)
	attB := attemptID(t, s, runB)
	s.RecordStageArtifact(ctx, runA, attA, "pm", "spec", "spec-A")
	s.RecordStageArtifact(ctx, runB, attB, "pm", "spec", "spec-B")
	da, _ = s.GetProject(ctx, a.ID)
	db, _ = s.GetProject(ctx, b.ID)
	for _, ar := range da.Artifacts {
		if ar.RunID != runA {
			t.Errorf("A leaked artifact %+v", ar)
		}
	}
	for _, ar := range db.Artifacts {
		if ar.RunID != runB {
			t.Errorf("B leaked artifact %+v", ar)
		}
	}
}

func attemptID(t *testing.T, s *Store, runID string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`SELECT id FROM attempts WHERE run_id = ?`, runID).Scan(&id); err != nil {
		t.Fatalf("lookup attempt: %v", err)
	}
	return id
}
