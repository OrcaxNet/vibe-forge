package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// sseEvent is one parsed SSE block from the response body.
type sseEvent struct {
	id    int
	event string
}

// parseSSE splits an SSE response body into events.
func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "id: "):
				n, _ := strconv.Atoi(strings.TrimPrefix(line, "id: "))
				ev.id = n
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			}
		}
		out = append(out, ev)
	}
	return out
}

// TestSSEReplayAndDedup (A-FR-07): the SSE stream replays events from
// Last-Event-ID with no dup and no reorder, and closes after the terminal event.
func TestSSEReplayAndDedup(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)
	_, rb := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	var run struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &run)

	// Seed a complete event timeline ending in a terminal run_completed, and flip
	// the run to succeeded (the loop would do this in production).
	ctx := context.Background()
	srv.store.AppendEvent(ctx, run.RunID, "run_started", map[string]any{"runId": run.RunID, "projectId": p.ID})
	srv.store.AppendEvent(ctx, run.RunID, "stage_started", map[string]any{"runId": run.RunID, "stage": "pm", "sequence": 1})
	srv.store.AppendEvent(ctx, run.RunID, "run_completed", map[string]any{"runId": run.RunID, "versionId": "v1", "durationMs": 123})
	srv.store.SetRunStatus(ctx, run.RunID, "succeeded")

	// Fresh connect (no Last-Event-ID): all 3 events, in seq order, terminal last.
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.RunID+"/events", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("SSE status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	evs := parseSSE(t, rec.Body.String())
	if len(evs) != 3 {
		t.Fatalf("fresh connect got %d events, want 3: %+v", len(evs), evs)
	}
	for i, e := range evs {
		if e.id != i+1 {
			t.Errorf("event[%d] id = %d, want %d (monotonic, no reorder)", i, e.id, i+1)
		}
	}
	if evs[len(evs)-1].event != "run_completed" {
		t.Errorf("last event = %q, want run_completed (terminal close)", evs[len(evs)-1].event)
	}

	// Reconnect from Last-Event-ID: 1 -> only events 2 and 3 (no dup of 1).
	req2 := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.RunID+"/events", nil)
	req2.Header.Set("Last-Event-ID", "1")
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	evs2 := parseSSE(t, rec2.Body.String())
	if len(evs2) != 2 {
		t.Fatalf("replay from 1 got %d events, want 2: %+v", len(evs2), evs2)
	}
	if evs2[0].id != 2 || evs2[1].id != 3 {
		t.Errorf("replay ids = %d,%d, want 2,3 (no dup, no gap)", evs2[0].id, evs2[1].id)
	}
}

// TestSSEInterruptedRunSynthesizesClose: a run left 'interrupted' (crash) with
// no persisted terminal event gets a synthesized retryable run_failed so the
// client can retry (C-FR-06/07).
func TestSSEInterruptedRunSynthesizesClose(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)
	_, rb := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	var run struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &run)

	// No terminal event; run interrupted (as MarkActiveRunsInterrupted would do).
	srv.store.SetRunStatus(context.Background(), run.RunID, "interrupted")

	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.RunID+"/events", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	evs := parseSSE(t, rec.Body.String())
	if len(evs) != 1 || evs[0].event != "run_failed" {
		t.Fatalf("expected one synthesized run_failed, got %+v", evs)
	}
}

// TestRetryRunNotRetryable: retrying a run that is still active (queued/running)
// returns 409 CONFLICT (contract §paths.retryRun).
func TestRetryRunNotRetryable(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)
	_, rb := doJSON(t, srv, "POST", "/api/projects/"+p.ID+"/runs", "rk",
		map[string]string{"prompt": "build an app"})
	var run struct {
		RunID string `json:"runId"`
	}
	json.Unmarshal(rb, &run)

	// Run is still 'queued' (no loop wired in this test server) -> not retryable.
	s, b := doJSON(t, srv, "POST", "/api/runs/"+run.RunID+"/retry", "rky",
		map[string]any{"attemptId": "any", "idempotencyKey": "rky"})
	if s != 409 {
		t.Fatalf("retry status = %d, want 409 (body=%s)", s, b)
	}
	var e APIError
	json.Unmarshal(b, &e)
	if e.Code != "CONFLICT" {
		t.Errorf("error code = %q, want CONFLICT", e.Code)
	}
}

// TestWriteFileIllegalPath422: PUT to any file path other than /src/App.tsx
// returns 422 VALIDATION_ERROR (acceptance 3).
func TestWriteFileIllegalPath422(t *testing.T) {
	srv := newAPITestServer(t)
	_, body := doJSON(t, srv, "POST", "/api/projects", "pk",
		map[string]string{"initialPrompt": "build an app"})
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &p)

	illegal := []string{
		"/api/projects/" + p.ID + "/files/src/main.tsx",
		"/api/projects/" + p.ID + "/files/src/components/Foo.tsx",
		"/api/projects/" + p.ID + "/files/index.html",
	}
	for _, path := range illegal {
		s, b := doJSON(t, srv, "PUT", path, "wk", map[string]any{"content": "x"})
		if s != 422 {
			t.Errorf("PUT %s status = %d, want 422 (body=%s)", path, s, b)
		}
		var e APIError
		json.Unmarshal(b, &e)
		if e.Code != "VALIDATION_ERROR" {
			t.Errorf("PUT %s code = %q, want VALIDATION_ERROR", path, e.Code)
		}
	}
}
