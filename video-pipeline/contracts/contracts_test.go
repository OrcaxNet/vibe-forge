package contracts_test

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestContractDocumentsAreValidYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file string
		root string
	}{
		{name: "OpenAPI", file: "openapi.yaml", root: "openapi"},
		{name: "AsyncAPI", file: "asyncapi.yaml", root: "asyncapi"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", tt.file, err)
			}
			var document map[string]any
			if err := yaml.Unmarshal(content, &document); err != nil {
				t.Fatalf("yaml.Unmarshal(%q): %v", tt.file, err)
			}
			if document[tt.root] == nil {
				t.Fatalf("%s root key missing from %s", tt.root, tt.file)
			}
		})
	}
}

func TestOpenAPIContainsProviderFirstOperationsAndErrors(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		"/video-api/v1/providers/status:",
		"/video-api/v1/providers/capabilities:",
		"/video-api/v1/generation-plans:",
		"/video-api/v1/provider-jobs:",
		"/video-api/v1/provider-jobs/{providerJobId}/cancel:",
		"/video-api/v1/provider-callbacks/{providerProfileId}:",
		"/video-api/v1/episodes/{episodeId}/production-batches:",
		"/video-api/v1/runs/{runId}/cancel:",
		"/video-api/v1/runs/{runId}/resume:",
		"/video-api/v1/approvals:",
		"/video-api/v1/manifests/{scopeType}/{revisionId}:",
		"ATTEMPT_LIMIT_REACHED",
		"LICENSE_BLOCKED",
		"STALE_DEPENDENCY",
		"PROVIDER_AUTHENTICATION_FAILED",
		"PROVIDER_QUOTA_EXHAUSTED",
		"PROVIDER_CONTENT_BLOCKED",
		"PROVIDER_REGION_UNAVAILABLE",
		"PROVIDER_MODEL_UNAVAILABLE",
		"PROVIDER_RESULT_UNKNOWN",
	} {
		if !contains(text, required) {
			t.Errorf("openapi.yaml missing %q", required)
		}
	}
	for _, forbidden := range []string{"GPU_WORKER_UNAVAILABLE", "COMFYUI_REJECTED", "Wan2.2-TI2V-5B"} {
		if contains(text, forbidden) {
			t.Errorf("openapi.yaml contains superseded assumption %q", forbidden)
		}
	}
}

func TestAsyncAPIContainsProviderAndCostEvents(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile("asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		"video.provider-job.state-changed.v1",
		"video.cost-ledger.recorded.v1",
		"PROVIDER_RATE_LIMITED",
		"REQUIRES_ACTION",
		"UNKNOWN",
	} {
		if !contains(text, required) {
			t.Errorf("asyncapi.yaml missing %q", required)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
