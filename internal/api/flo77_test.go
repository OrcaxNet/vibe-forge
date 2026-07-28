package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// flo77_test.go locks the FLO-77 P0 through the HTTP surface: a manual
// PUT /api/projects/:id/files/src/App.tsx that fails the compile gate returns 422
// VALIDATION_ERROR with the structured errors and leaves the project's
// stableVersionId on the previous stable; a valid edit still advances it.

const flo77GoodApp = `import { useState } from "react";

export default function App() {
  const [n, setN] = useState(0);
  return <main><h1>{n}</h1></main>;
}`

// seedStableProject creates a project with one stable version (succeeded run) via
// the store and returns (projectID, stableVersionID). Seeding through the store
// avoids launching the agent loop in tests.
func seedStableProject(t *testing.T, srv *Server) (string, string) {
	t.Helper()
	_, body, _, err := srv.store.CreateProject(context.Background(), "", "build an app", "pk")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	_, rb, _, err := srv.store.CreateRun(context.Background(), p.ID, "build an app", "", "rk", true)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	var rr struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &rr)
	var iterID string
	srv.store.DB().QueryRow(`SELECT id FROM iterations WHERE run_id = ?`, rr.RunID).Scan(&iterID)

	v, err := srv.store.CommitVersion(context.Background(), store.CommitInput{
		ProjectID: p.ID, IterationID: iterID, RunID: rr.RunID,
		Files: []store.FileSnapshot{{Path: "/src/App.tsx", Content: flo77GoodApp, Readonly: false}},
	})
	if err != nil {
		t.Fatalf("CommitVersion: %v", err)
	}
	return p.ID, v.ID
}

// projectStableVersionID GETs the project detail and returns its stableVersionId.
func projectStableVersionID(t *testing.T, srv *Server, projectID string) string {
	t.Helper()
	_, db := doJSON(t, srv, "GET", "/api/projects/"+projectID, "", nil)
	var d struct {
		StableVersionID string `json:"stableVersionId"`
	}
	json.Unmarshal(db, &d)
	return d.StableVersionID
}

// TestWriteFileCompileFailureHTTP: the public FLO-77 repro over HTTP - invalid
// JSX returns 422 and stableVersionId is unchanged.
func TestWriteFileCompileFailureHTTP(t *testing.T) {
	srv := newAPITestServer(t)
	pid, stable := seedStableProject(t, srv)

	bad := `export default function App() { return <main><h1>QA compile failure</main>; }`
	status, body := doJSON(t, srv, "PUT", "/api/projects/"+pid+"/files/src/App.tsx", "wk1",
		map[string]any{"content": bad, "baseVersionId": stable, "idempotencyKey": "wk1"})
	if status != 422 {
		t.Fatalf("status = %d, want 422 (body=%s)", status, body)
	}
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Errors []struct {
				File, Message string
				Line          int
			} `json:"errors"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, body)
	}
	if e.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", e.Code)
	}
	if len(e.Details.Errors) == 0 {
		t.Errorf("expected compile errors in details, got none (message=%q)", e.Message)
	}
	if got := projectStableVersionID(t, srv, pid); got != stable {
		t.Fatalf("stableVersionId = %q, want %q (must not advance on compile failure)", got, stable)
	}
}

// TestWriteFileCompileFailureClassesHTTP: each FLO-77 acceptance-3 failure class
// returns 422 and keeps stableVersionId unchanged.
func TestWriteFileCompileFailureClassesHTTP(t *testing.T) {
	srv := newAPITestServer(t)
	pid, stable := seedStableProject(t, srv)

	cases := map[string]string{
		"invalid JSX":          `export default function App() { return <main><h1>x</main>; }`,
		"missing default exp":  `const App = () => <main><div>hi</div></main>;`,
		"unclosed bracket":     `export default function App() { return <main>x</main>; `,
		"forbidden token":      `export default function App() { void process.env.X; return <main />; }`,
	}
	i := 0
	for name, content := range cases {
		i++
		status, body := doJSON(t, srv, "PUT", "/api/projects/"+pid+"/files/src/App.tsx", "",
			map[string]any{"content": content, "baseVersionId": stable, "idempotencyKey": name})
		if status != 422 {
			t.Errorf("%s: status = %d, want 422 (body=%s)", name, status, body)
		}
		if got := projectStableVersionID(t, srv, pid); got != stable {
			t.Errorf("%s: stableVersionId = %q, want %q", name, got, stable)
		}
	}
}

// TestWriteFileCompilePassAdvancesHTTP: a valid edit returns 202 and advances
// stableVersionId to the new version (happy path unchanged).
func TestWriteFileCompilePassAdvancesHTTP(t *testing.T) {
	srv := newAPITestServer(t)
	pid, stable := seedStableProject(t, srv)

	edited := `export default function App() { return <main><h1>v2</h1></main>; }`
	status, body := doJSON(t, srv, "PUT", "/api/projects/"+pid+"/files/src/App.tsx", "wk-ok",
		map[string]any{"content": edited, "baseVersionId": stable, "idempotencyKey": "wk-ok"})
	if status != 202 {
		t.Fatalf("status = %d, want 202 (body=%s)", status, body)
	}
	var iter struct {
		ResultVersionID string `json:"resultVersionId"`
	}
	json.Unmarshal(body, &iter)
	if iter.ResultVersionID == "" || iter.ResultVersionID == stable {
		t.Fatalf("resultVersionId = %q, want a new stable version", iter.ResultVersionID)
	}
	if got := projectStableVersionID(t, srv, pid); got != iter.ResultVersionID {
		t.Fatalf("stableVersionId = %q, want %q", got, iter.ResultVersionID)
	}
}

// TestWriteFileCompileFailureThenCorrectSucceedsHTTP (acceptance 4): after a
// failed save, correcting the code and saving again succeeds and advances
// stableVersionId - the retry/correct entry works.
func TestWriteFileCompileFailureThenCorrectSucceedsHTTP(t *testing.T) {
	srv := newAPITestServer(t)
	pid, stable := seedStableProject(t, srv)

	// First save: invalid -> 422, stable unchanged.
	bad := `export default function App() { return <main><h1>x</main>; }`
	if status, _ := doJSON(t, srv, "PUT", "/api/projects/"+pid+"/files/src/App.tsx", "wk-bad",
		map[string]any{"content": bad, "baseVersionId": stable, "idempotencyKey": "wk-bad"}); status != 422 {
		t.Fatalf("first (bad) save status = %d, want 422", status)
	}
	if got := projectStableVersionID(t, srv, pid); got != stable {
		t.Fatalf("after bad save stableVersionId = %q, want %q", got, stable)
	}

	// Second save: corrected -> 202, stable advances.
	good := `export default function App() { return <main><h1>fixed</h1></main>; }`
	status, body := doJSON(t, srv, "PUT", "/api/projects/"+pid+"/files/src/App.tsx", "wk-good",
		map[string]any{"content": good, "baseVersionId": stable, "idempotencyKey": "wk-good"})
	if status != 202 {
		t.Fatalf("corrected save status = %d, want 202 (body=%s)", status, body)
	}
	var iter struct {
		ResultVersionID string `json:"resultVersionId"`
	}
	json.Unmarshal(body, &iter)
	if got := projectStableVersionID(t, srv, pid); got != iter.ResultVersionID {
		t.Fatalf("after corrected save stableVersionId = %q, want %q", got, iter.ResultVersionID)
	}
}
