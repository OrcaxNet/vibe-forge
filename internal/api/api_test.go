package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthNotReadyStructurallyCorrect is the backend smoke: with no
// DATABASE_PATH and no ANTHROPIC_API_KEY, GET /api/health returns 503 with a
// structurally correct, sanitized body (acceptance criterion 3).
func TestHealthNotReadyStructurallyCorrect(t *testing.T) {
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
	// Sanitized: no raw env paths/keys leak into the body.
	body := rec.Body.String()
	for _, secret := range []string{"ANTHROPIC_API_KEY", "sk-", "gho_", "DATABASE_PATH"} {
		if contains(body, secret) {
			t.Errorf("health body leaks %q: %s", secret, body)
		}
	}
}

// TestHealthReadyWithDeps proves the structure flips to 200 healthy once the
// dependencies are configured, so the "not ready" state is configuration-driven
// rather than hardcoded.
func TestHealthReadyWithDeps(t *testing.T) {
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-sonnet-5")
	srv, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

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

// TestStubReturnsStableError verifies a contract path stub responds with the
// stable error structure (contract §errors.structure).
func TestStubReturnsStableError(t *testing.T) {
	srv, _ := New(context.Background())
	defer srv.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/projects", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	var e APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Code == "" || e.Message == "" {
		t.Errorf("error missing code/message: %+v", e)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
