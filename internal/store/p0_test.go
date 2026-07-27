package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// p0_test.go locks the two FLO-55 P0 regressions found by FLO-58 on main@a4be32f:
//
//   - VF-P0-02: whitespace-only (and empty / overlong) prompts are rejected with
//     VALIDATION_ERROR and leave no project/message/run or idempotency record.
//   - VF-P0-03: a run-startup failure (model not configured) does not lose the
//     initial prompt; the failed run + failure event + retry entry are queryable
//     after a refresh, with no duplicate project or message.

// assertNoDirtyRecords fails if any project/message/idempotency row exists - a
// rejected request must not have persisted anything.
func assertNoDirtyRecords(t *testing.T, db *sql.DB) {
	t.Helper()
	var nProj, nMsg, nIdem int
	db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&nProj)
	db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&nMsg)
	db.QueryRow(`SELECT COUNT(*) FROM idempotency_records`).Scan(&nIdem)
	if nProj != 0 || nMsg != 0 || nIdem != 0 {
		t.Errorf("rejected request left dirty records: projects=%d messages=%d idempotency=%d", nProj, nMsg, nIdem)
	}
}

// TestCreateProjectRejectsWhitespace (VF-P0-02): a whitespace-only initialPrompt
// is rejected and persists nothing.
func TestCreateProjectRejectsWhitespace(t *testing.T) {
	s, _ := newTestStore(t)
	_, _, _, err := s.CreateProject(ctx, "", "   ", "wk")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("whitespace prompt should be VALIDATION_ERROR, got %v", err)
	}
	assertNoDirtyRecords(t, s.db)
}

// TestCreateProjectPromptBoundaries (VF-P0-02): empty and overlong prompts are
// rejected; a normal prompt is accepted (and seeds exactly one user message).
func TestCreateProjectPromptBoundaries(t *testing.T) {
	s, _ := newTestStore(t)

	if _, _, _, err := s.CreateProject(ctx, "", "", "k1"); !errors.Is(err, ErrValidation) {
		t.Errorf("empty prompt: want VALIDATION_ERROR, got %v", err)
	}
	if _, _, _, err := s.CreateProject(ctx, "", strings.Repeat("x", 4001), "k2"); !errors.Is(err, ErrValidation) {
		t.Errorf("overlong prompt (4001 runes): want VALIDATION_ERROR, got %v", err)
	}
	// A prompt at the max boundary is accepted.
	p := createProject(t, s, "k3", strings.Repeat("x", 4000))
	d, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 1 {
		t.Errorf("normal project should seed 1 message, got %d", len(d.Messages))
	}
}

// TestCreateRunRejectsWhitespace (VF-P0-02): a whitespace-only run prompt is
// rejected and creates no run or idempotency record; the project's initial
// message (from createProject) is the only message.
func TestCreateRunRejectsWhitespace(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")

	_, _, _, err := s.CreateRun(ctx, p.ID, "   ", "", "wk", true)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("whitespace run prompt should be VALIDATION_ERROR, got %v", err)
	}

	var nRuns, nMsg, nIdem int
	s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE project_id = ?`, p.ID).Scan(&nRuns)
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE project_id = ?`, p.ID).Scan(&nMsg)
	// Count only the rejected request's key: createProject's own 201 record (key
	// "pk") legitimately persists, but the whitespace CreateRun (key "wk") must not.
	s.db.QueryRow(`SELECT COUNT(*) FROM idempotency_records WHERE key = ?`, "wk").Scan(&nIdem)
	if nRuns != 0 || nMsg != 1 || nIdem != 0 {
		t.Errorf("whitespace run left dirty records: runs=%d messages=%d idempotency(wk)=%d", nRuns, nMsg, nIdem)
	}
}

// TestCreateRunRejectsOverlong (VF-P0-02): an overlong run prompt is rejected.
func TestCreateRunRejectsOverlong(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")
	if _, _, _, err := s.CreateRun(ctx, p.ID, strings.Repeat("x", 4001), "", "ok", true); !errors.Is(err, ErrValidation) {
		t.Errorf("overlong run prompt: want VALIDATION_ERROR, got %v", err)
	}
}

