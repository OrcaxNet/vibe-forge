// Package store owns all typed access to the SQLite source of truth for Vibe
// Forge. It implements the PRD-C data model (§5) and the transaction boundaries
// the contract pins (§concurrency / §storage):
//
//   - Single active run per project (partial UNIQUE index + explicit check).
//   - Idempotency-Key ledger: a repeated key within ttlSeconds replays the
//     original 2xx result with no duplicate side effect.
//   - Optimistic lock on baseVersionId (CAS inside the success transaction).
//   - Atomic success transaction: file snapshot + iteration result + stable
//     version switch commit in one tx; any step failing rolls everything back
//     and leaves stableVersionId untouched.
//
// The HTTP layer (package api) depends only on this package and the contract;
// it never writes SQL. FLO-60 (agent loop / SSE) builds on these primitives.
package store

import (
	"database/sql"
	"time"

	"github.com/OrcaxNet/vibe-forge/contracts"
	"github.com/google/uuid"
)

// Store is the typed data-access layer over a single SQLite connection.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New wraps an already-opened, migrated database with the default UTC clock.
func New(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// NewWithClock is for tests that need a controllable clock (ordering, TTL).
func NewWithClock(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

// DB exposes the underlying connection for callers (e.g. FLO-60 SSE replay) that
// need direct read access. Writes must still go through Store methods.
func (s *Store) DB() *sql.DB { return s.db }

// nowTS returns the current time as UTC ISO-8601 (contract §storage.timeFormat).
func (s *Store) nowTS() string { return s.now().Format(time.RFC3339) }

// newID returns an unpredictable UUID v4 (contract §storage.idFormat).
func newID() string { return uuid.NewString() }

// Error is a contract-coded error. The HTTP layer maps Code to status and
// serializes Message/Details verbatim (contract §errors.structure).
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Is lets errors.Is match by Code so callers can test for store.ErrNotFound etc.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Sentinel errors for the contract error codes.
var (
	ErrNotFound   = &Error{Code: "NOT_FOUND", Message: "not found"}
	ErrConflict   = &Error{Code: "CONFLICT", Message: "conflict"}
	ErrValidation = &Error{Code: "VALIDATION_ERROR", Message: "validation error"}
)

func notFound(msg string) *Error { return &Error{Code: "NOT_FOUND", Message: msg} }

func conflict(msg string, details map[string]any) *Error {
	return &Error{Code: "CONFLICT", Message: msg, Details: details}
}

func validation(msg string, details map[string]any) *Error {
	return &Error{Code: "VALIDATION_ERROR", Message: msg, Details: details}
}

// --- Entity types (JSON shapes mirror contracts/contract.json §models) ---

// Project is the top-level build space.
type Project struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	StableVersionID *string `json:"stableVersionId"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

// Message is one chat turn (user or assistant).
type Message struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

// Run is one generation task.
type Run struct {
	ID              string               `json:"id"`
	ProjectID       string               `json:"projectId"`
	Status          string               `json:"status"`
	Prompt          string               `json:"prompt"`
	BaseVersionID   *string              `json:"baseVersionId"`
	ActiveAttemptID *string              `json:"activeAttemptId"`
	CreatedAt       string               `json:"createdAt"`
	UpdatedAt       string               `json:"updatedAt"`
	Stages          []WorkflowStageState `json:"stages,omitempty"`
	StageArtifacts  []StageArtifact      `json:"stageArtifacts,omitempty"`
}

// Attempt is one execution attempt within a run (retries add attempts).
type Attempt struct {
	ID           string `json:"id"`
	RunID        string `json:"runId"`
	Sequence     int    `json:"sequence"`
	Status       string `json:"status"`
	AutoFixRound int    `json:"autoFixRound"`
	CreatedAt    string `json:"createdAt"`
}

// StageArtifact binds a real artifact to a Build Pulse node.
type StageArtifact struct {
	ID           string `json:"id"`
	RunID        string `json:"runId"`
	AttemptID    string `json:"attemptId"`
	Stage        string `json:"stage"`
	ArtifactType string `json:"artifactType"`
	ArtifactRef  string `json:"artifactRef"`
	CreatedAt    string `json:"createdAt"`
}

// WorkflowStageState is the durable Build Pulse state for one stage in one
// attempt. Stage is a compatibility alias for clients that predate stageKey.
type WorkflowStageState struct {
	StageKey     string  `json:"stageKey"`
	Stage        string  `json:"stage"`
	Status       string  `json:"status"`
	Attempt      int     `json:"attempt"`
	AttemptID    *string `json:"attemptId,omitempty"`
	StartedAt    *string `json:"startedAt"`
	FinishedAt   *string `json:"finishedAt"`
	CompletedAt  *string `json:"completedAt,omitempty"`
	UpdatedAt    string  `json:"updatedAt"`
	ErrorCode    *string `json:"errorCode"`
	ArtifactType *string `json:"artifactType,omitempty"`
	ArtifactRef  *string `json:"artifactRef,omitempty"`
}

// WorkflowPreview identifies the stable preview and the workflow that produced
// it. A failed latest run may legitimately point at a preview from an older run.
type WorkflowPreview struct {
	Version       *string `json:"version"`
	WorkflowRunID *string `json:"workflowRunId"`
}

// WorkflowConsistency makes recovery conflicts explicit instead of silently
// returning contradictory waiting/running/failed stage nodes.
type WorkflowConsistency struct {
	OK            bool     `json:"ok"`
	ConflictCodes []string `json:"conflictCodes"`
}

// Iteration is one modification round (agent / manual / restore).
type Iteration struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"projectId"`
	RunID           *string `json:"runId"`
	Kind            string  `json:"kind"`
	BaseVersionID   *string `json:"baseVersionId"`
	ResultVersionID *string `json:"resultVersionId"`
	Prompt          *string `json:"prompt"`
	CreatedAt       string  `json:"createdAt"`
}

// Version is a file snapshot (draft/validating/stable/failed).
type Version struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"projectId"`
	IterationID string  `json:"iterationId"`
	Status      string  `json:"status"`
	FilesHash   *string `json:"filesHash"`
	CreatedAt   string  `json:"createdAt"`
}

// File is one file within a version snapshot.
type File struct {
	ID        string `json:"id"`
	VersionID string `json:"versionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Readonly  bool   `json:"readonly"`
}

// FileSnapshot is the input shape for committing a version's files.
type FileSnapshot struct {
	Path     string
	Content  string
	Readonly bool
}

// Event is one persisted SSE event (FLO-60 streams these).
type Event struct {
	ID        string         `json:"id"`
	RunID     string         `json:"runId"`
	Seq       int            `json:"seq"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"createdAt"`
}

// limits reads the shared contract limits so the store never hardcodes them.
func limits() contracts.Limits { return contracts.Load().Limits }
