// Package mockprovider implements a deterministic, no-credential provider
// fixture. It exercises the same provider-neutral contract used by remote
// adapters without performing generative inference or requiring a GPU.
package mockprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/artifactstore"
	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/provider"
	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/runtimeconfig"
)

type jobRecord struct {
	Request     provider.JobRequest
	RequestHash string
	Response    provider.JobResponse
	Polls       int
	CallbackIDs map[string]struct{}
	CallbackSeq int64
}

// Server serves a process-local fixture. Product truth remains in PostgreSQL
// and Temporal; this store emulates only a remote provider's task registry.
type Server struct {
	config runtimeconfig.MockProvider
	store  *artifactstore.Store
	mu     sync.Mutex
	jobs   map[string]*jobRecord
}

// New creates a mock provider.
func New(config runtimeconfig.MockProvider, store *artifactstore.Store) *Server {
	return &Server{config: config, store: store, jobs: make(map[string]*jobRecord)}
}

// Handler returns the versioned internal adapter routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.health)
	mux.HandleFunc("GET /health/ready", s.health)
	mux.HandleFunc("GET /v1/capabilities", s.capabilities)
	mux.HandleFunc("POST /v1/estimates", s.estimate)
	mux.HandleFunc("POST /v1/jobs", s.createJob)
	mux.HandleFunc("GET /v1/jobs/{jobID}", s.getJob)
	mux.HandleFunc("POST /v1/jobs/{jobID}/cancel", s.cancelJob)
	mux.HandleFunc("POST /v1/jobs/{jobID}/callbacks", s.applyCallback)
	return http.MaxBytesHandler(mux, 1<<20)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ready",
		"providerId":   s.config.ProviderID,
		"mode":         "mock",
		"configured":   true,
		"requiresKey":  false,
		"capabilities": s.config.Capabilities,
	})
}

func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	snapshots := make([]provider.CapabilitySnapshot, 0, len(s.config.Capabilities))
	for _, alias := range s.config.Capabilities {
		sum := sha256.Sum256([]byte(s.config.ProviderID + "\x00" + alias + "\x00v1"))
		snapshots = append(snapshots, provider.CapabilitySnapshot{
			Alias:          alias,
			Kind:           strings.TrimSuffix(alias, ".primary"),
			Configured:     true,
			Enabled:        true,
			Mode:           "mock",
			Provider:       "mock",
			ModelID:        "fixture-" + strings.TrimSuffix(alias, ".primary") + "-v1",
			RouteVersion:   "mock-routes-v1",
			SnapshotHash:   hex.EncodeToString(sum[:]),
			EffectiveAt:    "2026-07-30T00:00:00Z",
			Limits:         limitsFor(alias),
			SupportedInput: inputsFor(alias),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": "v1",
		"providerId":    s.config.ProviderID,
		"capabilities":  snapshots,
	})
}