// TestCreateProjectPersistsInitialMessage (VF-P0-03): createProject persists the
// initial user message atomically, so the prompt is queryable before any run.
func TestCreateProjectPersistsInitialMessage(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build a habit tracker")

	d, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 1 {
		t.Fatalf("expected 1 initial message, got %d", len(d.Messages))
	}
	if d.Messages[0].Role != "user" || d.Messages[0].Content != "build a habit tracker" {
		t.Errorf("unexpected initial message: %+v", d.Messages[0])
	}
}

// TestCreateRunFirstRunReusesInitialMessage (VF-P0-03): the first run does not
// duplicate the initial user message; an edit run records its own.
func TestCreateRunFirstRunReusesInitialMessage(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")

	createRun(t, s, p.ID, "build an app", "", "rk1") // first run
	d, _ := s.GetProject(ctx, p.ID)
	if len(d.Messages) != 1 {
		t.Fatalf("first run duplicated message: got %d, want 1", len(d.Messages))
	}

	// Move the first run to a terminal state so a second run is allowed (single
	// active run), then run an edit which records its own message.
	if err := s.SetRunStatus(ctx, d.Runs[0].ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	createRun(t, s, p.ID, "make it blue", "", "rk2") // edit
	d, _ = s.GetProject(ctx, p.ID)
	if len(d.Messages) != 2 {
		t.Errorf("edit should add one message: got %d, want 2", len(d.Messages))
	}
}

// TestCreateRunConfigMissingRecordsFailure (VF-P0-03): when the model is not
// configured (canStart=false), CreateRun still persists the run as 'failed' with
// a run_failed event; the initial user message and the retry entry survive.
func TestCreateRunConfigMissingRecordsFailure(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")

	status, _, _, err := s.CreateRun(ctx, p.ID, "build an app", "", "rk", false)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status != 503 {
		t.Fatalf("config-missing status = %d, want 503", status)
	}

	d, _ := s.GetProject(ctx, p.ID)
	if len(d.Runs) != 1 || d.Runs[0].Status != "failed" {
		t.Fatalf("expected 1 failed run, got %+v", d.Runs)
	}
	// The initial user message survives (no duplicate, no loss).
	if len(d.Messages) != 1 || d.Messages[0].Content != "build an app" {
		t.Errorf("initial message lost/duplicated: %+v", d.Messages)
	}
	// Failure context: a run_failed event carrying DEPENDENCY_UNAVAILABLE.
	evs, err := s.ListEvents(ctx, d.Runs[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != "run_failed" {
		t.Fatalf("expected 1 run_failed event, got %+v", evs)
	}
	if code, _ := evs[0].Payload["code"].(string); code != "DEPENDENCY_UNAVAILABLE" {
		t.Errorf("failure code = %v, want DEPENDENCY_UNAVAILABLE", evs[0].Payload["code"])
	}
	if retryable, _ := evs[0].Payload["retryable"].(bool); !retryable {
		t.Error("run_failed retryable = false, want true")
	}
}

// TestCreateRunConfigMissingThenRetry (VF-P0-03): after a config-missing failed
// run, retryRun (once the model is configured) re-drives the same run with a new
// attempt - exactly one new attempt, no duplicate run or message. "Retry only
// this round".
func TestCreateRunConfigMissingThenRetry(t *testing.T) {
	s, _ := newTestStore(t)
	p := createProject(t, s, "pk", "build an app")

	_, _, _, err := s.CreateRun(ctx, p.ID, "build an app", "", "rk", false)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	d, _ := s.GetProject(ctx, p.ID)
	runID := d.Runs[0].ID

	// Retry the failed run (now treat the model as available): one new attempt.
	_, _, _, err = s.RetryRun(ctx, runID, "retry-1")
	if err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	var nAttempts int
	s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE run_id = ?`, runID).Scan(&nAttempts)
	if nAttempts != 2 {
		t.Errorf("attempts after retry = %d, want 2 (failed + 1 retry)", nAttempts)
	}
	// No duplicate run or message from the retry.
	d, _ = s.GetProject(ctx, p.ID)
	if len(d.Runs) != 1 || len(d.Messages) != 1 {
		t.Errorf("retry duplicated: runs=%d messages=%d, want 1/1", len(d.Runs), len(d.Messages))
	}
}
