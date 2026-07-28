package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/OrcaxNet/vibe-forge/internal/db"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// fakeLLM is a test streamCall that returns canned stage outputs without hitting
// the Claude API. It routes by the last user message: PM -> spec text,
// Architect -> plan text, Engineer (initial or repair) -> a write_file tool_use
// whose content comes from contentFn(call#), and a user tool_result -> end_turn.
type fakeLLM struct {
	mu        sync.Mutex
	engCalls  int
	contentFn func(call int) string
	pmText    string
	archText  string
}

func (f *fakeLLM) stream(_ context.Context, params anthropic.MessageNewParams, _ func(string), _ func()) (anthropic.Message, error) {
	last := params.Messages[len(params.Messages)-1]
	for _, b := range last.Content {
		if b.OfToolResult != nil {
			return textMsg("done"), nil // turn complete after tool_result
		}
	}
	text := ""
	for _, b := range last.Content {
		if b.OfText != nil {
			text = b.OfText.Text
		}
	}
	switch {
	case strings.Contains(text, "Product Manager"):
		return textMsg(f.pmText), nil
	case strings.Contains(text, "Architect"):
		return textMsg(f.archText), nil
	default: // Engineer initial or repair
		f.mu.Lock()
		f.engCalls++
		n := f.engCalls
		f.mu.Unlock()
		return toolUseMsg(n, f.contentFn(n)), nil
	}
}

func textMsg(s string) anthropic.Message {
	return anthropic.Message{
		StopReason: anthropic.StopReasonEndTurn,
		Content:    []anthropic.ContentBlockUnion{{Type: "text", Text: s}},
	}
}

func toolUseMsg(n int, content string) anthropic.Message {
	input, _ := json.Marshal(map[string]string{"path": "/src/App.tsx", "content": content})
	return anthropic.Message{
		StopReason: anthropic.StopReasonToolUse,
		Content: []anthropic.ContentBlockUnion{{
			Type: "tool_use", ID: "toolu_fake_" + itoa(n), Name: "write_file", Input: input,
		}},
	}
}

const (
	goodAppTSX = `import { useState } from "react";
export default function App() {
  const [n, setN] = useState(0);
  return (
    <div className="p-4">
      <h1>Habit Tracker: {n}</h1>
      <button onClick={() => setN(n + 1)}>+</button>
    </div>
  );
}`
	badAppTSX = `export default function App() {
  return <div>broken`
)

// setupLoop builds an in-memory store with a project + run and a Loop wired to
// the given fake stream. Returns the loop, store, projectID, runID.
func setupLoop(t *testing.T, fake streamCall) (*Loop, *store.Store, string, string) {
	t.Helper()
	dbase, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })
	if err := db.Migrate(dbase); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(dbase)

	_, pbody, _, err := st.CreateProject(context.Background(), "Test", "build a habit tracker", "pk")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(pbody, &p)

	_, rbody, _, err := st.CreateRun(context.Background(), p.ID, "build a habit tracker", "", "rk", true)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	var r struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rbody, &r)

	l := &Loop{
		store:  st,
		model:  "fake-model",
		now:    func() time.Time { return time.Now().UTC() },
		logf:   func(string, ...any) {},
		stream: fake,
	}
	return l, st, p.ID, r.RunID
}

// TestLoopSuccess runs a full PM->Architect->Engineer->QA pipeline where QA
// passes on the first try: the run succeeds, stableVersion switches to the new
// version, preview_ready.versionId == stableVersionId, all four stage nodes
// have queryable artifactRefs, and run_completed is the terminal event.
func TestLoopSuccess(t *testing.T) {
	fake := &fakeLLM{
		pmText:    "# Spec\nA habit tracker with a counter.",
		archText:  "# Plan\nOne component, useState.",
		contentFn: func(int) string { return goodAppTSX },
	}
	l, st, projectID, runID := setupLoop(t, fake.stream)

	l.Run(context.Background(), runID)

	// Run succeeded.
	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" {
		t.Errorf("run status = %q, want succeeded", run.Status)
	}

	// stableVersion switched to the new version.
	stable, err := st.GetProjectStable(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if stable == nil {
		t.Fatal("stableVersion nil after success; expected the promoted version")
	}

	// All four stage nodes have artifacts (acceptance 2: queryable artifactRef).
	arts, err := st.ListStageArtifacts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	byStage := map[string]bool{}
	for _, a := range arts {
		if a.ArtifactRef == "" {
			t.Errorf("stage %s artifact has empty artifactRef", a.Stage)
		}
		byStage[a.Stage] = true
	}
	for _, s := range []string{"pm", "architect", "engineer", "qa"} {
		if !byStage[s] {
			t.Errorf("missing artifact for stage %q", s)
		}
	}

	// Events: monotonic seq, terminal run_completed, and preview_ready.versionId
	// == stableVersionId (B-FR-07).
	events, err := st.ListEvents(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Errorf("event %d seq = %d, want %d (monotonic)", i, e.Seq, i+1)
		}
	}
	last := events[len(events)-1]
	if last.Type != "run_completed" {
		t.Errorf("last event = %q, want run_completed", last.Type)
	}
	// Find preview_ready and check versionId == stable.
	var previewVer string
	for _, e := range events {
		if e.Type == "preview_ready" {
			if v, ok := e.Payload["versionId"].(string); ok {
				previewVer = v
			}
		}
	}
	if previewVer == "" {
		t.Fatal("no preview_ready event with versionId")
	}
	if previewVer != *stable {
		t.Errorf("preview_ready.versionId = %q, stableVersionId = %q (must match, B-FR-07)", previewVer, *stable)
	}
}

