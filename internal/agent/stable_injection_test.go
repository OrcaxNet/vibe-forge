package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/db"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// TestLoopCompileFailurePreservesStableVersion is the FLO-58 放行条件 3
// regression: for a project that already has a stable version, an injected
// compile failure (QA rejects the Engineer's output 3×) must NOT change the
// project's stableVersion and must NOT alter the old stable preview's files.
// The failed draft is recorded separately and never promoted.
func TestLoopCompileFailurePreservesStableVersion(t *testing.T) {
	ctx := context.Background()
	dbase, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	if err := db.Migrate(dbase); err != nil {
		t.Fatal(err)
	}
	st := store.New(dbase)

	_, pbody, _, err := st.CreateProject(ctx, "Test", "build a habit tracker", "pk")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(pbody, &p)

	newLoop := func(fake streamCall) *Loop {
		return &Loop{
			store:  st,
			model:  "fake-model",
			now:    func() time.Time { return time.Now().UTC() },
			logf:   func(string, ...any) {},
			stream: fake,
		}
	}

	// Run 1: succeeds and promotes a stable version V1.
	goodFake := &fakeLLM{pmText: "spec", archText: "plan", contentFn: func(int) string { return goodAppTSX }}
	_, rb1, _, err := st.CreateRun(ctx, p.ID, "build a habit tracker", "", "rk1", true)
	if err != nil {
		t.Fatal(err)
	}
	var r1 struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb1, &r1)
	newLoop(goodFake.stream).Run(ctx, r1.RunID)

	stable1, err := st.GetProjectStable(ctx, p.ID)
	if err != nil || stable1 == nil {
		t.Fatalf("expected a stable version after run 1, got %v (err=%v)", stable1, err)
	}
	v1Files, err := st.GetVersionFilesSnapshot(ctx, *stable1)
	if err != nil {
		t.Fatal(err)
	}
	v1App := ""
	for _, f := range v1Files {
		if f.Path == writablePath {
			v1App = f.Content
		}
	}
	if v1App != goodAppTSX {
		t.Fatalf("V1 App.tsx should be the good output")
	}

	// Run 2: edit run based on V1, Engineer always emits uncompilable code.
	// QA rejects 3× -> run_failed COMPILE_FAILED.
	badFake := &fakeLLM{pmText: "spec", archText: "plan", contentFn: func(int) string { return badAppTSX }}
	_, rb2, _, err := st.CreateRun(ctx, p.ID, "change the color", *stable1, "rk2", true)
	if err != nil {
		t.Fatalf("create edit run: %v", err)
	}
	var r2 struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb2, &r2)
	newLoop(badFake.stream).Run(ctx, r2.RunID)

	run2, err := st.GetRun(ctx, r2.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Status != "failed" {
		t.Fatalf("run 2 status = %q, want failed (COMPILE_FAILED)", run2.Status)
	}
	if code := lastRunFailedCode(ctx, st, r2.RunID); code != "COMPILE_FAILED" {
		t.Fatalf("run 2 terminal code = %q, want COMPILE_FAILED", code)
	}

	// stableVersion must be unchanged: still V1.
	stable2, err := st.GetProjectStable(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stable2 == nil || *stable2 != *stable1 {
		t.Fatalf("stableVersion changed after compile failure: was %s, now %v (must be unchanged)", *stable1, stable2)
	}

	// The old stable preview's files must be byte-for-byte unchanged.
	v2Files, err := st.GetVersionFilesSnapshot(ctx, *stable2)
	if err != nil {
		t.Fatal(err)
	}
	v2App := ""
	for _, f := range v2Files {
		if f.Path == writablePath {
			v2App = f.Content
		}
	}
	if v2App != v1App {
		t.Fatalf("stable preview App.tsx changed after a failed run (old preview must be immutable)")
	}

	// The failed draft exists separately and is not the stable version.
	it, _ := st.GetRunIteration(ctx, r2.RunID)
	if it.ResultVersionID != nil {
		// A failed draft version should not equal stable.
		if *it.ResultVersionID == *stable1 {
			t.Fatalf("failed draft was promoted to stable (must not be)")
		}
	}
}
