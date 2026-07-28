package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestApplyStreamEventToolUseDeltaRefreshesWatchdog is the unit-level proof of
// the FLO-60 Engineer-TIMEOUT fix. realStream previously refreshed the run
// watchdog only on text_delta (via onDelta). A write_file tool_use streams
// input_json_delta events - no text_delta - so a long generation tripped the
// watchdog. The fix routes every content-progress event through onActivity
// (watchdog refresh); onDelta stays text-only.
//
// applyStreamEvent is the per-event dispatch extracted from realStream's loop,
// so this exercises the exact production logic without an HTTP server.
func TestApplyStreamEventToolUseDeltaRefreshesWatchdog(t *testing.T) {
	cases := []struct {
		name      string
		eventJSON string
		wantAct   bool   // onActivity (watchdog refresh) expected
		wantDelta string // onDelta text expected ("" = not called)
	}{
		{
			"tool_use input_json_delta (the bug)",
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/src/App.tsx\"}"}}`,
			true, "",
		},
		{
			"text_delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			true, "hello",
		},
		{
			"content_block_start (tool_use block opens)",
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write_file","input":{}}}`,
			true, "",
		},
		{
			"message_start (no content progress -> no refresh)",
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"x","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			false, "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ev anthropic.MessageStreamEventUnion
			if err := json.Unmarshal([]byte(c.eventJSON), &ev); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			var msg anthropic.Message
			gotAct := false
			var gotDelta string
			applyStreamEvent(&msg, ev, func(s string) { gotDelta = s }, func() { gotAct = true })
			if gotAct != c.wantAct {
				t.Errorf("onActivity called=%v, want %v (watchdog refresh on content progress)", gotAct, c.wantAct)
			}
			if gotDelta != c.wantDelta {
				t.Errorf("onDelta text=%q, want %q", gotDelta, c.wantDelta)
			}
		})
	}
}

// TestEngineerToolUseWatchdogRepro reproduces the FLO-58 ~100s Engineer TIMEOUT
// in milliseconds and proves the fix's before/after difference (re-acceptance
// requirement 1: a stable, controllable repro stating root cause and the
// before/after delta).
//
// During the Engineer write_file turn the model streams input_json_delta events
// (the App.tsx content) with NO text_delta. Pre-fix realStream refreshed the
// watchdog only on text_delta, so a tool_use generation longer than
// runWatchdogSeconds tripped the watchdog -> run_failed TIMEOUT. Post-fix
// realStream refreshes on every content-progress event, so an active generation
// completes.
//
// slowToolUseStream simulates the Engineer turn as a long burst of input_json
// deltas spread over genDur (> watchdog). refresh mirrors pre-fix (no onActivity,
// only text_delta ever refreshed) vs post-fix (onActivity per delta) realStream.
// The watchdog is shortened to 150ms so the ~100s TIMEOUT reproduces fast.
func TestEngineerToolUseWatchdogRepro(t *testing.T) {
	const genDur = 500 * time.Millisecond // tool_use generation window (> watchdog)

	// Before the fix: tool_use input_json_delta does not refresh the watchdog,
	// so the long generation trips it -> run_failed TIMEOUT (retryable).
	t.Run("before_fix_times_out", func(t *testing.T) {
		l, st, _, runID := setupLoop(t, slowToolUseStream(false, genDur))
		l.watchdogOverride = 150 * time.Millisecond
		l.Run(context.Background(), runID)

		run, err := st.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "failed" {
			t.Fatalf("run status = %q, want failed before fix", run.Status)
		}
		if code := lastRunFailedCode(context.Background(), st, runID); code != "TIMEOUT" {
			t.Fatalf("terminal code = %q, want TIMEOUT before fix", code)
		}
		// stableVersion must be untouched by an infra failure.
	})

	// After the fix: every content-progress event refreshes the watchdog, so the
	// same long tool_use generation completes and the run succeeds.
	t.Run("after_fix_succeeds", func(t *testing.T) {
		l, st, _, runID := setupLoop(t, slowToolUseStream(true, genDur))
		l.watchdogOverride = 150 * time.Millisecond
		l.Run(context.Background(), runID)

		run, err := st.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "succeeded" {
			t.Fatalf("run status = %q, want succeeded after fix", run.Status)
		}
	})
}

// slowToolUseStream is a streamCall that routes PM/Architect/tool_result turns
// instantly and simulates the Engineer write_file turn as a long burst of
// input_json_delta events (no text_delta) spread over genDur. When refresh is
// true it invokes onActivity per delta (the post-fix realStream contract); when
// false it does not (the pre-fix contract, which only refreshed on text_delta).
// It respects ctx cancellation so the watchdog can interrupt the pre-fix case.
func slowToolUseStream(refresh bool, genDur time.Duration) streamCall {
	return func(ctx context.Context, params anthropic.MessageNewParams, _ func(string), onActivity func()) (anthropic.Message, error) {
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
			return textMsg("spec"), nil
		case strings.Contains(text, "Architect"):
			return textMsg("plan"), nil
		default: // Engineer turn: a long tool_use input_json_delta stream.
			if err := streamToolUseSlowly(ctx, genDur, refresh, onActivity); err != nil {
				return anthropic.Message{}, err
			}
			return toolUseMsg(1, goodAppTSX), nil
		}
	}
}

// streamToolUseSlowly emits one content_block_start then input_json_delta
// events spread over dur, invoking onActivity for each when refresh is true. It
// returns ctx.Err() if the context is cancelled (watchdog), letting the run
// classify the interruption as TIMEOUT.
func streamToolUseSlowly(ctx context.Context, dur time.Duration, refresh bool, onActivity func()) error {
	if refresh && onActivity != nil {
		onActivity() // content_block_start
	}
	deadline := time.Now().Add(dur)
	interval := dur / 8
	if interval <= 0 {
		interval = dur
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if refresh && onActivity != nil {
				onActivity() // input_json_delta
			}
		}
	}
}
