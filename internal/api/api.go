// Package api wires the HTTP routes for the Vibe Forge backend against the
// SQLite store (package store). FLO-55 implements the persistence-layer
// endpoints (projects, runs, versions, files, restore, manual edit) for real;
// the agent-loop endpoints (SSE, retry, compile-result) remain stubs for
// FLO-60. GET /api/health is real and reports sanitized dependency state.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/OrcaxNet/vibe-forge/contracts"
	"github.com/OrcaxNet/vibe-forge/internal/agent"
	"github.com/OrcaxNet/vibe-forge/internal/db"
	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// Version is the backend build version. Bumped per release.
const Version = "0.2.0"

// ContractVersion returns the shared contract version the backend is built
// against (sourced from the embedded contract.json).
func ContractVersion() string { return contracts.Version() }

// LoopRunner runs the agent loop for one run in the background. The SSE stream
// and run status are the observable surface; Run must be non-blocking (it is
// launched as a goroutine) and must reach a terminal run state. Decoupled from
// the concrete *agent.Loop so tests can inject a fake without hitting the API.
type LoopRunner interface {
	Run(ctx context.Context, runID string)
}

// Server holds runtime dependencies. A nil store means SQLite is not configured;
// mutating endpoints then return 503 DEPENDENCY_UNAVAILABLE.
type Server struct {
	db        *sql.DB
	store     *store.Store
	dbPath    string
	modelKey  string
	modelName string
	now       func() time.Time
	loop      LoopRunner // nil until InitLoop; createRun/retry launch it if set
	loopCtx   context.Context
	loopStop  context.CancelFunc
}

// New prepares the server. If DATABASE_PATH is set it opens and migrates SQLite
// now; a migration failure is returned so the caller refuses to start (PRD-C:
// migration failure MUST stop startup).
func New(ctx context.Context) (*Server, error) {
	s := &Server{
		dbPath:    os.Getenv("DATABASE_PATH"),
		modelKey:  os.Getenv("ANTHROPIC_API_KEY"),
		modelName: os.Getenv("ANTHROPIC_MODEL"),
		now:       func() time.Time { return time.Now().UTC() },
	}
	// The loop context outlives any single HTTP request (the agent run continues
	// after the 202 is returned) but is cancelled on Close/shutdown.
	loopCtx, loopStop := context.WithCancel(ctx)
	s.loopCtx = loopCtx
	s.loopStop = loopStop
	if s.dbPath != "" {
		d, err := db.Open(s.dbPath)
		if err != nil {
			return nil, err
		}
		if err := db.Migrate(d); err != nil {
			_ = d.Close()
			return nil, err
		}
		s.db = d
		s.store = store.New(d)
	}
	return s, nil
}

// InitLoop constructs and installs the real agent loop (FLO-60) when the store
// and model key are configured. Called by main after New. If the model is not
// configured, run creation stays rejected and no loop is wired (the rest of the
// API still serves). Returns an error only if the store is configured but the
// loop cannot be built.
func (s *Server) InitLoop() error {
	if s.store == nil || s.modelKey == "" {
		return nil
	}
	loop, err := agent.NewLoop(s.store, agent.LoopConfig{
		APIKey: s.modelKey,
		Model:  s.modelName,
	})
	if err != nil {
		return err
	}
	s.loop = loop
	return nil
}

// SetLoop installs a custom LoopRunner (used by tests to inject a fake).
func (s *Server) SetLoop(r LoopRunner) { s.loop = r }

// ReconcileInterruptedRuns flips any queued/running run to 'interrupted' so a
// crash never leaves a run stuck "active" (C-FR-06/07). Called on startup.
func (s *Server) ReconcileInterruptedRuns(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.MarkActiveRunsInterrupted(ctx)
}

// Close releases resources. It cancels the loop context so in-flight agent runs
// stop (they are reconciled as 'interrupted' on the next startup).
func (s *Server) Close() error {
	if s.loopStop != nil {
		s.loopStop()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Router returns the HTTP handler for all contract paths.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)

	// Persistence-layer endpoints (FLO-55).
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("POST /api/projects/{id}/runs", s.createRun)
	mux.HandleFunc("GET /api/projects/{id}/files", s.listFiles)
	mux.HandleFunc("PUT /api/projects/{id}/files/src/App.tsx", s.writeFile)
	mux.HandleFunc("PUT /api/projects/{id}/files/{path...}", s.writeFileIllegalPath)
	mux.HandleFunc("GET /api/projects/{id}/versions", s.listVersions)
	mux.HandleFunc("GET /api/projects/{id}/versions/{versionId}/files", s.versionFiles)
	mux.HandleFunc("POST /api/projects/{id}/versions/{versionId}/restore", s.restoreVersion)

	// Agent-loop endpoints (FLO-60).
	mux.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	mux.HandleFunc("POST /api/runs/{id}/retry", s.retryRun)
	mux.HandleFunc("POST /api/runs/{id}/compile-result", s.compileResult)
	return mux
}

