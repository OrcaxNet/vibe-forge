package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/OrcaxNet/vibe-forge/contracts"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// agent.go implements the Vibe Forge single agent loop (FLO-60): one Claude
// conversation thread driven PM -> Architect -> Engineer -> QA, four strictly
// serial stages, with a sandboxed write_file tool, an SSE event stream
// persisted to SQLite, a 60s watchdog, and bounded auto-repair.
//
// Lifecycle (per run):
//   - run_started; BeginAttempt (queued -> running).
//   - PM (text) -> spec artifact; Architect (text) -> structure_plan artifact.
//   - Engineer (tool-use, write_file) -> /src/App.tsx; a draft version is
//     created and file_written emitted (stable preview NOT switched).
//   - QA = server-side structural compile (compile.go). Pass -> stage_artifact,
//     PromoteDraftVersion (draft -> stable, atomic CAS), preview_ready,
//     run_completed. Fail -> re-feed errors to Engineer for up to
//     maxAutoFixRounds (2) repairs; a 3rd consecutive fail -> run_failed
//     COMPILE_FAILED, the draft is failed, stableVersion is never touched.
//   - Claude 429 / 5xx / timeout / watchdog -> run_failed{RATE_LIMITED,
//     UPSTREAM_ERROR, TIMEOUT, INTERNAL} (retryable); stableVersion unchanged.
//   - Server shutdown (parent ctx cancelled) -> exit without a terminal event;
//     MarkActiveRunsInterrupted reconciles the run as 'interrupted' on restart.
//
// Thinking is disabled for the loop so the multi-turn tool-use reconstruction
// (echoing the assistant turn with its tool_use block) stays simple and
// reliable; the prompts are explicit enough that Opus produces compilable TSX
// without extended thinking.

// errWatchdog is the cancellation cause set when the run watchdog fires (no
// effective SSE event within RunWatchdogSeconds). Run uses it to tell a
// watchdog timeout apart from a server shutdown.
var errWatchdog = errors.New("run watchdog: no effective event within watchdogSeconds")

// LoopConfig configures the agent loop.
type LoopConfig struct {
	APIKey    string // ANTHROPIC_API_KEY auth (mutually exclusive with AuthToken)
	AuthToken string // ANTHROPIC_AUTH_TOKEN bearer auth (e.g. platform proxy)
	BaseURL   string // optional API base URL override (required with AuthToken)
	Model     string // default claude-opus-4-8
}

// streamCall is one Claude streaming turn. It accumulates the full message,
// calls onDelta for each text chunk (so the runner can emit message_delta and
// keep the watchdog fresh), and returns the accumulated message. Abstracted as a
// field so tests can inject a fake LLM without hitting the API.
type streamCall func(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (anthropic.Message, error)

// Loop drives agent runs against the store and the Claude API. It is safe to
// call Run concurrently for different runIDs (each Run builds its own runner).
type Loop struct {
	store  *store.Store
	client anthropic.Client
	model  anthropic.Model
	now    func() time.Time
	logf   func(format string, args ...any)
	stream streamCall // realStream(client) in prod; fake in tests
}

// NewLoop constructs a Loop. It returns an error if the store or API key is
// missing; the caller (Server.InitLoop) simply skips wiring the loop in that
// case so the rest of the API still serves.
func NewLoop(st *store.Store, cfg LoopConfig) (*Loop, error) {
	if st == nil {
		return nil, errors.New("agent: nil store")
	}
	if cfg.APIKey == "" && cfg.AuthToken == "" {
		return nil, errors.New("agent: API key or auth token required")
	}
	model := cfg.Model
	if model == "" {
		model = string(anthropic.ModelClaudeOpus4_8)
	}
	var opts []option.RequestOption
	if cfg.AuthToken != "" {
		opts = append(opts, option.WithAuthToken(cfg.AuthToken))
	} else {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	client := anthropic.NewClient(opts...)
	return &Loop{
		store:  st,
		client: client,
		model:  anthropic.Model(model),
		now:    func() time.Time { return time.Now().UTC() },
		logf:   func(string, ...any) {},
		stream: realStream(client),
	}, nil
}

// realStream returns a streamCall that drives the Claude SDK streaming loop,
// accumulating the message and forwarding text deltas to onDelta.
func realStream(client anthropic.Client) streamCall {
	return func(ctx context.Context, params anthropic.MessageNewParams, onDelta func(string)) (anthropic.Message, error) {
		stream := client.Messages.NewStreaming(ctx, params)
		defer stream.Close()
		var msg anthropic.Message
		for stream.Next() {
			ev := stream.Current()
			_ = msg.Accumulate(ev)
			if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				onDelta(ev.Delta.Text)
			}
		}
		if err := stream.Err(); err != nil {
			return msg, err
		}
		return msg, nil
	}
}

// SetLogger installs a logger (best-effort; nil-able). Unused in tests.
func (l *Loop) SetLogger(f func(format string, args ...any)) {
	if f != nil {
		l.logf = f
	}
}

// Run executes the agent loop for one run. It always returns after the run
// reaches a terminal state (succeeded / failed) or the parent context is
// cancelled (server shutdown). It never panics on Claude errors.
func (l *Loop) Run(parent context.Context, runID string) {
	runCtx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)

	r := &runner{l: l, runID: runID, lastEvent: l.now()}
	l.startWatchdog(runCtx, r, cancel)

	err := r.run(runCtx)
	if err == nil {
		return // success or COMPILE_FAILED already terminalized inside r.run
	}
	// Server shutdown: leave the run to be reconciled as 'interrupted' on
	// restart (MarkActiveRunsInterrupted). Do not fail or emit a terminal event.
	if parent.Err() != nil {
		l.logf("agent: run %s aborted by shutdown: %v", runID, err)
		return
	}
	// Infra failure (429 / 5xx / timeout / internal): fail the run (stableVersion
	// untouched) and emit the terminal run_failed. Use Background so the
	// (possibly watchdog-cancelled) runCtx cannot block the terminal write.
	r.failInfra()
	code, retryable := classifyError(err, runCtx)
	l.logf("agent: run %s failed: code=%s stage=%s err=%v", runID, code, r.stage, err)
	r.emitBG("run_failed", map[string]any{
		"runId":     runID,
		"stage":     r.stage,
		"code":      code,
		"retryable": retryable,
	})
}

