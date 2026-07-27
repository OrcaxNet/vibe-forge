package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// p0_test.go locks the two FLO-55 P0 regressions (VF-P0-02 / VF-P0-03) through
// the HTTP surface, end to end with the SQLite store.

// newAPITestServerNoModel returns a server with SQLite configured but NO model,
// so createRun records a startup failure (503) instead of launching the loop.
func newAPITestServerNoModel(t *testing.T) *Server {
	t.Helper()
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestCreateProjectWhitespaceAPI (VF-P0-02): a whitespace-only initialPrompt
// returns 422 VALIDATION_ERROR and creates no project.
func TestCreateProjectWhitespaceAPI(t *testing.T) {
	srv := newAPITestServer(t)
	status, body := doJSON(t, srv, "POST", "/api/projects", "wk",
		map[string]string{"initialPrompt": "   "})
	if status != 422 {
		t.Fatalf("status = %d, want 422 (body=%s)", status, body)
	}
	var e APIError
	json.Unmarshal(body, &e)
	if e.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", e.Code)
	}
	_, list := doJSON(t, srv, "GET", "/api/projects", "", nil)
	var ps []map[string]any
	json.Unmarshal(list, &ps)
	if len(ps) != 0 {
		t.Errorf("whitespace created %d projects, want 0", len(ps))
	}
}

// TestCreateProjectOverlongAPI (VF-P0-02): an overlong initialPrompt is rejected.
func TestCreateProjectOverlongAPI(t *testing.T) {
	srv := newAPITestServer(t)
	status, _ := doJSON(t, srv, "POST", "/api/projects", "ok",
		map[string]string{"initialPrompt": strings.Repeat("x", 4001)})
	if status != 422 {
		t.Errorf("overlong status = %d, want 422", status)
	}
}

// TestRunStartupFailurePreservesPrompt (VF-P0-03): project created, run creation
// 503s (model not configured); after refresh the initial user message, the
// failed run, and the retry entry are all queryable, with no duplicate message.
func TestRunStartupFailurePreservesPrompt(t *testing.T) {
	srv := newAPITestServerNoModel(t)

	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	// Run creation 503s (model not configured) but records a failed run.
	rs, _ := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	if rs != 503 {
		t.Fatalf("run status = %d, want 503", rs)
	}

	// Refresh: initial message + failed run are queryable.
	ds, db := doJSON(t, srv, "GET", "/api/projects/"+p.ID, "", nil)
	if ds != 200 {
		t.Fatalf("get project: %d (body=%s)", ds, db)
	}
	var detail struct {
		Messages []struct {
			Role, Content string
		} `json:"messages"`
		Runs []struct {
			ID, Status string
		} `json:"runs"`
	}
	json.Unmarshal(db, &detail)
	if len(detail.Messages) != 1 || detail.Messages[0].Content != "build an app" {
		t.Errorf("initial message lost/duplicated: %+v", detail.Messages)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].Status != "failed" {
		t.Errorf("expected 1 failed run, got %+v", detail.Runs)
	}
	runID := detail.Runs[0].ID

	// "Retry only this round" entry: retryRun on the failed run. Model still not
	// configured -> 503, and it must NOT create a duplicate run or message.
	rs2, _ := doJSON(t, srv, "POST", "/api/runs/"+runID+"/retry", "retry-1",
		map[string]string{"idempotencyKey": "retry-1"})
	if rs2 != 503 {
		t.Errorf("retry status = %d, want 503 (model still not configured)", rs2)
	}
	_, db2 := doJSON(t, srv, "GET", "/api/projects/"+p.ID, "", nil)
	var d2 struct {
		Messages []struct{} `json:"messages"`
		Runs     []struct{} `json:"runs"`
	}
	json.Unmarshal(db2, &d2)
	if len(d2.Messages) != 1 {
		t.Errorf("retry duplicated message: got %d, want 1", len(d2.Messages))
	}
	if len(d2.Runs) != 1 {
		t.Errorf("retry duplicated run: got %d, want 1", len(d2.Runs))
	}
}
