package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestCreateProjectIdempotent (criterion 2): the same Idempotency-Key does not
// create a duplicate project; the second call replays the original 201 response.
func TestCreateProjectIdempotent(t *testing.T) {
	s, _ := newTestStore(t)

	p1 := createProject(t, s, "key-A", "build a habit tracker")
	_, body2, replayed2, err := s.CreateProject(ctx, "", "build a habit tracker", "key-A")
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if !replayed2 {
		t.Error("second call with same key should be a replay")
	}
	// The replayed body must carry the SAME project id as the original response.
	var p2 Project
	if err := json.Unmarshal(body2, &p2); err != nil {
		t.Fatalf("decode replayed project: %v", err)
	}
	if p2.ID != p1.ID {
		t.Errorf("replay returned different project id: %q vs %q", p2.ID, p1.ID)
	}

	// Exactly one project exists.
	ps, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Errorf("expected 1 project, got %d", len(ps))
	}
}

// TestCreateProjectDifferentKeys creates distinct projects for distinct keys.
func TestCreateProjectDifferentKeys(t *testing.T) {
	s, clk := newTestStore(t)
	a := createProject(t, s, "k1", "prompt one")
	clk.advance(1 * time.Second)
	b := createProject(t, s, "k2", "prompt two")
	if a.ID == b.ID {
		t.Fatal("distinct keys produced the same project id")
	}
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 2 {
		t.Errorf("expected 2 projects, got %d", len(ps))
	}
}

// TestIdempotencyTTLExpiry (criterion 2): after ttlSeconds the same key is
// treated as fresh and creates a new project.
func TestIdempotencyTTLExpiry(t *testing.T) {
	s, clk := newTestStore(t)
	p1 := createProject(t, s, "key-TTL", "first attempt")
	// Advance past the contract TTL (30s).
	clk.advance(31 * time.Second)
	p2 := createProject(t, s, "key-TTL", "first attempt")
	if p1.ID == p2.ID {
		t.Error("same key after TTL should create a new project")
	}
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 2 {
		t.Errorf("expected 2 projects after TTL expiry, got %d", len(ps))
	}
}

// TestListProjectsOrdering (C-FR-02): list is updatedAt desc; creating A then B
// yields B first; opening A bumps A.updatedAt so A rises to the top.
func TestListProjectsOrdering(t *testing.T) {
	s, clk := newTestStore(t)
	a := createProject(t, s, "ka", "alpha")
	clk.advance(1 * time.Second)
	b := createProject(t, s, "kb", "beta")

	ps, _ := s.ListProjects(ctx)
	if len(ps) != 2 || ps[0].ID != b.ID || ps[1].ID != a.ID {
		t.Fatalf("expected [B, A], got %v", order(ps))
	}

	// "Open" A -> A rises to the top.
	clk.advance(1 * time.Second)
	if _, err := s.GetProject(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	ps, _ = s.ListProjects(ctx)
	if ps[0].ID != a.ID {
		t.Errorf("expected A on top after opening, got %v", order(ps))
	}
}

// TestGetProjectNotFoundNoLeak (C-FR-02): a nonexistent project returns
// NOT_FOUND and never exposes another project's data.
func TestGetProjectNotFoundNoLeak(t *testing.T) {
	s, _ := newTestStore(t)
	a := createProject(t, s, "ka", "alpha")

	_, err := s.GetProject(ctx, "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	d, err := s.GetProject(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != a.ID {
		t.Errorf("detail returned wrong project: %q", d.ID)
	}
	// Detail must contain only this project's rows.
	for _, m := range d.Messages {
		if m.ProjectID != a.ID {
			t.Errorf("detail leaked message from another project: %+v", m)
		}
	}
	for _, v := range d.Versions {
		if v.ProjectID != a.ID {
			t.Errorf("detail leaked version from another project: %+v", v)
		}
	}
}

// TestCreateProjectValidation: prompt length is enforced.
func TestCreateProjectValidation(t *testing.T) {
	s, _ := newTestStore(t)
	_, _, _, err := s.CreateProject(ctx, "", "", "k")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("empty prompt should be VALIDATION_ERROR, got %v", err)
	}
}

func order(ps []Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}