// startWatchdog cancels the run (with errWatchdog cause) if no effective SSE
// event is persisted within RunWatchdogSeconds. Effective events are emitted
// throughout streaming (message_delta per text chunk), so a healthy long
// generation keeps the watchdog fresh; a stalled call trips it.
func (l *Loop) startWatchdog(ctx context.Context, r *runner, cancel context.CancelCauseFunc) {
	go func() {
		wd := time.Duration(contracts.Load().Limits.RunWatchdogSeconds) * time.Second
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.mu.Lock()
				last := r.lastEvent
				r.mu.Unlock()
				if time.Since(last) > wd {
					cancel(errWatchdog)
					return
				}
			}
		}
	}()
}

// classifyError maps a Claude/infra error to a contract RunFailureCode. Any
// context cancellation reaching here is a watchdog/timeout (server shutdown is
// filtered out by the caller via parent.Err()).
func classifyError(err error, runCtx context.Context) (string, bool) {
	if context.Cause(runCtx) == errWatchdog ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return "TIMEOUT", true
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 429:
			return "RATE_LIMITED", true
		case apiErr.StatusCode >= 500:
			return "UPSTREAM_ERROR", true
		default:
			return "INTERNAL", true
		}
	}
	return "INTERNAL", true
}

// runner holds the per-run mutable state.
type runner struct {
	l                             *Loop
	runID, projectID, iterationID string
	attemptID                     string
	stage                         string // current stage for run_failed attribution
	currentDraft                  string // draft version id (for failInfra); "" until first Engineer write
	start                         time.Time
	msgSeq                        int // message_delta payload seq (dedup)
	mu                            sync.Mutex
	lastEvent                     time.Time
}

