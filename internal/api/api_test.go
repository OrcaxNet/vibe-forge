package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAPITestServer returns a server backed by an in-memory SQLite DB and a
// configured model key. It clears the bearer-token env vars so the suite is
// hermetic against a runner that exports ANTHROPIC_AUTH_TOKEN/BASE_URL.
func newAPITestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "claude-sonnet-5")
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// doJSON issues a request with an optional Idempotency-Key header and JSON body.
func doJSON(t *testing.T, srv *Server, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestHealthNotReadyStructurallyCorrect is the backend smoke: with no
// DATABASE_PATH and no model credentials, GET /api/health returns 503 with a
// structurally correct, sanitized body (Stage 1 acceptance criterion 3).
func TestHealthNotReadyStructurallyCorrect(t *testing.T) {
	// Force the "nothing configured" state regardless of the ambient runtime env
	// (a Claude Code runner exports ANTHROPIC_AUTH_TOKEN/BASE_URL); the test must
	// prove the not-ready body, not the runner's credentials.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("DATABASE_PATH", "")
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	if h.Status != "unhealthy" {
		t.Errorf("status = %q, want unhealthy", h.Status)
	}
	if h.ContractVersion == "" {
		t.Error("contractVersion empty; health must reference the shared contract")
	}
	if h.Dependencies.Database.Status != "not_configured" {
		t.Errorf("database status = %q, want not_configured", h.Dependencies.Database.Status)
	}
	if h.Dependencies.Model.Status != "not_configured" {
		t.Errorf("model status = %q, want not_configured", h.Dependencies.Model.Status)
	}
	body := rec.Body.String()
	for _, secret := range []string{"ANTHROPIC_API_KEY", "sk-", "gho_", "DATABASE_PATH"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
			t.Errorf("health body leaks %q: %s", secret, body)
		}
	}
}

// TestHealthReadyWithDeps proves the structure flips to 200 healthy once the
// dependencies are configured.
func TestHealthReadyWithDeps(t *testing.T) {
	srv := newAPITestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var h Health
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	if h.Status != "healthy" {
		t.Errorf("status = %q, want healthy", h.Status)
	}
}

// TestCompileResultRecordsArtifact (FLO-60): POST /api/runs/:id/compile-result
// is a real idempotent recorder now (no longer a 501 stub). A valid body for an
// existing attempt returns 200 and binds a qa compile_result artifact; an empty
// body returns 422 VALIDATION_ERROR.
func TestCompileResultRecordsArtifact(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	s, rb := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	if s != 202 {
		t.Fatalf("create run status = %d, want 202 (body=%s)", s, rb)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &run)

	// Empty body -> 422 VALIDATION_ERROR.
	es, _ := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/compile-result", "", nil)
	if es != 422 {
		t.Fatalf("empty compile-result status = %d, want 422", es)
	}

	// Valid body for attempt 1 -> 200 and a qa artifact on the project.
	cs, _ := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/compile-result", "",
		map[string]any{
			"attempt":   1,
			"errors":    []map[string]any{{"file": "/src/App.tsx", "line": 3, "message": "missing )"}},
			"filesHash": "abc",
		})
	if cs != 200 {
		t.Fatalf("compile-result status = %d, want 200", cs)
	}

	// Repeating the same body is idempotent (still 200, no duplicate artifact).
	doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/compile-result", "",
		map[string]any{"attempt": 1, "errors": []map[string]any{{"file": "/src/App.tsx", "line": 3, "message": "missing )"}}, "filesHash": "abc"})

	ds, db := doJSON(t, srv, "GET", "/api/projects/"+p.ID, "", nil)
	if ds != 200 {
		t.Fatalf("get project status = %d, want 200 (body=%s)", ds, db)
	}
	var detail struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	json.Unmarshal(db, &detail)
	qaCount := 0
	for _, a := range detail.Artifacts {
		if a["stage"] == "qa" && a["artifactType"] == "compile_result" {
			qaCount++
		}
	}
	if qaCount != 1 {
		t.Errorf("qa compile_result artifacts = %d, want 1 (idempotent)", qaCount)
	}
}