func (s *Server) estimate(w http.ResponseWriter, r *http.Request) {
	var request provider.EstimateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeProviderError(w, http.StatusBadRequest, newError(provider.ErrorInvalidRequest, "invalid estimate request", false, false))
		return
	}
	if !validCapability(request.Capability) || request.Candidates < 1 {
		writeProviderError(w, http.StatusUnprocessableEntity, newError(provider.ErrorInvalidRequest, "capability and positive candidates are required", false, false))
		return
	}
	material, _ := json.Marshal(request)
	sum := sha256.Sum256(material)
	minimum := int64(request.Candidates * 100)
	maximum := int64(request.Candidates * 150)
	writeJSON(w, http.StatusOK, provider.EstimateResponse{
		EstimateID:     "estimate-" + hex.EncodeToString(sum[:8]),
		UnitsMinimum:   minimum,
		UnitsMaximum:   maximum,
		Unit:           "mock-units",
		AmountMinimum:  &minimum,
		AmountMaximum:  &maximum,
		Currency:       "CNY",
		PricingVersion: "mock-pricing-v1",
		ValidUntil:     "2026-07-30T00:15:00Z",
	})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var request provider.JobRequest
	if err := decodeJSON(r, &request); err != nil {
		writeProviderError(w, http.StatusBadRequest, newError(provider.ErrorInvalidRequest, "invalid job envelope", false, false))
		return
	}
	if err := validateJob(request); err != nil {
		writeProviderError(w, http.StatusUnprocessableEntity, newError(provider.ErrorInvalidRequest, err.Error(), false, false))
		return
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key == "" || key != request.JobID {
		writeProviderError(w, http.StatusBadRequest, newError(provider.ErrorInvalidRequest, "Idempotency-Key must equal jobId", false, false))
		return
	}
	if status, providerErr := scenarioError(request.Simulation); providerErr != nil {
		if providerErr.RetryAfterMS > 0 {
			w.Header().Set("Retry-After", "1")
		}
		writeProviderError(w, status, providerErr)
		return
	}

	requestBytes, _ := json.Marshal(request)
	requestHashBytes := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(requestHashBytes[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.jobs[request.JobID]; ok {
		if existing.RequestHash != requestHash {
			writeProviderError(w, http.StatusConflict, newError(provider.ErrorIdempotencyConflict, "jobId was already used for different input", false, true))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, existing.Response)
		return
	}

	state := provider.StateQueued
	if request.Simulation == "timeout" {
		state = provider.StateUnknown
	}
	sum := sha256.Sum256([]byte(request.JobID + "\x00" + request.InputHash))
	actual := int64(125)
	response := provider.JobResponse{
		JobID:          request.JobID,
		RunID:          request.RunID,
		UpstreamTaskID: "mock-task-" + hex.EncodeToString(sum[:8]),
		RequestID:      "mock-request-" + hex.EncodeToString(sum[8:16]),
		State:          state,
		Progress:       0,
		Model:          request.Model,
		Usage:          provider.Usage{InputUnits: 100, OutputUnits: 25, Unit: "mock-units"},
		Cost: provider.Cost{
			EstimatedMinor: request.BudgetReservation.AmountMinor,
			ActualMinor:    &actual,
			Currency:       request.BudgetReservation.Currency,
			PricingVersion: request.BudgetReservation.PricingVersion,
			Verified:       true,
		},
	}
	s.jobs[request.JobID] = &jobRecord{
		Request:     request,
		RequestHash: requestHash,
		Response:    response,
		CallbackIDs: make(map[string]struct{}),
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.jobs[r.PathValue("jobID")]
	if !ok {
		writeProviderError(w, http.StatusNotFound, newError(provider.ErrorInvalidRequest, "job not found", false, false))
		return
	}
	if !provider.Terminal(record.Response.State) && record.Response.State != provider.StateUnknown {
		record.Polls++
		switch record.Request.Simulation {
		case "slow_success", "recovery":
			if record.Polls == 1 {
				record.Response.State = provider.StateRunning
				record.Response.Progress = 50
			} else {
				s.complete(record)
			}
		default:
			s.complete(record)
		}
	}
	writeJSON(w, http.StatusOK, record.Response)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.jobs[r.PathValue("jobID")]
	if !ok {
		writeProviderError(w, http.StatusNotFound, newError(provider.ErrorInvalidRequest, "job not found", false, false))
		return
	}
	if record.Request.Simulation == "cancel_race" {
		s.complete(record)
		w.Header().Set("X-Cancel-Result", "already-terminal")
		writeJSON(w, http.StatusOK, record.Response)
		return
	}
	if provider.Terminal(record.Response.State) {
		w.Header().Set("X-Cancel-Result", "already-terminal")
		writeJSON(w, http.StatusOK, record.Response)
		return
	}
	record.Response.State = provider.StateCancelled
	record.Response.Progress = 0
	w.Header().Set("X-Cancel-Result", "accepted")
	writeJSON(w, http.StatusAccepted, record.Response)
}

type callbackRequest struct {
	CallbackID string            `json:"callbackId"`
	Sequence   int64             `json:"sequence"`
	State      provider.JobState `json:"state"`
}

func (s *Server) applyCallback(w http.ResponseWriter, r *http.Request) {
	var callback callbackRequest
	if err := decodeJSON(r, &callback); err != nil || callback.CallbackID == "" || callback.Sequence < 1 || !validCallbackState(callback.State) {
		writeProviderError(w, http.StatusBadRequest, newError(provider.ErrorInvalidRequest, "callbackId, positive sequence, and a provider callback state are required", false, false))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.jobs[r.PathValue("jobID")]
	if !ok {
		writeProviderError(w, http.StatusNotFound, newError(provider.ErrorInvalidRequest, "job not found", false, false))
		return
	}
	if _, duplicate := record.CallbackIDs[callback.CallbackID]; duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "applied": false, "duplicate": true})
		return
	}
	record.CallbackIDs[callback.CallbackID] = struct{}{}
	if callback.Sequence <= record.CallbackSeq || provider.Terminal(record.Response.State) {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "applied": false, "stale": true})
		return
	}
	record.CallbackSeq = callback.Sequence
	record.Response.State = callback.State
	if callback.State == provider.StateSucceeded {
		s.complete(record)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "applied": true, "state": record.Response.State})
}