// run is the per-run pipeline. Returns nil on success or COMPILE_FAILED (both
// terminalized inside); returns non-nil on infra failure (caller terminalizes).
func (r *runner) run(ctx context.Context) error {
	run, err := r.l.store.GetRun(ctx, r.runID)
	if err != nil {
		return err
	}
	r.projectID = run.ProjectID
	iter, err := r.l.store.GetRunIteration(ctx, r.runID)
	if err != nil {
		return err
	}
	r.iterationID = iter.ID
	att, err := r.l.store.GetActiveAttempt(ctx, r.runID)
	if err != nil {
		return err
	}
	r.attemptID = att.ID
	r.start = r.l.now()

	if err := r.emit(ctx, "run_started", map[string]any{
		"runId": r.runID, "projectId": r.projectID, "iterationId": r.iterationID,
	}); err != nil {
		return err
	}
	if err := r.l.store.BeginAttempt(ctx, r.runID, r.attemptID); err != nil {
		return err
	}

	userIdea := run.Prompt
	var messages []anthropic.MessageParam

	// PM
	r.stage = "pm"
	r.emit(ctx, "stage_started", map[string]any{"runId": r.runID, "stage": "pm", "sequence": 1})
	spec, msgs, err := r.textTurn(ctx, messages, pmUserTurn(userIdea))
	if err != nil {
		return err
	}
	messages = msgs
	r.persistAssistant(spec)
	r.recordArtifact(ctx, "pm", "spec", spec)

	// Architect
	r.stage = "architect"
	r.emit(ctx, "stage_started", map[string]any{"runId": r.runID, "stage": "architect", "sequence": 2})
	plan, msgs, err := r.textTurn(ctx, messages, architectUserTurn(spec))
	if err != nil {
		return err
	}
	messages = msgs
	r.persistAssistant(plan)
	r.recordArtifact(ctx, "architect", "structure_plan", plan)

	// Engineer + QA (with auto-repair)
	r.stage = "engineer"
	r.emit(ctx, "stage_started", map[string]any{"runId": r.runID, "stage": "engineer", "sequence": 3})
	return r.engineerQA(ctx, userIdea, plan, messages)
}

// engineerQA runs the Engineer write_file turn, creates a draft version, then
// runs the QA compile gate. On pass it promotes and completes; on fail it
// re-feeds the errors to the Engineer for up to maxAutoFixRounds repairs, then
// fails the run with COMPILE_FAILED. stableVersion is never changed on failure.
func (r *runner) engineerQA(ctx context.Context, userIdea, plan string, messages []anthropic.MessageParam) error {
	maxRounds := contracts.Load().Limits.MaxAutoFixRounds
	var prevErrors []CompileError
	qaStarted := false
	engArtificated := false
	repairsDone := 0

	for {
		r.stage = "engineer"
		var prompt string
		if repairsDone == 0 {
			prompt = engineerUserTurn(userIdea, plan)
		} else {
			prompt = engineerRepairTurn(formatCompileErrors(prevErrors))
		}
		appTSX, msgs, err := r.engineerTurn(ctx, messages, prompt)
		if err != nil {
			return err
		}
		messages = msgs

		// Create the draft version (scaffold + App.tsx) and emit file_written.
		// stable preview is NOT switched here - only PromoteDraftVersion does that.
		draft, derr := r.l.store.CreateDraftVersion(ctx, r.projectID, r.iterationID, toSnapshots(BuildFilesMap(appTSX)))
		if derr != nil {
			return derr
		}
		if r.currentDraft != "" && r.currentDraft != draft.ID {
			// Mark the previous intermediate draft failed (no run/attempt switch) so
			// the versions list is not littered with orphan drafts.
			_, _ = r.l.store.FailDraftVersion(ctx, r.currentDraft, "", "")
		}
		r.currentDraft = draft.ID
		r.emit(ctx, "file_written", map[string]any{
			"runId": r.runID, "path": writablePath, "versionDraftId": draft.ID,
		})
		// The Engineer node succeeded once it wrote the file (acceptance 2: every
		// succeeded node has a queryable artifactRef). Recorded once.
		if !engArtificated {
			r.recordArtifact(ctx, "engineer", "file_ref", draft.ID+":"+writablePath)
			engArtificated = true
		}

		// QA gate (server-side structural compile).
		r.stage = "qa"
		if !qaStarted {
			r.emit(ctx, "stage_started", map[string]any{"runId": r.runID, "stage": "qa", "sequence": 4})
			qaStarted = true
		}
		result := ValidateCompile(appTSX)
		if draft.FilesHash != nil {
			result.FilesHash = *draft.FilesHash
		}

		if result.Pass {
			ref := mustJSON(result)
			r.l.store.RecordStageArtifact(ctx, r.runID, r.attemptID, "qa", "compile_result", ref)
			r.emit(ctx, "stage_artifact", map[string]any{
				"stage": "qa", "artifactType": "compile_result", "artifactRef": ref,
			})
			v, perr := r.l.store.PromoteDraftVersion(ctx, draft.ID, r.runID, r.attemptID)
			if perr != nil {
				return perr
			}
			r.emit(ctx, "preview_ready", map[string]any{"runId": r.runID, "versionId": v.ID})
			r.emit(ctx, "run_completed", map[string]any{
				"runId": r.runID, "versionId": v.ID,
				"durationMs": int(r.l.now().Sub(r.start).Milliseconds()),
			})
			return nil
		}

		// Compile failed.
		if repairsDone >= maxRounds {
			// 3rd consecutive fail (initial + maxRounds repairs): run_failed,
			// no infinite loop, stableVersion unchanged.
			_, _ = r.l.store.FailDraftVersion(ctx, draft.ID, r.runID, r.attemptID)
			r.emit(ctx, "run_failed", map[string]any{
				"runId": r.runID, "stage": "qa", "code": "COMPILE_FAILED", "retryable": true,
			})
			return nil
		}
		prevErrors = result.Errors
		repairsDone++
	}
}

