// Package provider defines the provider-neutral generation boundary used by
// the video production domain. Provider SDK types and credentials must never
// cross this package boundary.
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/artifactstore"
)

// Capability is a stable business alias. A route snapshot resolves it to one
// provider/model for an individual attempt.
type Capability string

const (
	CapabilityText   Capability = "text.primary"
	CapabilityImage  Capability = "image.primary"
	CapabilityVideo  Capability = "video.primary"
	CapabilitySpeech Capability = "speech.primary"
)

// JobState is the provider-neutral lifecycle. UNKNOWN is deliberately not a
// failure: reconciliation must continue with the saved upstream task ID.
type JobState string

const (
	StateDraft          JobState = "DRAFT"
	StateValidated      JobState = "VALIDATED"
	StateQueued         JobState = "QUEUED"
	StateRunning        JobState = "RUNNING"
	StateSucceeded      JobState = "SUCCEEDED"
	StateFailed         JobState = "FAILED"
	StateUnknown        JobState = "UNKNOWN"
	StateRequiresAction JobState = "REQUIRES_ACTION"
	StateCancelled      JobState = "CANCELLED"
)

// ErrorCode is stable across providers and is safe for API/UI branching.
type ErrorCode string

const (
	ErrorAuthenticationFailed ErrorCode = "PROVIDER_AUTHENTICATION_FAILED"
	ErrorPermissionDenied     ErrorCode = "PROVIDER_PERMISSION_DENIED"
	ErrorRateLimited          ErrorCode = "PROVIDER_RATE_LIMITED"
	ErrorQuotaExhausted       ErrorCode = "PROVIDER_QUOTA_EXHAUSTED"
	ErrorBudgetExceeded       ErrorCode = "BUDGET_EXCEEDED"
	ErrorContentBlocked       ErrorCode = "PROVIDER_CONTENT_BLOCKED"
	ErrorRegionUnavailable    ErrorCode = "PROVIDER_REGION_UNAVAILABLE"
	ErrorModelUnavailable     ErrorCode = "PROVIDER_MODEL_UNAVAILABLE"
	ErrorUnavailable          ErrorCode = "PROVIDER_UNAVAILABLE"
	ErrorInvalidRequest       ErrorCode = "PROVIDER_INVALID_REQUEST"
	ErrorTimeoutUnknown       ErrorCode = "PROVIDER_RESULT_UNKNOWN"
	ErrorIdempotencyConflict  ErrorCode = "IDEMPOTENCY_CONFLICT"
)

// ModelSnapshot is immutable attempt provenance. Route changes affect only new
// attempts and never rewrite this value.
type ModelSnapshot struct {
	CapabilityAlias string `json:"capabilityAlias"`
	Provider        string `json:"provider"`
	ModelID         string `json:"modelId"`
	EndpointID      string `json:"endpointId,omitempty"`
	RouteVersion    string `json:"routeVersion"`
	CapabilityHash  string `json:"capabilityHash"`
}

// CapabilitySnapshot is returned by discovery and cached with an explicit
// version/effective time instead of becoming a permanent product rule.
type CapabilitySnapshot struct {
	Alias          string         `json:"alias"`
	Kind           string         `json:"kind"`
	Configured     bool           `json:"configured"`
	Enabled        bool           `json:"enabled"`
	Mode           string         `json:"mode"`
	Provider       string         `json:"provider"`
	ModelID        string         `json:"modelId"`
	RouteVersion   string         `json:"routeVersion"`
	SnapshotHash   string         `json:"snapshotHash"`
	EffectiveAt    string         `json:"effectiveAt"`
	Limits         map[string]any `json:"limits"`
	SupportedInput []string       `json:"supportedInput"`
}

// BudgetReservation is the approved upper bound attached before a paid submit.
type BudgetReservation struct {
	ReservationID  string `json:"reservationId"`
	Currency       string `json:"currency"`
	AmountMinor    int64  `json:"amountMinor"`
	PricingVersion string `json:"pricingVersion"`
	ConfirmedBy    string `json:"confirmedBy"`
}

// JobRequest is the versioned, secret-free Activity -> provider-adapter
// envelope. Simulation is accepted only by the local mock implementation.
type JobRequest struct {
	SchemaVersion     string            `json:"schemaVersion"`
	JobID             string            `json:"jobId"`
	RunID             string            `json:"runId"`
	Capability        Capability        `json:"capability"`
	InputHash         string            `json:"inputHash"`
	Model             ModelSnapshot     `json:"modelSnapshot"`
	Request           map[string]any    `json:"request"`
	BudgetReservation BudgetReservation `json:"budgetReservation"`
	TraceID           string            `json:"traceId"`
	CallbackURL       string            `json:"callbackUrl,omitempty"`
	Simulation        string            `json:"simulation,omitempty"`
}

