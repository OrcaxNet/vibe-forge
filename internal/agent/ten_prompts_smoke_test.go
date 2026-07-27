package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/db"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// tenPrompts is the FIXED 10-prompt suite from FLO-58's real-e2e-results.json.
// Do not change these strings - the ≥9/10 gate (VF-P0-01) is measured against
// exactly this set.
var tenPrompts = []string{
	"生成一个习惯追踪器，支持新增、完成和删除习惯，并显示完成统计。",
	"生成一个待办清单，支持新增、勾选完成、删除和全部清除。",
	"生成一个番茄钟，支持开始、暂停、重置，并显示当前专注轮次。",
	"生成一个个人记账板，支持新增收入支出、删除记录和计算余额。",
	"生成一个学习计划板，支持新增任务、按分类筛选和标记完成。",
	"生成一个四选一知识问答，答题后显示对错和最终得分。",
	"生成一个食谱收藏页，支持新增食谱、关键词筛选和删除。",
	"生成一个活动倒计时页面，可修改目标日期并显示剩余时间。",
	"生成一个单词卡片工具，支持添加卡片、翻面和删除。",
	"生成一个配色板工具，点击按钮生成五个颜色并可复制色值。",
}

type compileAttempt struct {
	Attempt  int            `json:"attempt"`
	Pass     bool           `json:"pass"`
	Errors   []CompileError `json:"errors,omitempty"`
	AppBytes int            `json:"appBytes"`
}

type promptResult struct {
	Index             int              `json:"index"`
	Prompt            string           `json:"prompt"`
	Success           bool             `json:"success"`
	Terminal          string           `json:"terminal"`
	TerminalCode      string           `json:"terminalCode,omitempty"`
	DurationMs        int64            `json:"durationMs"`
	EventCount        int              `json:"eventCount"`
	SeqMonotonic      bool             `json:"seqMonotonic"`
	StageStarts       []string         `json:"stageStarts"`
	ArtifactStages    []string         `json:"artifactStages"`
	HasFileWritten    bool             `json:"hasFileWritten"`
	HasPreviewReady   bool             `json:"hasPreviewReady"`
	StableVersionID   *string          `json:"stableVersionId,omitempty"`
	TerminalVersionID *string          `json:"terminalVersionId,omitempty"`
	AppBytes          int              `json:"appBytes"`
	SecretPattern     bool             `json:"secretPatternFound"`
	CompileAttempts   []compileAttempt `json:"compileAttempts,omitempty"`
	AppTsxPath        string           `json:"appTsxPath,omitempty"`
	InfraError        string           `json:"infraError,omitempty"`
}