// textTurn runs one no-tool stage turn: append the user prompt, stream the
// reply (emitting message_delta), append the assistant message, return its text.
func (r *runner) textTurn(ctx context.Context, messages []anthropic.MessageParam, userPrompt string) (string, []anthropic.MessageParam, error) {
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)))
	msg, err := r.stream(ctx, messages, nil, anthropic.ToolChoiceUnionParam{})
	if err != nil {
		return "", messages, err
	}
	messages = append(messages, assistantParam(msg))
	var sb []byte
	for _, b := range msg.Content {
		if b.Type == "text" {
			sb = append(sb, b.Text...)
		}
	}
	return string(sb), messages, nil
}

// engineerTurn runs one Engineer turn with the write_file tool. It drives the
// tool-use loop: stream -> execute tool calls -> return tool results -> repeat
// until the model ends its turn. Returns the last validly-written App.tsx
// content ("" if the model never wrote a valid file).
func (r *runner) engineerTurn(ctx context.Context, messages []anthropic.MessageParam, userPrompt string) (string, []anthropic.MessageParam, error) {
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)))
	tools := writeFileTool()
	tc := anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	lim := contracts.Load().Limits.FileContentMaxBytes
	var appTSX string

	// Bound the tool rounds so a misbehaving model cannot loop forever.
	for iter := 0; iter < 8; iter++ {
		msg, err := r.stream(ctx, messages, tools, tc)
		if err != nil {
			return appTSX, messages, err
		}
		messages = append(messages, assistantParam(msg))
		if msg.StopReason != anthropic.StopReasonToolUse {
			break // end_turn (or max_tokens): turn complete
		}
		var results []anthropic.ContentBlockParamUnion
		for _, b := range msg.Content {
			if b.Type != "tool_use" || b.Name != "write_file" {
				continue
			}
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(b.Input, &in); err != nil {
				results = append(results, anthropic.NewToolResultBlock(b.ID, "ERROR: malformed write_file input", true))
				continue
			}
			// Sandbox: only /src/App.tsx writable; reject traversal (422 source),
			// oversize, and forbidden shell/install tokens. The model can retry.
			if vErr := ValidateFilePath(in.Path); vErr != nil {
				results = append(results, anthropic.NewToolResultBlock(b.ID, "ERROR (422): "+vErr.Error(), true))
				continue
			}
			if len(in.Content) > lim {
				results = append(results, anthropic.NewToolResultBlock(b.ID, "ERROR (422): content exceeds size limit", true))
				continue
			}
			if pat, bad := forbiddenInCode(in.Content); bad {
				results = append(results, anthropic.NewToolResultBlock(b.ID, "ERROR (422): forbidden token "+pat+" (no shell/install)", true))
				continue
			}
			appTSX = in.Content
			results = append(results, anthropic.NewToolResultBlock(b.ID, "ok: wrote "+in.Path, false))
		}
		if len(results) == 0 {
			break // no tool calls to answer; turn complete
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	return appTSX, messages, nil
}

// stream is the single Claude streaming turn. It builds the params, calls the
// injectable streamCall (real SDK in prod, fake in tests), and emits
// message_delta events for text chunks (keeping the watchdog fresh). tools/tc
// are set only for tool turns.
func (r *runner) stream(ctx context.Context, messages []anthropic.MessageParam, tools []anthropic.ToolUnionParam, tc anthropic.ToolChoiceUnionParam) (anthropic.Message, error) {
	params := anthropic.MessageNewParams{
		Model:     r.l.model,
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  messages,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
	}
	if tools != nil {
		params.Tools = tools
		params.ToolChoice = tc
	}
	return r.l.stream(ctx, params, func(text string) {
		r.msgSeq++
		r.emit(ctx, "message_delta", map[string]any{
			"runId": r.runID, "text": text, "seq": r.msgSeq,
		})
	})
}

// writeFileTool returns the write_file tool definition. The schema forces path
// + content; the server enforces that path == /src/App.tsx (422 otherwise).
func writeFileTool() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{{
		OfTool: &anthropic.ToolParam{
			Name:        "write_file",
			Description: anthropic.String(`Write the full contents of /src/App.tsx. The ONLY writable path is "/src/App.tsx". Provide raw TypeScript/TSX source (no markdown fences, no shell, no npm).`),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":    map[string]any{"type": "string", "description": `Must be "/src/App.tsx".`},
					"content": map[string]any{"type": "string", "description": "Full TypeScript/TSX source of App.tsx, including a default export."},
				},
				Required: []string{"path", "content"},
			},
			Type: anthropic.ToolTypeCustom,
		},
	}}
}