// ArtifactDescriptor contains only durable, internal artifact references. A
// provider's temporary signed URL is consumed by the adapter and never stored.
type ArtifactDescriptor struct {
	Artifact   artifactstore.Artifact `json:"artifact"`
	Role       string                 `json:"role"`
	MediaType  string                 `json:"mediaType"`
	Width      int                    `json:"width,omitempty"`
	Height     int                    `json:"height,omitempty"`
	DurationMS int64                  `json:"durationMs,omitempty"`
}

// Usage and Cost are manifest mounting points even when a provider cannot
// return an authoritative amount.
type Usage struct {
	InputUnits  int64  `json:"inputUnits"`
	OutputUnits int64  `json:"outputUnits"`
	Unit        string `json:"unit"`
}

type Cost struct {
	EstimatedMinor int64  `json:"estimatedMinor"`
	ActualMinor    *int64 `json:"actualMinor,omitempty"`
	Currency       string `json:"currency"`
	PricingVersion string `json:"pricingVersion"`
	Verified       bool   `json:"verified"`
}

// JobResponse is safe to persist in ProviderJob/GenerationManifest.
type JobResponse struct {
	JobID          string               `json:"jobId"`
	RunID          string               `json:"runId"`
	UpstreamTaskID string               `json:"upstreamTaskId"`
	RequestID      string               `json:"requestId"`
	State          JobState             `json:"state"`
	Progress       int                  `json:"progress"`
	Model          ModelSnapshot        `json:"modelSnapshot"`
	Artifacts      []ArtifactDescriptor `json:"artifacts"`
	Usage          Usage                `json:"usage"`
	Cost           Cost                 `json:"cost"`
	Error          *ProviderError       `json:"error,omitempty"`
}

// ProviderError preserves retry/action semantics without exposing upstream
// response bodies or credentials.
type ProviderError struct {
	Code            ErrorCode `json:"code"`
	Message         string    `json:"message"`
	Retryable       bool      `json:"retryable"`
	RequiresAction  bool      `json:"requiresAction"`
	SuggestedAction string    `json:"suggestedAction"`
	RetryAfterMS    int64     `json:"retryAfterMs,omitempty"`
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// EstimateRequest/Response allow the budget gate to run before submit. Unknown
// monetary price is represented explicitly; it is never fabricated as zero.
type EstimateRequest struct {
	Capability Capability     `json:"capability"`
	Model      ModelSnapshot  `json:"modelSnapshot"`
	Parameters map[string]any `json:"parameters"`
	Candidates int            `json:"candidates"`
}

type EstimateResponse struct {
	EstimateID     string `json:"estimateId"`
	UnitsMinimum   int64  `json:"unitsMinimum"`
	UnitsMaximum   int64  `json:"unitsMaximum"`
	Unit           string `json:"unit"`
	AmountMinimum  *int64 `json:"amountMinimum,omitempty"`
	AmountMaximum  *int64 `json:"amountMaximum,omitempty"`
	Currency       string `json:"currency,omitempty"`
	PricingVersion string `json:"pricingVersion"`
	ValidUntil     string `json:"validUntil"`
}

// Adapter is the provider-neutral application interface. Synchronous and
// asynchronous providers can implement it without leaking SDK-specific types.
type Adapter interface {
	DiscoverCapabilities(context.Context) ([]CapabilitySnapshot, error)
	Estimate(context.Context, EstimateRequest) (EstimateResponse, error)
	GenerateText(context.Context, JobRequest) (JobResponse, error)
	GenerateImage(context.Context, JobRequest) (JobResponse, error)
	SubmitVideo(context.Context, JobRequest) (JobResponse, error)
	GetVideoTask(context.Context, string) (JobResponse, error)
	CancelVideoTask(context.Context, string) (JobResponse, error)
	SynthesizeSpeech(context.Context, JobRequest) (JobResponse, error)
}

// Terminal reports states that cannot be changed by later polling/callbacks.
func Terminal(state JobState) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// RetryPolicy is shared documentation/config for infrastructure retries. A
// user edit creates a new attempt; these retries reuse the same JobID.
type RetryPolicy struct {
	MaximumAttempts int
	InitialInterval time.Duration
	MaximumInterval time.Duration
}