// TestLoopAutoRepairThirdFailRunsFailed: when every Engineer write fails QA, the
// loop auto-repairs up to maxAutoFixRounds (2) and then fails on the 3rd
// consecutive compile fail (acceptance 5). The previous stableVersion is
// unchanged (nil), there is no infinite loop (exactly 3 engineer writes), the
// run is 'failed' with a COMPILE_FAILED run_failed event, and the QA node has
// no succeeded artifact.
func TestLoopAutoRepairThirdFailRunsFailed(t *testing.T) {
	fake := &fakeLLM{
		pmText:    "spec",
		archText:  "plan",
		contentFn: func(int) string { return badAppTSX }, // always uncompilable
	}
	l, st, projectID, runID := setupLoop(t, fake.stream)

	l.Run(context.Background(), runID)

	if fake.engCalls != 3 {
		t.Errorf("engineer writes = %d, want exactly 3 (initial + 2 repairs, no infinite loop)", fake.engCalls)
	}

	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}

	// stableVersion unchanged (still nil - there was no prior stable version).
	stable, err := st.GetProjectStable(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if stable != nil {
		t.Errorf("stableVersion = %v after compile failure; must be unchanged (nil)", stable)
	}

	// Terminal event is run_failed COMPILE_FAILED.
	events, _ := st.ListEvents(context.Background(), runID, 0)
	last := events[len(events)-1]
	if last.Type != "run_failed" {
		t.Errorf("last event = %q, want run_failed", last.Type)
	}
	if code, _ := last.Payload["code"].(string); code != "COMPILE_FAILED" {
		t.Errorf("run_failed code = %q, want COMPILE_FAILED", code)
	}

	// QA node never succeeded: no qa stage_artifact (only pm/architect/engineer).
	arts, _ := st.ListStageArtifacts(context.Background(), runID)
	for _, a := range arts {
		if a.Stage == "qa" {
			t.Errorf("qa artifact present after compile failure; QA node must not be succeeded")
		}
	}
}

// TestLoopAutoRepairSucceedsOnSecondWrite: the first write fails QA, the
// auto-repair re-feed produces compilable code on the second write, and the run
// succeeds - proving the repair feedback path works and the loop does not give
// up early.
func TestLoopAutoRepairSucceedsOnSecondWrite(t *testing.T) {
	fake := &fakeLLM{
		pmText:   "spec",
		archText: "plan",
		contentFn: func(call int) string {
			if call == 1 {
				return badAppTSX
			}
			return goodAppTSX
		},
	}
	l, st, projectID, runID := setupLoop(t, fake.stream)

	l.Run(context.Background(), runID)

	if fake.engCalls != 2 {
		t.Errorf("engineer writes = %d, want 2 (initial fail + 1 repair)", fake.engCalls)
	}
	run, _ := st.GetRun(context.Background(), runID)
	if run.Status != "succeeded" {
		t.Errorf("run status = %q, want succeeded after repair", run.Status)
	}
	stable, _ := st.GetProjectStable(context.Background(), projectID)
	if stable == nil {
		t.Error("stableVersion nil; expected promotion after successful repair")
	}
}

// TestLoopInfraFailureLeavesStableUnchanged: a Claude 429 mid-run fails the run
// with RATE_LIMITED (retryable) and leaves stableVersion unchanged (C-FR-04).
func TestLoopInfraFailureLeavesStableUnchanged(t *testing.T) {
	// Fake that returns a 429 on the very first call (PM stage).
	failing := func(_ context.Context, _ anthropic.MessageNewParams, _ func(string), _ func()) (anthropic.Message, error) {
		return anthropic.Message{}, &anthropic.Error{StatusCode: 429}
	}
	l, st, projectID, runID := setupLoop(t, failing)

	l.Run(context.Background(), runID)

	run, _ := st.GetRun(context.Background(), runID)
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed after 429", run.Status)
	}
	stable, _ := st.GetProjectStable(context.Background(), projectID)
	if stable != nil {
		t.Errorf("stableVersion = %v after infra failure; must be unchanged", stable)
	}
	events, _ := st.ListEvents(context.Background(), runID, 0)
	last := events[len(events)-1]
	if last.Type != "run_failed" {
		t.Errorf("last event = %q, want run_failed", last.Type)
	}
	if code, _ := last.Payload["code"].(string); code != "RATE_LIMITED" {
		t.Errorf("run_failed code = %q, want RATE_LIMITED", code)
	}
	if retryable, _ := last.Payload["retryable"].(bool); !retryable {
		t.Error("run_failed retryable = false, want true for RATE_LIMITED")
	}
}

// TestLoopEventsReplayNoDup: ListEvents(afterSeq) returns exactly the tail with
// no gaps or duplicates - the guarantee the SSE Last-Event-ID replay relies on.
func TestLoopEventsReplayNoDup(t *testing.T) {
	fake := &fakeLLM{
		pmText: "spec", archText: "plan", contentFn: func(int) string { return goodAppTSX },
	}
	l, st, _, runID := setupLoop(t, fake.stream)
	l.Run(context.Background(), runID)

	all, _ := st.ListEvents(context.Background(), runID, 0)
	if len(all) < 4 {
		t.Fatalf("expected several events, got %d", len(all))
	}
	mid := all[2].Seq
	tail, _ := st.ListEvents(context.Background(), runID, mid)
	// Tail must be exactly all[3:], in order, with no duplicate of mid.
	if len(tail) != len(all)-3 {
		t.Errorf("tail len = %d, want %d", len(tail), len(all)-3)
	}
	for i, e := range tail {
		if e.Seq <= mid {
			t.Errorf("tail event seq %d <= afterSeq %d (dup/replay leak)", e.Seq, mid)
		}
		if e.Seq != all[3+i].Seq {
			t.Errorf("tail[%d] seq = %d, want %d (order)", i, e.Seq, all[3+i].Seq)
		}
	}
}
