package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OrcaxNet/vibe-forge/contracts"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// agent_handlers.go implements the FLO-60 agent-loop HTTP surface: the SSE
// event stream (with Last-Event-ID replay), run retry, and the compile-result
// callback. createRun (handlers.go) launches the loop; this file serves the
// observable/streaming side.

// terminalEventTypes are the SSE events that close a stream (contract
// §events.protocol.terminalEvents).
var terminalEventTypes = map[string]bool{
	"run_completed": true,
	"run_failed":    true,
}

// runEvents: GET /api/runs/:id/events (SSE).
//
// Replays all events with seq > Last-Event-ID, then polls for live events until
// a terminal event (run_completed/run_failed) is sent or the client disconnects.
// If a run already ended without a persisted terminal event (e.g. it was
// 'interrupted' by a crash before FLO-60 reconciled it), a terminal run_failed
// is synthesized so the client is never left hanging. Events are persisted with
// monotonic per-run seq (store.AppendEvent), so a reconnect resumes from
// Last-Event-ID with no dup and no reorder (A-FR-07).
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	runID := r.PathValue("id")
	if _, err := st.GetRun(r.Context(), runID); err != nil {
		s.writeStoreErr(w, err)
		return
	}

	afterSeq := 0
	if h := r.Header.Get(contracts.Load().Events.Protocol.ReplayHeader); h != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n > 0 {
			afterSeq = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	lastSent := afterSeq
	sentTerminal := false
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Drain everything since lastSent.
		events, err := st.ListEvents(r.Context(), runID, lastSent)
		if err != nil {
			return
		}
		for _, e := range events {
			writeSSE(w, e)
			lastSent = e.Seq
			if terminalEventTypes[e.Type] {
				sentTerminal = true
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if sentTerminal {
			return
		}

		// If the run is already in a terminal state with no terminal event
		// (crashed/interrupted before the loop emitted one), synthesize a
		// retryable run_failed so the client is not left hanging and can retry.
		// A succeeded run always has a persisted run_completed; if it was not
		// drained (e.g. reconnect past it), just close.
		run, err := st.GetRun(r.Context(), runID)
		if err != nil {
			return
		}
		if isRunTerminal(run.Status) {
			if run.Status == "failed" || run.Status == "interrupted" {
				synthesizeTerminal(w, run)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// writeSSE writes one event in the SSE wire format. The id line carries the
// monotonic seq so a reconnect's Last-Event-ID resumes after it.
func writeSSE(w http.ResponseWriter, e store.Event) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		payload = []byte(`{}`)
	}
	// Each field on its own line; blank line terminates the event.
	w.Write([]byte("id: "))
	w.Write([]byte(strconv.Itoa(e.Seq)))
	w.Write([]byte("\nevent: "))
	w.Write([]byte(e.Type))
	w.Write([]byte("\ndata: "))
	w.Write(payload)
	w.Write([]byte("\n\n"))
}

// synthesizeTerminal writes a retryable run_failed for a run that ended without
// a persisted terminal event (interrupted/failed by a crash). Transient (not
// persisted): each connection reconnecting to an interrupted run re-synthesizes.
func synthesizeTerminal(w http.ResponseWriter, run store.Run) {
	w.Write([]byte("event: run_failed\ndata: "))
	json.NewEncoder(w).Encode(map[string]any{
		"runId": run.ID, "stage": nil, "code": "INTERNAL", "retryable": true,
	})
	w.Write([]byte("\n\n"))
}

// isRunTerminal reports whether a run status is terminal for streaming purposes
// (no further live events are expected from the agent loop).
func isRunTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "interrupted":
		return true
	}
	return false
}

// retryRun: POST /api/runs/:id/retry. Starts a fresh attempt for a
// failed/interrupted run and re-launches the agent loop. Idempotent on
// Idempotency-Key; 409 if the run is not retryable (contract §paths.retryRun).
func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	if s.modelKey == "" {
		writeContractError(w, "DEPENDENCY_UNAVAILABLE", "model is not configured")
		return
	}
	var req struct {
		AttemptID      string `json:"attemptId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeContractError(w, "VALIDATION_ERROR", err.Error())
		return
	}
	key := idempotencyKey(r, req.IdempotencyKey)
	if !requireKey(w, key) {
		return
	}
	runID := r.PathValue("id")
	status, body, replayed, err := st.RetryRun(r.Context(), runID, key)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !replayed && s.loop != nil {
		go s.loop.Run(s.loopCtx, runID)
	}
	writeRaw(w, status, body)
}

// compileResult: POST /api/runs/:id/compile-result. Internal/protected callback
// for the Sandpack frontend to report its real compile result (FLO-56). Pure
// idempotent recorder: it records a qa compile_result artifact for the named
// attempt and returns 200. It does not change run state - the agent loop's own
// QA gate drives promotion/failure. (FLO-60's backend QA is self-sufficient;
// this endpoint forward-enables the browser compiler.)
func (s *Server) compileResult(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	var req struct {
		Attempt   int               `json:"attempt"`
		Errors    []compileErrorDTO `json:"errors"`
		FilesHash string            `json:"filesHash"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeContractError(w, "VALIDATION_ERROR", err.Error())
		return
	}
	if req.Attempt < 1 {
		writeContractError(w, "VALIDATION_ERROR", "attempt (int >= 1) is required")
		return
	}
	runID := r.PathValue("id")
	att, err := st.GetAttemptBySequence(r.Context(), runID, req.Attempt)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	ref, _ := json.Marshal(map[string]any{
		"attempt":   req.Attempt,
		"errors":    req.Errors,
		"filesHash": req.FilesHash,
		"pass":      len(req.Errors) == 0,
	})
	if _, err := st.RecordCompileResult(r.Context(), runID, att.ID, string(ref)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// compileErrorDTO is the structured compile error (B-FR-06: file/line/message)
// accepted by the compile-result callback.
type compileErrorDTO struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}