// TestSmokeTenPrompts reproduces FLO-58's 10-prompt real-API suite against the
// current code, captures per-prompt terminal state / duration / compile errors /
// generated App.tsx, and writes ten_prompts_results.json. It is the VF-P0-01
// gate: ≥9/10 success AND the first three consecutive Happy Path must succeed.
//
//	SMOKE=1 go test ./internal/agent/ -run TestSmokeTenPrompts -v -timeout 20m
func TestSmokeTenPrompts(t *testing.T) {
	if os.Getenv("SMOKE") != "1" {
		t.Skip("skipping 10-prompt smoke; set SMOKE=1 to run")
	}
	cfg := LoopConfig{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		AuthToken: os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		BaseURL:   os.Getenv("ANTHROPIC_BASE_URL"),
	}
	if cfg.APIKey == "" && cfg.AuthToken == "" {
		t.Skip("no ANTHROPIC credentials set")
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		cfg.Model = m
	} else {
		cfg.Model = "claude-sonnet-5"
	}
	outDir := filepath.Join(".", "tenprompts")
	_ = os.MkdirAll(outDir, 0755)

	// One shared in-memory DB for all 10 projects (projects are independent).
	dbase, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	if err := db.Migrate(dbase); err != nil {
		t.Fatal(err)
	}
	st := store.New(dbase)

	loop, err := NewLoop(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	loop.SetLogger(func(format string, args ...any) {})

	results := make([]promptResult, len(tenPrompts))
	concurrency := 3
	if c := os.Getenv("SMOKE_CONCURRENCY"); c != "" {
		fmt.Sscanf(c, "%d", &concurrency)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, prompt := range tenPrompts {
		i, prompt := i, prompt
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runOnePrompt(t, st, loop, i+1, prompt, outDir)
		}()
	}
	wg.Wait()

	// Persist results.
	enc, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile(filepath.Join(".", "ten_prompts_results.json"), enc, 0644)

	// Summary.
	success := 0
	for _, r := range results {
		if r.Success {
			success++
		}
	}
	firstThreeConsecutive := len(results) >= 3 && results[0].Success && results[1].Success && results[2].Success
	t.Logf("==== 10-PROMPT SMOKE SUMMARY: %d/10 success; first3Consecutive=%v ====", success, firstThreeConsecutive)
	for _, r := range results {
		extra := ""
		if !r.Success && len(r.CompileAttempts) > 0 {
			last := r.CompileAttempts[len(r.CompileAttempts)-1]
			if len(last.Errors) > 0 {
				extra = fmt.Sprintf(" lastErr=%s:%d %s", last.Errors[0].File, last.Errors[0].Line, last.Errors[0].Message)
			}
		}
		t.Logf("  [%d] success=%v terminal=%s code=%s dur=%dms app=%dB%s", r.Index, r.Success, r.Terminal, r.TerminalCode, r.DurationMs, r.AppBytes, extra)
	}

	// Gate (VF-P0-01): ≥9/10 AND first three consecutive Happy Path.
	if success < 9 {
		t.Errorf("VF-P0-01 gate FAILED: %d/10 success (need ≥9)", success)
	}
	if !firstThreeConsecutive {
		t.Errorf("VF-P0-01 gate FAILED: first three prompts not all successful (need consecutive Happy Path)")
	}
}

func runOnePrompt(t *testing.T, st *store.Store, loop *Loop, index int, prompt, outDir string) promptResult {
	r := promptResult{Index: index, Prompt: prompt, SeqMonotonic: true}
	start := time.Now()
	ctx := context.Background()

	pstat, pbody, _, err := st.CreateProject(ctx, fmt.Sprintf("Prompt %d", index), prompt, fmt.Sprintf("p-%d-%d", index, time.Now().UnixNano()))
	if err != nil || pstat != 201 {
		r.InfraError = fmt.Sprintf("CreateProject: status=%d err=%v", pstat, err)
		r.Terminal = "infra_error"
		return r
	}
	var pj struct {
		ID string `json:"id"`
	}
	json.Unmarshal(pbody, &pj)

	rstat, rbody, _, err := st.CreateRun(ctx, pj.ID, prompt, "", fmt.Sprintf("r-%d-%d", index, time.Now().UnixNano()), true)
	if err != nil || rstat != 202 {
		r.InfraError = fmt.Sprintf("CreateRun: status=%d err=%v", rstat, err)
		r.Terminal = "infra_error"
		return r
	}
	var rj struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rbody, &rj)

	done := make(chan struct{})
	go func() { loop.Run(ctx, rj.RunID); close(done) }()
	select {
	case <-done:
	case <-time.After(6 * time.Minute):
		r.InfraError = "run timed out after 6m"
		r.Terminal = "timeout"
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}
	r.DurationMs = time.Since(start).Milliseconds()

	run, err := st.GetRun(ctx, rj.RunID)
	if err != nil {
		r.InfraError = fmt.Sprintf("GetRun: %v", err)
		r.Terminal = "infra_error"
		return r
	}
	r.Terminal = run.Status
	r.Success = run.Status == "succeeded"
	if !r.Success {
		r.TerminalCode = lastRunFailedCode(ctx, st, rj.RunID)
	}

	events, _ := st.ListEvents(ctx, rj.RunID, 0)
	r.EventCount = len(events)
	prevSeq := 0
	for _, e := range events {
		if e.Seq <= prevSeq {
			r.SeqMonotonic = false
		}
		prevSeq = e.Seq
		switch e.Type {
		case "stage_started":
			if s, ok := e.Payload["stage"].(string); ok {
				r.StageStarts = append(r.StageStarts, s)
			}
		case "stage_artifact":
			if s, ok := e.Payload["stage"].(string); ok {
				r.ArtifactStages = append(r.ArtifactStages, s)
			}
		case "file_written":
			r.HasFileWritten = true
		case "preview_ready":
			r.HasPreviewReady = true
			if v, ok := e.Payload["versionId"].(string); ok {
				r.TerminalVersionID = strPtr(v)
			}
		case "run_completed":
			if v, ok := e.Payload["versionId"].(string); ok {
				r.TerminalVersionID = strPtr(v)
			}
		}
	}

	stable, _ := st.GetProjectStable(ctx, pj.ID)
	if stable != nil {
		r.StableVersionID = stable
		// consistency: stable == terminal version
		if r.TerminalVersionID != nil && *r.TerminalVersionID != *stable {
			r.InfraError = fmt.Sprintf("stable %s != terminal %s", *stable, *r.TerminalVersionID)
		}
	}

	// Capture every generated draft (one per file_written) and re-run the QA
	// gate on it, so we can classify why failed runs were rejected.
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != "file_written" {
			continue
		}
		vid, _ := e.Payload["versionDraftId"].(string)
		if vid == "" || seen[vid] {
			continue
		}
		seen[vid] = true
		files, _ := st.GetVersionFilesSnapshot(ctx, vid)
		app := ""
		for _, f := range files {
			if f.Path == writablePath {
				app = f.Content
			}
		}
		res := ValidateCompile(app)
		attempt := compileAttempt{Attempt: len(r.CompileAttempts) + 1, Pass: res.Pass, AppBytes: len(app), Errors: res.Errors}
		r.CompileAttempts = append(r.CompileAttempts, attempt)
		if app != "" {
			fname := filepath.Join(outDir, fmt.Sprintf("%d-attempt%d.tsx", index, attempt.Attempt))
			_ = os.WriteFile(fname, []byte(app), 0644)
			r.AppTsxPath = fname
		}
		if app != "" {
			r.AppBytes = len(app)
		}
		r.SecretPattern = r.SecretPattern || secretScan(app)
	}
	return r
}

func lastRunFailedCode(ctx context.Context, st *store.Store, runID string) string {
	events, _ := st.ListEvents(ctx, runID, 0)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "run_failed" {
			if c, ok := events[i].Payload["code"].(string); ok {
				return c
			}
		}
	}
	return ""
}

func strPtr(s string) *string { return &s }

func secretScan(s string) bool {
	low := strings.ToLower(s)
	for _, pat := range []string{"sk-ant", "api_key", "apikey", "authorization", "bearer", "anthropic_auth", "process.env"} {
		if strings.Contains(low, pat) {
			return true
		}
	}
	return false
}
