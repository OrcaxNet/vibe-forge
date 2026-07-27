package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/db"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// TestSmokeRealAPI (acceptance 6) drives the full agent loop against a real LLM
// with a FIXED prompt (a habit tracker) - not preset/hardcoded output - and
// captures the generated App.tsx and event stream. It is skipped unless
// SMOKE=1 is set, and uses ANTHROPIC_AUTH_TOKEN + ANTHROPIC_BASE_URL (the
// platform proxy) or ANTHROPIC_API_KEY. Run: SMOKE=1 go test ./internal/agent/ -run TestSmokeRealAPI -v -timeout 5m
func TestSmokeRealAPI(t *testing.T) {
	if os.Getenv("SMOKE") != "1" {
		t.Skip("skipping real-API smoke; set SMOKE=1 (and ANTHROPIC_AUTH_TOKEN/ANTHROPIC_BASE_URL or ANTHROPIC_API_KEY) to run")
	}
	cfg := LoopConfig{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		AuthToken: os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		BaseURL:   os.Getenv("ANTHROPIC_BASE_URL"),
	}
	if cfg.APIKey == "" && cfg.AuthToken == "" {
		t.Skip("no ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN set; cannot run real-API smoke")
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		cfg.Model = m
	} else {
		cfg.Model = "claude-sonnet-5"
	}
	t.Logf("smoke: model=%s baseURL set=%v auth=%s", cfg.Model, cfg.BaseURL != "", func() string {
		if cfg.AuthToken != "" {
			return "token"
		}
		return "apikey"
	}())

	dbase, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	if err := db.Migrate(dbase); err != nil {
		t.Fatal(err)
	}
	st := store.New(dbase)

	const prompt = "Build a habit tracker app. Show a list of three habits, each with a checkbox to mark it done for today, and a daily streak counter that increments when a habit is checked. Use Tailwind utility classes for a clean card layout. Keep everything in App.tsx."
	_, pbody, _, err := st.CreateProject(context.Background(), "Smoke", prompt, "smoke-pk")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(pbody, &p)
	_, rbody, _, err := st.CreateRun(context.Background(), p.ID, prompt, "", "smoke-rk")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rbody, &r)

	loop, err := NewLoop(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	loop.SetLogger(func(format string, args ...any) { t.Logf("loop: "+format, args...) })

	// Run synchronously (the loop blocks until terminal).
	done := make(chan struct{})
	go func() { loop.Run(context.Background(), r.RunID); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Minute):
		t.Fatal("smoke run did not finish within 4 minutes")
	}

	run, err := st.GetRun(context.Background(), r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("smoke: run status=%s", run.Status)
	if run.Status != "succeeded" {
		// Dump the last draft's generated file + the structural compile result
		// so we can see exactly what the QA gate rejected.
		it, ierr := st.GetRunIteration(context.Background(), r.RunID)
		if ierr == nil && it.ResultVersionID != nil {
			files, _ := st.GetVersionFilesSnapshot(context.Background(), *it.ResultVersionID)
			for _, f := range files {
				if f.Path == writablePath {
					t.Logf("smoke: FAILED draft App.tsx (%d bytes):\n%s", len(f.Content), f.Content)
					res := ValidateCompile(f.Content)
					t.Logf("smoke: ValidateCompile pass=%v errors=%+v", res.Pass, res.Errors)
					_ = os.WriteFile(filepath.Join(".", "smoke_app_failed.tsx"), []byte(f.Content), 0644)
					break
				}
			}
		}
		events, _ := st.ListEvents(context.Background(), r.RunID, 0)
		enc, _ := json.MarshalIndent(events, "", "  ")
		_ = os.WriteFile(filepath.Join(".", "smoke_events.json"), enc, 0644)
		t.Fatalf("smoke run did not succeed (status=%s). %d events captured to ./smoke_events.json", run.Status, len(events))
	}

	stable, err := st.GetProjectStable(context.Background(), p.ID)
	if err != nil || stable == nil {
		t.Fatalf("smoke: no stable version (err=%v)", err)
	}
	files, err := st.GetVersionFilesSnapshot(context.Background(), *stable)
	if err != nil {
		t.Fatal(err)
	}
	appTSX := ""
	for _, f := range files {
		if f.Path == writablePath {
			appTSX = f.Content
		}
	}
	if appTSX == "" {
		t.Fatal("smoke: generated App.tsx is empty")
	}
	t.Logf("smoke: generated App.tsx (%d bytes):\n%s", len(appTSX), appTSX)

	// Sanity: the generated file must be real (default export, non-trivial) and
	// not a preset - it should reference the habit/streak idea from the prompt.
	if len(appTSX) < 200 {
		t.Errorf("smoke: App.tsx only %d bytes, suspiciously small (preset?)", len(appTSX))
	}
	low := string(toLower(appTSX))
	if !contains(low, "habit") && !contains(low, "streak") {
		t.Errorf("smoke: App.tsx does not reference the habit/streak prompt (preset?)")
	}

	// Capture artifacts + events for evidence.
	arts, _ := st.ListStageArtifacts(context.Background(), r.RunID)
	t.Logf("smoke: %d stage artifacts (stages: %v)", len(arts), stageListOf(arts))
	events, _ := st.ListEvents(context.Background(), r.RunID, 0)
	t.Logf("smoke: %d events; terminal=%s", len(events), events[len(events)-1].Type)

	// Write evidence to the workdir (not committed; used to capture for the PR).
	_ = os.WriteFile(filepath.Join(".", "smoke_app.tsx"), []byte(appTSX), 0644)
	enc, _ := json.MarshalIndent(events, "", "  ")
	_ = os.WriteFile(filepath.Join(".", "smoke_events.json"), enc, 0644)
}

func toLower(s string) []byte {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return b
}
func contains(s string, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func stageListOf(arts []store.StageArtifact) []string {
	var out []string
	for _, a := range arts {
		out = append(out, a.Stage)
	}
	return out
}
