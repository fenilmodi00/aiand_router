package proxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestCodexSubscriptionModelRejectionSuppressesLaterOAuthResolution(t *testing.T) {
	const model = "gpt-6-astra"
	ctx := codexSubscriptionTestCtx()
	svc := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderAIAND: {}}}
	err := &providers.UpstreamErrorResponse{
		Status: http.StatusNotFound,
		Body:   []byte(`{"error":{"code":"model_not_found","param":"model"}}`),
	}

	credentialCtx := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, model, http.Header{})
	require.True(t, servedOnCodexSubscription(credentialCtx))
	svc.recordSubscriptionModelRejection(credentialCtx, providers.ProviderAIAND, model, err)

	laterCtx := svc.resolveCredentials(ctx, providers.ProviderAIAND, model, http.Header{})
	assert.False(t, servedOnCodexSubscription(laterCtx), "a cached model-access denial must skip Codex OAuth for that model")
	assert.False(t, servedOnSubscription(laterCtx), "the denied turn must resolve onto the available paid fallback")

	otherModelCtx := svc.resolveCredentials(laterCtx, providers.ProviderAIAND, "gpt-5.6-sol", http.Header{})
	assert.True(t, servedOnCodexSubscription(otherModelCtx), "a model-specific denial must leave sibling failover on Codex OAuth")
}