func validCallbackState(state provider.JobState) bool {
	switch state {
	case provider.StateQueued,
		provider.StateRunning,
		provider.StateSucceeded,
		provider.StateFailed,
		provider.StateUnknown,
		provider.StateRequiresAction,
		provider.StateCancelled:
		return true
	default:
		return false
	}
}

func (s *Server) complete(record *jobRecord) {
	if record.Response.State == provider.StateSucceeded && len(record.Response.Artifacts) > 0 {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"schemaVersion": "v1",
		"kind":          "mock-generation-result",
		"capability":    record.Request.Capability,
		"jobId":         record.Request.JobID,
		"inputHash":     record.Request.InputHash,
		"modelSnapshot": record.Request.Model,
	})
	artifact, err := s.store.Put(context.Background(), bytes.NewReader(payload))
	if err != nil {
		record.Response.State = provider.StateUnknown
		record.Response.Error = newError(provider.ErrorUnavailable, "artifact commit is unavailable", true, false)
		return
	}
	record.Response.State = provider.StateSucceeded
	record.Response.Progress = 100
	record.Response.Artifacts = []provider.ArtifactDescriptor{{
		Artifact:   artifact,
		Role:       "OUTPUT",
		MediaType:  mediaTypeFor(record.Request.Capability),
		Width:      1280,
		Height:     720,
		DurationMS: 5000,
	}}
	record.Response.Error = nil
}

func validateJob(request provider.JobRequest) error {
	if request.SchemaVersion != "v1" {
		return errors.New("schemaVersion must be v1")
	}
	if request.JobID == "" || request.RunID == "" || request.TraceID == "" {
		return errors.New("jobId, runId, and traceId are required")
	}
	if !validCapability(request.Capability) {
		return errors.New("capability must be text.primary, image.primary, video.primary, or speech.primary")
	}
	if !validDigest(request.InputHash) || !validDigest(request.Model.CapabilityHash) {
		return errors.New("inputHash and modelSnapshot.capabilityHash must be lowercase SHA-256 digests")
	}
	if request.Model.CapabilityAlias != string(request.Capability) ||
		request.Model.Provider == "" ||
		request.Model.ModelID == "" ||
		request.Model.RouteVersion == "" {
		return errors.New("a matching immutable modelSnapshot is required")
	}
	if request.BudgetReservation.ReservationID == "" ||
		request.BudgetReservation.Currency == "" ||
		request.BudgetReservation.AmountMinor < 0 ||
		request.BudgetReservation.PricingVersion == "" ||
		request.BudgetReservation.ConfirmedBy == "" {
		return errors.New("an approved budgetReservation is required")
	}
	return nil
}