// Health is the dependency health body (PRD-C §5.1).
type Health struct {
	Status          string `json:"status"` // "healthy" | "unhealthy"
	ContractVersion string `json:"contractVersion"`
	Version         string `json:"version"`
	Dependencies    Deps   `json:"dependencies"`
	Time            string `json:"time"`
}

// Deps reports per-dependency status with sanitized details.
type Deps struct {
	Database Dep `json:"database"`
	Model    Dep `json:"model"`
}

// Dep is one dependency's status. Detail is sanitized (no keys/paths/secrets).
type Dep struct {
	Status string `json:"status"` // "ok" | "not_configured" | "unavailable" | "configured"
	Detail string `json:"detail,omitempty"`
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := Health{
		ContractVersion: contracts.Version(),
		Version:         Version,
		Time:            s.now().Format(time.RFC3339),
	}

	switch {
	case s.dbPath == "":
		h.Dependencies.Database = Dep{Status: "not_configured"}
	case s.db != nil:
		if err := s.db.PingContext(r.Context()); err != nil {
			h.Dependencies.Database = Dep{Status: "unavailable", Detail: "database ping failed"}
		} else {
			h.Dependencies.Database = Dep{Status: "ok"}
		}
	default:
		h.Dependencies.Database = Dep{Status: "unavailable", Detail: "database not opened"}
	}

	if s.modelKey == "" {
		h.Dependencies.Model = Dep{Status: "not_configured"}
	} else {
		h.Dependencies.Model = Dep{Status: "configured"}
	}

	healthy := h.Dependencies.Database.Status == "ok" && h.Dependencies.Model.Status == "configured"
	if healthy {
		h.Status = "healthy"
		writeJSON(w, http.StatusOK, h)
		return
	}
	h.Status = "unhealthy"
	writeJSON(w, http.StatusServiceUnavailable, h)
}

// APIError is the stable error structure (contract §errors.structure).
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, code, msg string, retryable bool, status int) {
	writeJSON(w, status, APIError{Code: code, Message: msg, Retryable: retryable})
}

// writeContractError maps a stable contract code to its HTTP status automatically.
func writeContractError(w http.ResponseWriter, code, msg string) {
	status := contracts.HTTPStatusFor(code)
	writeJSON(w, status, APIError{Code: code, Message: msg, Retryable: contracts.IsRetryable(code)})
}

// writeStoreErr maps a store error to the stable error shape. Store *store.Error
// values carry a contract code; anything else is a sanitized INTERNAL 500 (no
// stack, key or internal address leaks - C-NFR-03).
func (s *Server) writeStoreErr(w http.ResponseWriter, err error) {
	var se *store.Error
	if errors.As(err, &se) {
		writeJSON(w, contracts.HTTPStatusFor(se.Code), APIError{
			Code: se.Code, Message: se.Message,
			Retryable: contracts.IsRetryable(se.Code), Details: se.Details,
		})
		return
	}
	writeContractError(w, "INTERNAL", "internal error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeRaw writes a pre-serialized response body (used for idempotent create
// endpoints whose body is the exact bytes to replay).
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// requireStore returns the store or writes 503 if SQLite is not configured.
func (s *Server) requireStore(w http.ResponseWriter) *store.Store {
	if s.store == nil {
		writeContractError(w, "DEPENDENCY_UNAVAILABLE", "database is not configured")
		return nil
	}
	return s.store
}

// idempotencyKey reads the Idempotency-Key header (canonical), falling back to a
// body field of the same name.
func idempotencyKey(r *http.Request, bodyKey string) string {
	if k := r.Header.Get(contracts.Load().Idempotency.Header); k != "" {
		return k
	}
	return bodyKey
}

// requireKey writes 422 if the idempotency key is empty.
func requireKey(w http.ResponseWriter, key string) bool {
	if key == "" {
		writeContractError(w, "VALIDATION_ERROR", "Idempotency-Key is required")
		return false
	}
	return true
}

// decodeJSON decodes a JSON request body, treating an empty body as a validation
// error rather than a transport error.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return err
	}
	return nil
}
