package store

import (
	"errors"
	"testing"
)

// TestCreateRunIdempotent (criterion 2): the same Idempotency-Key does not create
// a duplicate run.
func TestCreateRunIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")

	runID1, _ := createRun(t, s, p.ID, "build an app", "", "rk")
	_, _, replayed, err := s.CreateRun(ctx, p.ID, "build an app", "", "rk")
	if err != nil {
		t.Fatalf("second CreateRun: %v", err)
	}
	if !replayed {
		t.Error("second CreateRun with same key should be a replay")
	}
	// Exactly one run exists.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE project_id = ?`, p.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 run, got %d", n)
	}
	_ = runID1
}

// TestCreateRunActiveConflict (criterion 2): a second run while one is active
// returns 409 CONFLICT carrying the activeRunId.
func TestCreateRunActiveConflict(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	runID, _ := createRun(t, s, p.ID, "build an app", "", "rk1")

	_, _, _, err := s.CreateRun(ctx, p.ID, "build an app again", "", "rk2")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for second active run, got %v", err)
	}
	var e *Error
	if errors.As(err, &e) {
		if e.Details["activeRunId"] != runID {
			t.Errorf("conflict details activeRunId = %v, want %q", e.Details["activeRunId"], runID)
		}
	}
}

// TestCreateRunOptimisticLock: a baseVersionId that does not match the current
// stable version is rejected with 409.
func TestCreateRunOptimisticLock(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk1")
	v := commitStable(t, s, p.ID, iterID, runID, []FileSnapshot{{Path: "/src/App.tsx", Content: "v1"}})

	// Correct base -> 202.
	createRun(t, s, p.ID, "edit it", v.ID, "rk2")

	// Wrong base -> 409.
	_, _, _, err := s.CreateRun(ctx, p.ID, "edit it", "bogus-version-id", "rk3")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict for base mismatch, got %v", err)
	}
}

// TestCreateRunValidation: prompt length is enforced.
func TestCreateRunValidation(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	_, _, _, err := s.CreateRun(ctx, p.ID, "", "", "rk")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("empty prompt should be VALIDATION_ERROR, got %v", err)
	}
}
