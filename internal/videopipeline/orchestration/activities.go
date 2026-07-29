package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/mockprovider"
	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/provider"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Activities is the executable local skeleton for the Activity boundary.
// Production implementations persist through the control-plane repository;
// this version keeps no business truth and delegates only versioned provider
// jobs. The adapter URL can target the included mock or a remote-provider
// adapter; credentials never enter workflow inputs.
type Activities struct {
	ProviderAdapterURL string
	HTTPClient         *http.Client
}

// NewActivities creates bounded Activity clients.
func NewActivities(providerAdapterURL string) *Activities {
	return &Activities{ProviderAdapterURL: providerAdapterURL, HTTPClient: mockprovider.DefaultHTTPClient()}
}

// ValidateBatch rejects unversioned or unapproved production inputs.
func (a *Activities) ValidateBatch(_ context.Context, input EpisodeProductionInput) error {
	return validateWorkflowInput(input)
}

// CompilePrompt returns an immutable deterministic placeholder snapshot. The
// production compiler persists the full effective-context and evidence chain.
func (a *Activities) CompilePrompt(_ context.Context, input CompilePromptInput) (PromptSnapshotRef, error) {
	if input.ShotSpecRevisionID == "" || input.GenerationProfileRef == "" {
		return PromptSnapshotRef{}, errors.New("shotSpecRevisionId and generationProfileRef are required")
	}
	sum := sha256.Sum256([]byte(input.ShotSpecRevisionID + "\x00" + input.GenerationProfileRef))
	digest := hex.EncodeToString(sum[:])
	return PromptSnapshotRef{ID: "prompt-" + digest[:16], Digest: digest}, nil
}

// CreateRun creates deterministic IDs for the runnable skeleton. A production
// repository makes this an idempotent PostgreSQL transaction with an outbox row.
func (a *Activities) CreateRun(_ context.Context, input CreateRunInput) (GenerationRunRef, error) {
	if input.CreativeAttempt < 1 || input.CreativeAttempt > 2 {
		return GenerationRunRef{}, errors.New("creativeAttempt must be 1 or 2")
	}
	if input.Route.CapabilityAlias != string(provider.CapabilityVideo) ||
		input.Route.Provider == "" ||
		input.Route.ModelID == "" ||
		input.Route.RouteVersion == "" ||
		input.Route.CapabilityHash == "" {
		return GenerationRunRef{}, errors.New("a frozen video.primary route is required")
	}
	material := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d",
		input.ShotSpecRevisionID,
		input.PromptSnapshot.Digest,
		input.GenerationProfileRef,
		input.Route.CapabilityAlias,
		input.Route.Provider,
		input.Route.ModelID,
		input.Route.RouteVersion,
		input.Route.CapabilityHash,
		input.CreativeAttempt,
	)
	sum := sha256.Sum256([]byte(material))
	digest := hex.EncodeToString(sum[:])
	return GenerationRunRef{
		RunID:         "run-" + digest[:16],
		RunSpecDigest: digest,
		Attempt:       input.CreativeAttempt,
	}, nil
}