// assistantParam reconstructs an assistant MessageParam from an accumulated
// response Message so the turn can be echoed back for multi-turn tool use.
// Only text and tool_use blocks occur (thinking is disabled).
func assistantParam(msg anthropic.Message) anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			blocks = append(blocks, anthropic.NewTextBlock(b.Text))
		case "tool_use":
			blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, b.Input, b.Name))
		case "thinking":
			blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Thinking))
		case "redacted_thinking":
			blocks = append(blocks, anthropic.NewRedactedThinkingBlock(b.Data))
		}
	}
	return anthropic.NewAssistantMessage(blocks...)
}

// emit persists one SSE event (monotonic seq) and refreshes the watchdog clock.
// Errors are logged but not fatal - events are observability; the version
// transition is the source of truth.
func (r *runner) emit(ctx context.Context, eventType string, payload map[string]any) error {
	_, err := r.l.store.AppendEvent(ctx, r.runID, eventType, payload)
	if err != nil {
		r.l.logf("agent: emit %s failed: %v", eventType, err)
		return nil
	}
	r.mu.Lock()
	r.lastEvent = r.l.now()
	r.mu.Unlock()
	return nil
}

// emitBG persists a terminal event with Background context (the run ctx may be
// cancelled by the watchdog).
func (r *runner) emitBG(eventType string, payload map[string]any) {
	_, err := r.l.store.AppendEvent(context.Background(), r.runID, eventType, payload)
	if err != nil {
		r.l.logf("agent: emitBG %s failed: %v", eventType, err)
	}
}

// failInfra fails the run on an infra error: if a draft exists, fail it (and
// the run/attempt); otherwise just fail the run. stableVersion is untouched.
func (r *runner) failInfra() {
	if r.currentDraft != "" {
		_, _ = r.l.store.FailDraftVersion(context.Background(), r.currentDraft, r.runID, r.attemptID)
		return
	}
	_ = r.l.store.FailRun(context.Background(), r.runID)
}

// persistAssistant records an assistant chat message (PM spec / Architect plan)
// so the project's message thread reflects the conversation.
func (r *runner) persistAssistant(content string) {
	if content == "" {
		return
	}
	if _, err := r.l.store.AppendMessage(context.Background(), r.projectID, "assistant", content); err != nil {
		r.l.logf("agent: persistAssistant failed: %v", err)
	}
}

// recordArtifact binds a stage artifact and emits the stage_artifact event.
func (r *runner) recordArtifact(ctx context.Context, stage, artifactType, artifactRef string) {
	if _, err := r.l.store.RecordStageArtifact(ctx, r.runID, r.attemptID, stage, artifactType, artifactRef); err != nil {
		r.l.logf("agent: recordArtifact %s failed: %v", stage, err)
	}
	r.emit(ctx, "stage_artifact", map[string]any{
		"stage": stage, "artifactType": artifactType, "artifactRef": artifactRef,
	})
}

// toSnapshots converts the agent FileEntry map to store FileSnapshots.
func toSnapshots(entries []FileEntry) []store.FileSnapshot {
	out := make([]store.FileSnapshot, 0, len(entries))
	for _, e := range entries {
		out = append(out, store.FileSnapshot{Path: e.Path, Content: e.Content, Readonly: e.Readonly})
	}
	return out
}

// mustJSON marshals v (best-effort; empty string on error).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