// TestCreateProjectAndList (criterion 2/5): create a project via the API, then
// list and fetch it back - the server is the source of truth, no browser needed.
func TestCreateProjectAndList(t *testing.T) {
	srv := newAPITestServer(t)

	status, body := doJSON(t, srv, "POST", "/api/projects", "key-1",
		map[string]string{"initialPrompt": "build a habit tracker"})
	if status != 201 {
		t.Fatalf("create status = %d, want 201 (body=%s)", status, body)
	}
	var p struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "active" || p.Title == "" {
		t.Errorf("unexpected project: %+v", p)
	}

	// List returns the project.
	status, body = doJSON(t, srv, "GET", "/api/projects", "", nil)
	if status != 200 {
		t.Fatalf("list status = %d, want 200", status)
	}
	var ps []map[string]any
	json.Unmarshal(body, &ps)
	if len(ps) != 1 {
		t.Errorf("expected 1 project, got %d", len(ps))
	}

	// Detail returns the same project.
	status, body = doJSON(t, srv, "GET", "/api/projects/"+p.ID, "", nil)
	if status != 200 {
		t.Fatalf("detail status = %d, want 200 (body=%s)", status, body)
	}
}

// TestCreateProjectIdempotentAPI (criterion 2): same Idempotency-Key replays the
// original 201 without creating a duplicate.
func TestCreateProjectIdempotentAPI(t *testing.T) {
	srv := newAPITestServer(t)
	s1, b1 := doJSON(t, srv, "POST", "/api/projects", "dup-key",
		map[string]string{"initialPrompt": "build X"})
	s2, b2 := doJSON(t, srv, "POST", "/api/projects", "dup-key",
		map[string]string{"initialPrompt": "build X"})
	if s1 != 201 || s2 != 201 {
		t.Fatalf("statuses = %d,%d, want 201,201", s1, s2)
	}
	if !bytes.Equal(b1, b2) {
		t.Error("replayed response body differs from original")
	}
	_, list := doJSON(t, srv, "GET", "/api/projects", "", nil)
	var ps []map[string]any
	json.Unmarshal(list, &ps)
	if len(ps) != 1 {
		t.Errorf("expected 1 project after idempotent replay, got %d", len(ps))
	}
}

// TestCreateRunConflictAPI (criterion 2): a second active run returns 409.
func TestCreateRunConflictAPI(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	s1, _ := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk1",
		map[string]string{"prompt": "build an app"})
	if s1 != 202 {
		t.Fatalf("first run status = %d, want 202", s1)
	}
	s2, b2 := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk2",
		map[string]string{"prompt": "build again"})
	if s2 != 409 {
		t.Fatalf("second run status = %d, want 409 (body=%s)", s2, b2)
	}
	var e APIError
	json.Unmarshal(b2, &e)
	if e.Code != "CONFLICT" {
		t.Errorf("error code = %q, want CONFLICT", e.Code)
	}
}

// TestGetProjectNotFoundAPI (C-FR-02): a nonexistent project returns 404 with the
// stable error shape and does not leak data.
func TestGetProjectNotFoundAPI(t *testing.T) {
	srv := newAPITestServer(t)
	status, body := doJSON(t, srv, "GET", "/api/projects/does-not-exist", "", nil)
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
	var e APIError
	json.Unmarshal(body, &e)
	if e.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", e.Code)
	}
}

// TestErrorsAreSanitized (C-NFR-03): error bodies never leak the API key, a stack
// trace or internal paths.
func TestErrorsAreSanitized(t *testing.T) {
	srv := newAPITestServer(t)
	// 404 body must not leak secrets.
	_, body := doJSON(t, srv, "GET", "/api/projects/nope", "", nil)
	if bytes.Contains(body, []byte("test-key")) {
		t.Errorf("404 body leaks API key: %s", body)
	}
	// Validation error from missing idempotency key.
	_, body = doJSON(t, srv, "POST", "/api/projects", "",
		map[string]string{"initialPrompt": "build X"})
	if bytes.Contains(body, []byte("test-key")) || bytes.Contains(body, []byte(".go:")) {
		t.Errorf("error body not sanitized: %s", body)
	}
}

// TestMutatingEndpointRequiresKey: omitting the Idempotency-Key is a 422.
func TestMutatingEndpointRequiresKey(t *testing.T) {
	srv := newAPITestServer(t)
	status, _ := doJSON(t, srv, "POST", "/api/projects", "",
		map[string]string{"initialPrompt": "build X"})
	if status != 422 {
		t.Fatalf("status = %d, want 422", status)
	}
}
