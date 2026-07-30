package contracts

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestContractParses guards against a malformed contract.json slipping through
// the embed; the server would panic at startup otherwise.
func TestContractParses(t *testing.T) {
	c := Load()
	if c.Version == "" {
		t.Fatal("contract version is empty")
	}
	if len(c.Stages.Order) != 4 {
		t.Fatalf("expected 4 stages, got %d", len(c.Stages.Order))
	}
	wantStages := []string{"pm", "architect", "engineer", "qa"}
	for i, s := range c.Stages.Order {
		if s != wantStages[i] {
			t.Fatalf("stage %d = %q, want %q", i, s, wantStages[i])
		}
	}
}

// TestRequiredStatesPresent asserts acceptance criterion 4: Project/Run/Version
// states are defined.
func TestRequiredStatesPresent(t *testing.T) {
	c := Load()
	for _, name := range []string{"project", "run", "version", "stageNode", "workflowProject", "workflowStage"} {
		e, ok := c.States[name]
		if !ok {
			t.Fatalf("missing state enum %q", name)
		}
		if len(e.Values) == 0 {
			t.Fatalf("state enum %q has no values", name)
		}
	}
	if !contains(c.States["project"].Values, "archived") {
		t.Error("project state missing archived")
	}
	if !contains(c.States["run"].Values, "interrupted") {
		t.Error("run state missing interrupted")
	}
	if !contains(c.States["version"].Values, "stable") {
		t.Error("version state missing stable")
	}
	if !contains(c.States["workflowProject"].Values, "recovering") {
		t.Error("workflow project state missing recovering")
	}
	if !contains(c.States["workflowStage"].Values, "cancelled") {
		t.Error("workflow stage state missing cancelled")
	}
}

// TestUnifiedEventNames asserts the 8 unified SSE events exist by name.
func TestUnifiedEventNames(t *testing.T) {
	c := Load()
	want := []string{
		"run_started", "stage_started", "stage_artifact", "message_delta",
		"file_written", "preview_ready", "run_failed", "run_completed",
	}
	for _, name := range want {
		if _, ok := c.Events.Definitions[name]; !ok {
			t.Errorf("missing event %q", name)
		}
	}
	if c.Events.Protocol.ReplayHeader != "Last-Event-ID" {
		t.Errorf("replay header = %q, want Last-Event-ID", c.Events.Protocol.ReplayHeader)
	}
}

// TestIdempotencyAndErrors asserts the idempotency key and stable error
// structure sections exist.
func TestIdempotencyAndErrors(t *testing.T) {
	c := Load()
	if c.Idempotency.Header != "Idempotency-Key" {
		t.Errorf("idempotency header = %q", c.Idempotency.Header)
	}
	if len(c.Idempotency.AppliesTo) == 0 {
		t.Error("idempotency appliesTo is empty")
	}
	for _, code := range []string{"VALIDATION_ERROR", "NOT_FOUND", "CONFLICT", "DEPENDENCY_UNAVAILABLE"} {
		if _, ok := c.Errors.Codes[code]; !ok {
			t.Errorf("missing error code %q", code)
		}
	}
	if _, ok := c.Errors.Structure["code"]; !ok {
		t.Error("error structure missing code field")
	}
}

func TestAuthenticationPathsPresent(t *testing.T) {
	c := Load()
	want := map[string]string{
		"authLogin":   "/api/auth/login",
		"authSession": "/api/auth/session",
		"authLogout":  "/api/auth/logout",
	}
	for name, path := range want {
		got, ok := c.Paths[name]
		if !ok {
			t.Fatalf("missing authentication path %q", name)
		}
		if got.Path != path {
			t.Errorf("%s path = %q, want %q", name, got.Path, path)
		}
	}
	for _, code := range []string{
		"AUTH_PASSWORD_REQUIRED",
		"AUTH_REQUIRED",
		"AUTH_INVALID",
		"AUTH_RATE_LIMITED",
		"AUTH_UNAVAILABLE",
	} {
		if _, ok := c.Errors.Codes[code]; !ok {
			t.Errorf("missing authentication error code %q", code)
		}
	}
}

// TestRawIsCanonicalJSON re-parses Raw() to ensure the accessor returns valid,
// canonical JSON identical to what the frontend imports.
func TestRawIsCanonicalJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(Raw(), &v); err != nil {
		t.Fatalf("Raw() is not valid JSON: %v", err)
	}
	if !strings.Contains(string(Raw()), "/src/App.tsx") {
		t.Error("contract does not pin /src/App.tsx as writable file")
	}
}

// TestHTTPStatusFor covers the helper used by the API layer.
func TestHTTPStatusFor(t *testing.T) {
	cases := map[string]int{
		"VALIDATION_ERROR":       422,
		"NOT_FOUND":              404,
		"CONFLICT":               409,
		"RATE_LIMITED":           429,
		"DEPENDENCY_UNAVAILABLE": 503,
		"INTERNAL":               500,
		"UNKNOWN_CODE":           500,
	}
	for code, want := range cases {
		if got := HTTPStatusFor(code); got != want {
			t.Errorf("HTTPStatusFor(%q) = %d, want %d", code, got, want)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
