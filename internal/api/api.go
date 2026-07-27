// Package api wires the HTTP routes for the Vibe Forge backend. Stage 1 ships
// only GET /api/health for real; the remaining contract paths are registered as
// 501 stubs so the router is a faithful scaffold of contracts/contract.json and
// the stable error shape is demonstrated end-to-end. Stage 2/3 (FLO-55, FLO-60)
// replace the stubs with real handlers.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/OrcaxNet/vibe-forge/contracts"
	"github.com/OrcaxNet/vibe-forge/internal/db"
)

// Version is the backend build version. Bumped per release.
const Version = "0.1.0-skeleton"

// ContractVersion returns the shared contract version the backend is built
// against (sourced from the embedded contract.json).
func ContractVersion() string { return contracts.Version() }

// Server holds runtime dependencies. A nil db means SQLite is not configured.
type Server struct {
	db         *sql.DB
	dbPath     string
	modelKey   string
	modelName  string
	now        func() time.Time
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
	}
	return s, nil
}

// Close releases resources.
func (s *Server) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Router returns the HTTP handler for all contract paths.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)

	// Contract path stubs: present so the router mirrors contract.json and the
	// stable error shape is exercised. Replaced by FLO-55 / FLO-60.
	stub := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			writeError(w, "NOT_IMPLEMENTED", name+" is not implemented in the Stage 1 skeleton", false, http.StatusNotImplemented)
		}
	}
	mux.HandleFunc("POST /api/projects", stub("createProject"))
	mux.HandleFunc("GET /api/projects", stub("listProjects"))
	mux.HandleFunc("GET /api/projects/{id}", stub("getProject"))
	mux.HandleFunc("POST /api/projects/{id}/runs", stub("createRun"))
	mux.HandleFunc("GET /api/runs/{id}/events", stub("runEvents"))
	mux.HandleFunc("POST /api/runs/{id}/retry", stub("retryRun"))
	mux.HandleFunc("GET /api/projects/{id}/files", stub("listFiles"))
	mux.HandleFunc("PUT /api/projects/{id}/files/src/App.tsx", stub("writeFile"))
	mux.HandleFunc("GET /api/projects/{id}/versions/{versionId}/files", stub("versionFiles"))
	mux.HandleFunc("POST /api/runs/{id}/compile-result", stub("compileResult"))
	mux.HandleFunc("GET /api/projects/{id}/versions", stub("listVersions"))
	mux.HandleFunc("POST /api/projects/{id}/versions/{versionId}/restore", stub("restoreVersion"))
	return mux
}

// Health is the dependency health body (PRD-C §5.1).
type Health struct {
	Status          string         `json:"status"` // "healthy" | "unhealthy"
	ContractVersion string         `json:"contractVersion"`
	Version         string         `json:"version"`
	Dependencies    Deps           `json:"dependencies"`
	Time            string         `json:"time"`
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

	// Database: ping if configured, else report not_configured.
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

	// Model: configured iff the API key is present. We do not probe upstream in
	// the skeleton (agent loop is Stage 3); we only report configuration state.
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// unused guard: writeContractError is part of the scaffold's public surface for
// Stage 2/3 handlers; keep the symbol so importers can rely on it.
var _ = writeContractError