func validCapability(capability provider.Capability) bool {
	switch capability {
	case provider.CapabilityText, provider.CapabilityImage, provider.CapabilityVideo, provider.CapabilitySpeech:
		return true
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func scenarioError(simulation string) (int, *provider.ProviderError) {
	switch simulation {
	case "", "success", "slow_success", "recovery", "timeout", "cancel_race", "duplicate_callback":
		return 0, nil
	case "unauthorized":
		return http.StatusUnauthorized, newError(provider.ErrorAuthenticationFailed, "provider credential is missing or invalid", false, true)
	case "forbidden":
		return http.StatusForbidden, newError(provider.ErrorPermissionDenied, "model or endpoint access is denied", false, true)
	case "rate_limited":
		err := newError(provider.ErrorRateLimited, "provider rate limit reached", true, false)
		err.RetryAfterMS = 1000
		return http.StatusTooManyRequests, err
	case "quota_exhausted":
		return http.StatusPaymentRequired, newError(provider.ErrorQuotaExhausted, "provider quota or balance is exhausted", false, true)
	case "budget_exceeded":
		return http.StatusUnprocessableEntity, newError(provider.ErrorBudgetExceeded, "approved budget is insufficient for the request", false, true)
	case "content_blocked":
		return http.StatusUnprocessableEntity, newError(provider.ErrorContentBlocked, "provider safety policy blocked the request", false, true)
	case "provider_unavailable":
		return http.StatusServiceUnavailable, newError(provider.ErrorUnavailable, "provider is temporarily unavailable", true, false)
	case "region_unavailable":
		return http.StatusUnprocessableEntity, newError(provider.ErrorRegionUnavailable, "model is unavailable in the configured region", false, true)
	case "model_unavailable":
		return http.StatusUnprocessableEntity, newError(provider.ErrorModelUnavailable, "configured model or endpoint is unavailable", false, true)
	default:
		return http.StatusUnprocessableEntity, newError(provider.ErrorInvalidRequest, "unknown mock simulation", false, true)
	}
}

func newError(code provider.ErrorCode, message string, retryable, requiresAction bool) *provider.ProviderError {
	action := "inspect the provider status and retry"
	if requiresAction {
		action = "update credentials, quota, route, budget, or request before creating a new attempt"
	}
	return &provider.ProviderError{
		Code:            code,
		Message:         message,
		Retryable:       retryable,
		RequiresAction:  requiresAction,
		SuggestedAction: action,
	}
}

func limitsFor(alias string) map[string]any {
	switch alias {
	case string(provider.CapabilityVideo):
		return map[string]any{
			"durationsSeconds":   []int{4, 5, 6, 10, 15},
			"ratios":             []string{"16:9", "9:16"},
			"resolutions":        []string{"480p", "720p", "1080p"},
			"asynchronous":       true,
			"maximumConcurrency": 1,
		}
	case string(provider.CapabilityImage):
		return map[string]any{"maximumReferences": 4, "returns": []string{"url", "base64"}}
	case string(provider.CapabilitySpeech):
		return map[string]any{"timestamps": []string{"sentence", "word"}, "formats": []string{"wav", "mp3"}}
	default:
		return map[string]any{"structuredOutput": true, "streaming": true}
	}
}

func inputsFor(alias string) []string {
	switch alias {
	case string(provider.CapabilityVideo):
		return []string{"text", "image-reference", "tail-frame"}
	case string(provider.CapabilityImage):
		return []string{"text", "image-reference"}
	case string(provider.CapabilitySpeech):
		return []string{"text", "voice-ref"}
	default:
		return []string{"messages", "json-schema", "context"}
	}
}

func mediaTypeFor(capability provider.Capability) string {
	switch capability {
	case provider.CapabilityText:
		return "application/json"
	case provider.CapabilityImage:
		return "image/png"
	case provider.CapabilitySpeech:
		return "audio/wav"
	default:
		return "video/mp4"
	}
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProviderError(w http.ResponseWriter, status int, providerErr *provider.ProviderError) {
	writeJSON(w, status, map[string]any{"error": providerErr})
}

// Submit creates or replays a provider job.
func Submit(ctx context.Context, client *http.Client, endpoint string, request provider.JobRequest) (provider.JobResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return provider.JobResponse{}, fmt.Errorf("encode provider job: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return provider.JobResponse{}, fmt.Errorf("create provider request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.JobID)
	return doJobRequest(client, httpRequest)
}

// Get polls an existing upstream task through the provider adapter.
func Get(ctx context.Context, client *http.Client, endpoint, jobID string) (provider.JobResponse, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return provider.JobResponse{}, fmt.Errorf("create provider poll request: %w", err)
	}
	return doJobRequest(client, httpRequest)
}

// Cancel asks the provider to cancel without assuming that cancellation wins a
// race with completion.
func Cancel(ctx context.Context, client *http.Client, endpoint, jobID string) (provider.JobResponse, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/jobs/"+url.PathEscape(jobID)+"/cancel", nil)
	if err != nil {
		return provider.JobResponse{}, fmt.Errorf("create provider cancel request: %w", err)
	}
	return doJobRequest(client, httpRequest)
}

func doJobRequest(client *http.Client, request *http.Request) (provider.JobResponse, error) {
	response, err := client.Do(request)
	if err != nil {
		return provider.JobResponse{}, fmt.Errorf("provider adapter request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error *provider.ProviderError `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err == nil && envelope.Error != nil {
			return provider.JobResponse{}, envelope.Error
		}
		return provider.JobResponse{}, fmt.Errorf("provider adapter returned HTTP %d", response.StatusCode)
	}
	var result provider.JobResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return provider.JobResponse{}, fmt.Errorf("decode provider response: %w", err)
	}
	return result, nil
}

// DefaultHTTPClient gives Activities an explicit bounded transport.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
