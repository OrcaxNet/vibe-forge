package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// flo77_test.go locks the FLO-77 P0: a manual "保存并编译" that fails the compile
// gate must NOT advance stableVersionId. The new version is recorded as 'failed',
// the previous stable stays authoritative, and the save returns 422 VALIDATION_ERROR
// with the structured compile errors. A passing edit still advances stable as
// before. The gate is the same one the agent loop runs (internal/compile).

// goodApp is a structurally valid App.tsx that passes the compile gate.
const goodApp = `import { useState } from "react";

export default function App() {
  const [n, setN] = useState(0);
  return (
    <main>
      <h1>{n}</h1>
      <button onClick={() => setN(n + 1)}>inc</button>
    </main>
  );
}`

// seedStable creates a project with one stable version (and a succeeded run) and
// returns the project and its stable version id. The run is terminal so the
// manual/agent mutex does not block a subsequent WriteFile.
func seedStable(t *testing.T, s *Store) (Project, string) {
	t.Helper()
	p := createProject(t, s, "pk", "build an app")
	runID, iterID := createRun(t, s, p.ID, "build an app", "", "rk1")
	v := commitStable(t, s, p.ID, iterID, runID, []FileSnapshot{
		{Path: "/src/App.tsx", Content: goodApp, Readonly: false},
	})
	return p, v.ID
}

// TestWriteFileCompileFailureKeepsStable is the core FLO-77 regression: the exact
// public repro (unterminated JSX) returns 422 and leaves stableVersionId on the
// previous stable instead of promoting the invalid version.
func TestWriteFileCompileFailureKeepsStable(t *testing.T) {
	s, _ := newTestStore(t)
	p, stable := seedStable(t, s)

	bad := `export default function App() { return <main><h1>QA compile failure</main>; }`
	status, body, _, err := s.WriteFile(ctx, p.ID, bad, stable, "wk1")
	if err != nil {
		t.Fatalf("WriteFile err = %v (a compile fail returns a 422 body, not an error)", err)
	}
	if status != 422 {
		t.Fatalf("status = %d, want 422 (body=%s)", status, body)
	}
	if got := stableVersionID(t, s, p.ID); got != stable {
		t.Fatalf("stableVersionId advanced to %q, want %q (compile failure must not promote)", got, stable)
	}

	// The 422 body is a VALIDATION_ERROR carrying the structured compile errors.
	var resp struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
		Details   struct {
			Errors []struct {
				File, Message string
				Line          int
			} `json:"errors"`
			CompilePass bool `json:"compilePass"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode 422 body: %v (body=%s)", err, body)
	}
	if resp.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", resp.Code)
	}
	if resp.Retryable {
		t.Error("retryable = true, want false (a compile fail is not transient)")
	}
	if resp.Details.CompilePass {
		t.Error("details.compilePass = true, want false")
	}
	if len(resp.Details.Errors) == 0 {
		t.Errorf("expected structured compile errors in details, got none (message=%q)", resp.Message)
	}
	if resp.Message == "" {
		t.Error("expected a human-displayable message, got empty")
	}
}

// TestWriteFileCompileFailureCases covers the four FLO-77 acceptance-3 failure
// classes: invalid JSX, missing default export, unclosed bracket, forbidden token.
// Each must 422, leave stableVersionId unchanged, and record a failed version.
func TestWriteFileCompileFailureCases(t *testing.T) {
	s, _ := newTestStore(t)
	p, stable := seedStable(t, s)

	cases := []struct {
		name    string
		content string
	}{
		{"invalid JSX", `export default function App() { return <main><h1>x</main>; }`},
		{"missing default export", `const App = () => <main><div>hi</div></main>;`},
		{"unclosed bracket", `export default function App() { return <main>x</main>; `},
		{"forbidden token", `export default function App() { void process.env.X; return <main />; }`},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body, _, err := s.WriteFile(ctx, p.ID, c.content, stable, fmt.Sprintf("wk-%d", i))
			if err != nil {
				t.Fatalf("WriteFile err = %v", err)
			}
			if status != 422 {
				t.Fatalf("status = %d, want 422 (body=%s)", status, body)
			}
			if got := stableVersionID(t, s, p.ID); got != stable {
				t.Fatalf("stableVersionId advanced to %q, want %q", got, stable)
			}
			var nFailed int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE project_id = ? AND status = 'failed'`, p.ID).Scan(&nFailed); err != nil {
				t.Fatalf("count failed versions: %v", err)
			}
			if nFailed != i+1 {
				t.Errorf("failed versions = %d, want %d (one per failed save)", nFailed, i+1)
			}
		})
	}
}