// ExecuteProviderJob submits and reconciles one idempotent remote API job.
// Activity retries reuse jobId and upstreamTaskId, so they never create a new
// paid attempt. UNKNOWN is polled; it is not treated as a failed generation.
func (a *Activities) ExecuteProviderJob(ctx context.Context, input ExecuteProviderJobInput) (ProviderResult, error) {
	if a.HTTPClient == nil {
		return ProviderResult{}, errors.New("provider HTTP client is required")
	}
	activity.RecordHeartbeat(ctx, map[string]any{"phase": "submitting", "runId": input.Run.RunID})
	result, err := mockprovider.Submit(ctx, a.HTTPClient, a.ProviderAdapterURL, provider.JobRequest{
		SchemaVersion: "v1",
		JobID:         "provider-job-" + input.Run.RunID,
		RunID:         input.Run.RunID,
		Capability:    provider.CapabilityVideo,
		InputHash:     input.Run.RunSpecDigest,
		Model:         input.Route,
		Request: map[string]any{
			"promptSnapshotId": input.Prompt.ID,
			"promptHash":       input.Prompt.Digest,
		},
		BudgetReservation: provider.BudgetReservation{
			ReservationID:  input.BudgetApprovalID,
			Currency:       input.BudgetCurrency,
			AmountMinor:    input.BudgetMaximumMinor,
			PricingVersion: "workflow-approved-v1",
			ConfirmedBy:    input.BudgetApprovalID,
		},
		TraceID: input.TraceID,
	})
	if err != nil {
		return ProviderResult{}, classifyProviderError(err)
	}
	activity.RecordHeartbeat(ctx, map[string]any{
		"phase":          "submitted",
		"providerJobId":  result.JobID,
		"upstreamTaskId": result.UpstreamTaskID,
		"state":          result.State,
	})

	for !provider.Terminal(result.State) {
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return ProviderResult{}, err
		}
		result, err = mockprovider.Get(ctx, a.HTTPClient, a.ProviderAdapterURL, result.JobID)
		if err != nil {
			return ProviderResult{}, classifyProviderError(err)
		}
		activity.RecordHeartbeat(ctx, map[string]any{
			"phase":          "reconciling",
			"providerJobId":  result.JobID,
			"upstreamTaskId": result.UpstreamTaskID,
			"state":          result.State,
			"progress":       result.Progress,
		})
		if result.State == provider.StateRequiresAction {
			return ProviderResult{}, temporal.NewNonRetryableApplicationError(
				"provider job requires action",
				string(provider.ErrorInvalidRequest),
				result.Error,
			)
		}
	}
	if result.State != provider.StateSucceeded {
		if result.Error != nil {
			return ProviderResult{}, classifyProviderError(result.Error)
		}
		return ProviderResult{}, temporal.NewNonRetryableApplicationError(
			"provider job ended without an artifact",
			string(provider.ErrorInvalidRequest),
			nil,
		)
	}
	if len(result.Artifacts) == 0 {
		return ProviderResult{}, errors.New("provider job succeeded without a committed artifact")
	}
	output := result.Artifacts[0].Artifact
	activity.RecordHeartbeat(ctx, map[string]any{
		"phase":          "committed",
		"upstreamTaskId": result.UpstreamTaskID,
		"artifactDigest": output.Digest,
	})
	return ProviderResult{
		UpstreamTaskID: result.UpstreamTaskID,
		RequestID:      result.RequestID,
		ArtifactDigest: output.Digest,
		ArtifactURI:    output.URI,
		Model:          result.Model,
		Usage:          result.Usage,
		Cost:           result.Cost,
	}, nil
}

// RunAutomaticQC is deliberately conservative in the skeleton: a committed,
// correctly addressed mock artifact passes structural QC only.
func (a *Activities) RunAutomaticQC(_ context.Context, input RunQCInput) (QCResult, error) {
	if input.Provider.ArtifactDigest == "" || input.Provider.ArtifactURI == "" {
		return QCResult{Passed: false, FailureCode: "QC_MEDIA_MISSING"}, nil
	}
	return QCResult{Passed: true}, nil
}

// CreateShotReview is a typed boundary for the control-plane ReviewTask write.
func (a *Activities) CreateShotReview(_ context.Context, input CreateReviewInput) error {
	if input.ShotSpecRevisionID == "" || input.RunID == "" || input.ArtifactDigest == "" {
		return errors.New("shot review requires shotSpecRevisionId, runId, and artifactDigest")
	}
	return nil
}

// EscalateShot records that two creative attempts were exhausted.
func (a *Activities) EscalateShot(_ context.Context, input EscalateShotInput) error {
	if input.ShotSpecRevisionID == "" {
		return errors.New("shotSpecRevisionId is required")
	}
	return nil
}

// CreateGate3 creates the final review task after every shot has a reviewable
// immutable artifact.
func (a *Activities) CreateGate3(_ context.Context, input CreateGate3Input) error {
	if input.EpisodeRevisionID == "" || len(input.RunIDs) == 0 {
		return errors.New("G3 review requires episodeRevisionId and runIds")
	}
	return nil
}

func classifyProviderError(err error) error {
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) {
		return err
	}
	if providerErr.Retryable {
		return err
	}
	return temporal.NewNonRetryableApplicationError(
		providerErr.Message,
		string(providerErr.Code),
		err,
	)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ActivityTimeout is exported for manifest/config documentation tests.
const ActivityTimeout = 30 * time.Minute
