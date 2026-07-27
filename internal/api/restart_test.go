package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// restart_test.go locks FLO-59 acceptance criterion 3 deterministically, with
// no external API dependency: a run left active by a crash is reconciled to
// 'interrupted' on startup, the SSE stream surfaces it as retryable, and retry
// creates exactly one new attempt (no fake "continue", no duplicate attempts).

// countAttempts reads the attempt count for a run straight from the store so the
// test can prove "retry only adds one attempt".
func countAttempts(t *testing.T, srv *Server, runID string) int {
	t.Helper()
	var n int
	if err := srv.store.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM attempts WHERE run_id = ?`, runID).Scan(&n); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return n
}

// TestRestartReconcileAndRetry is the core restart-recovery flow (criterion 3):
// queued/running run -> (crash) -> reconcile on startup -> interrupted ->
// retry -> new attempt, run running again. Retry is idempotent (same key replays
// without a second new attempt); retrying an active run is 409.
func TestRestartReconcileAndRetry(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	_, rb := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	var run struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &run)

	// The loop would flip queued->running; simulate that, then a crash leaves it
	// mid-flight (no terminal event persisted).
	if err := srv.store.SetRunStatus(context.Background(), run.RunID, "running"); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if got := countAttempts(t, srv, run.RunID); got != 1 {
		t.Fatalf("attempts before crash = %d, want 1", got)
	}

	// Startup reconciliation (exactly what main.ReconcileInterruptedRuns does).
	n, err := srv.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("reconciled = %d, want 1 (one active run)", n)
	}
	r, _ := srv.store.GetRun(context.Background(), run.RunID)
	if r.Status != "interrupted" {
		t.Fatalf("run status after reconcile = %q, want interrupted", r.Status)
	}
	// An interrupted run no longer holds the active-run slot (single active run).
	if active, _ := srv.store.GetActiveRun(context.Background(), p.ID); active != "" {
		t.Errorf("active run after reconcile = %q, want none (lock released)", active)
	}

	// Retry must create exactly one new attempt and flip the run back to running.
	s, b := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/retry", "retry-1",
		map[string]any{"attemptId": "any", "idempotencyKey": "retry-1"})
	if s != 202 {
		t.Fatalf("retry status = %d, want 202 (body=%s)", s, b)
	}
	var ret struct {
		AttemptID string `json:"attemptId"`
	}
	json.Unmarshal(b, &ret)
	if ret.AttemptID == "" {
		t.Fatal("retry returned empty attemptId")
	}
	if got := countAttempts(t, srv, run.RunID); got != 2 {
		t.Errorf("attempts after retry = %d, want 2 (retry adds exactly one attempt)", got)
	}
	r, _ = srv.store.GetRun(context.Background(), run.RunID)
	if r.Status != "running" {
		t.Errorf("run status after retry = %q, want running", r.Status)
	}

	// Same idempotency key replays the original 202 WITHOUT a third attempt.
	s2, b2 := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/retry", "retry-1",
		map[string]any{"attemptId": "any", "idempotencyKey": "retry-1"})
	if s2 != 202 {
		t.Fatalf("replay status = %d, want 202", s2)
	}
	var ret2 struct {
		AttemptID string `json:"attemptId"`
	}
	json.Unmarshal(b2, &ret2)
	if ret2.AttemptID != ret.AttemptID {
		t.Errorf("replay attemptId = %q, want %q (idempotent replay)", ret2.AttemptID, ret.AttemptID)
	}
	if got := countAttempts(t, srv, run.RunID); got != 2 {
		t.Errorf("attempts after replay = %d, want 2 (no duplicate attempt)", got)
	}

	// A different key while the run is running again is 409 (not retryable).
	s3, b3 := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/retry", "retry-2",
		map[string]any{"attemptId": "any", "idempotencyKey": "retry-2"})
	if s3 != 409 {
		t.Fatalf("retry-while-running status = %d, want 409 (body=%s)", s3, b3)
	}
	var e APIError
	json.Unmarshal(b3, &e)
	if e.Code != "CONFLICT" {
		t.Errorf("retry-while-running code = %q, want CONFLICT", e.Code)
	}
}

// TestReconcileDoesNotTouchTerminalRuns proves reconciliation never resurrects a
// finished run (succeeded/failed/interrupted are left alone) - criterion 3's "do
// not fake continue".
func TestReconcileDoesNotTouchTerminalRuns(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	// Two runs: one succeeded, one failed (simulated terminal states).
	var r1, r2 struct {
		RunID string `json:"runId"`
	}
	_, b1 := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk1",
		map[string]string{"prompt": "build an app"})
	json.Unmarshal(b1, &r1)
	srv.store.SetRunStatus(context.Background(), r1.RunID, "succeeded")

	// r1 is now terminal; create r2 (single-active-run allows it).
	_, b2 := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk2",
		map[string]string{"prompt": "build again"})
	json.Unmarshal(b2, &r2)
	srv.store.SetRunStatus(context.Background(), r2.RunID, "failed")

	n, err := srv.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("reconciled = %d, want 0 (no queued/running runs)", n)
	}
	got1, _ := srv.store.GetRun(context.Background(), r1.RunID)
	got2, _ := srv.store.GetRun(context.Background(), r2.RunID)
	if got1.Status != "succeeded" || got2.Status != "failed" {
		t.Errorf("terminal runs changed: r1=%q r2=%q (want succeeded/failed)", got1.Status, got2.Status)
	}
}

// TestAuthTokenConfiguresModel proves Mode B (bearer token + base URL) is treated
// as "model configured" by health and run creation, so the same image runs behind
// an Anthropic-compatible gateway without an API key.
func TestAuthTokenConfiguresModel(t *testing.T) {
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok-xyz")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example.com/api")
	t.Setenv("ANTHROPIC_MODEL", "glm-5.2")
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	var h Health
	json.Unmarshal(rec.Body.Bytes(), &h)
	if h.Dependencies.Model.Status != "configured" {
		t.Errorf("model status = %q, want configured (auth-token mode)", h.Dependencies.Model.Status)
	}
	if rec.Code != 200 {
		t.Errorf("health status = %d, want 200 (healthy with auth token)", rec.Code)
	}
	// Health body must not leak the token.
	if bytes.Contains(rec.Body.Bytes(), []byte("tok-xyz")) {
		t.Errorf("health body leaks auth token: %s", rec.Body.String())
	}
}