// TestWriteFileCompilePassAdvancesStable: a valid edit still promotes to stable
// and CAS-switches stableVersionId (the happy path is unchanged).
func TestWriteFileCompilePassAdvancesStable(t *testing.T) {
	s, _ := newTestStore(t)
	p, stable := seedStable(t, s)

	edited := `import { useState } from "react";
export default function App() {
  const [n, setN] = useState(7);
  return <main><h1>{n}</h1></main>;
}`
	status, body, replayed, err := s.WriteFile(ctx, p.ID, edited, stable, "wk-ok")
	if err != nil {
		t.Fatalf("WriteFile err = %v", err)
	}
	if status != 202 {
		t.Fatalf("status = %d, want 202 (body=%s)", status, body)
	}
	if replayed {
		t.Error("replayed = true on first save, want false")
	}
	var iter struct {
		ResultVersionID string `json:"resultVersionId"`
	}
	if err := json.Unmarshal(body, &iter); err != nil {
		t.Fatalf("decode iteration: %v (body=%s)", err, body)
	}
	if iter.ResultVersionID == "" || iter.ResultVersionID == stable {
		t.Fatalf("resultVersionId = %q, want a new stable version", iter.ResultVersionID)
	}
	if got := stableVersionID(t, s, p.ID); got != iter.ResultVersionID {
		t.Fatalf("stableVersionId = %q, want %q (should advance to the new stable)", got, iter.ResultVersionID)
	}
	// The new version is stable; the previous stable is still restorable.
	var status_ string
	if err := s.db.QueryRow(`SELECT status FROM versions WHERE id = ?`, iter.ResultVersionID).Scan(&status_); err != nil {
		t.Fatalf("load new version: %v", err)
	}
	if status_ != "stable" {
		t.Errorf("new version status = %q, want stable", status_)
	}
}

// TestWriteFilePassIsIdempotent: repeating the same idempotency key for a passing
// edit replays the original 202 and creates no duplicate version.
func TestWriteFilePassIsIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	p, stable := seedStable(t, s)

	edited := `export default function App() { return <main><h1>v2</h1></main>; }`
	_, body1, replayed1, err := s.WriteFile(ctx, p.ID, edited, stable, "wk-idem")
	if err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	_, body2, replayed2, err := s.WriteFile(ctx, p.ID, edited, stable, "wk-idem")
	if err != nil {
		t.Fatalf("replay WriteFile: %v", err)
	}
	if !replayed2 {
		t.Error("second save with same key: replayed = false, want true")
	}
	if string(body1) != string(body2) {
		t.Errorf("replay body differs from original")
	}
	if replayed1 == replayed2 {
		t.Error("expected replayed flag to differ between first and replay")
	}
	var nVersions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM versions WHERE project_id = ?`, p.ID).Scan(&nVersions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	// One seeded stable + one promoted by the edit; the replay must not add a third.
	if nVersions != 2 {
		t.Errorf("versions after replay = %d, want 2 (no duplicate)", nVersions)
	}
}

// TestWriteFileCompileFailureVersionNotRestorable: a failed manual version is
// recorded for history but cannot be restored (only stable versions can).
func TestWriteFileCompileFailureVersionNotRestorable(t *testing.T) {
	s, _ := newTestStore(t)
	p, stable := seedStable(t, s)

	bad := `export default function App() { return <main><h1>x</main>; }`
	_, _, _, err := s.WriteFile(ctx, p.ID, bad, stable, "wk-fail")
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var failedID string
	if err := s.db.QueryRow(`SELECT id FROM versions WHERE project_id = ? AND status = 'failed'`, p.ID).Scan(&failedID); err != nil {
		t.Fatalf("find failed version: %v", err)
	}
	_, _, _, err = s.RestoreVersion(ctx, p.ID, failedID, "restore-k")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RestoreVersion of a failed version: err = %v, want ErrConflict", err)
	}
	if got := stableVersionID(t, s, p.ID); got != stable {
		t.Errorf("stableVersionId = %q, want %q after refused restore", got, stable)
	}
}
