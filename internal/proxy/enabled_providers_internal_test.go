package proxy

import (
	"context"
	"net/http"
	"testing"
	"weave-os/router/internal/dispatch"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

// TestEnabledProvidersForRequest_DeploymentKeyedStillCrossSurface confirms
// env-keyed providers stay eligible regardless of surface.
func TestEnabledProvidersForRequest_DeploymentKeyedStillCrossSurface(t *testing.T) {
	s := &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAIAND: nil,
		}),
		deploymentKeyedProviders: map[string]struct{}{
			providers.ProviderAIAND: {},
		},
		passthroughEligibleProviders: map[string]struct{}{},
	}

	got := s.enabledProvidersForRequest(context.Background(), providers.ProviderAIAND, http.Header{})
	assert.Contains(t, got, providers.ProviderAIAND)
	assert.Contains(t, got, providers.ProviderAIAND,
		"env-keyed providers must remain eligible cross-surface; only passthrough is surface-scoped")
}

// TestEnabledProvidersForRequest_GatewayKeyDisplacesVendors: a tenant's own
// gateway is the exclusive upstream; no vendor may stay eligible.
func TestEnabledProvidersForRequest_GatewayKeyDisplacesVendors(t *testing.T) {
	s := &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAIAND: nil,
		}),
		deploymentKeyedProviders: map[string]struct{}{
			providers.ProviderAIAND: {},
		},
		passthroughEligibleProviders: map[string]struct{}{},
	}
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("pat")},
	})

	got := s.enabledProvidersForRequest(ctx, providers.ProviderAIAND, http.Header{})

	assert.Equal(t, map[string]struct{}{providers.ProviderAIAND: {}}, got)
}

func TestEnabledProvidersForRequest_VendorByokKeyDoesNotDisplaceVendors(t *testing.T) {
	s := &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAIAND: nil,
		}),
		deploymentKeyedProviders: map[string]struct{}{
			providers.ProviderAIAND: {},
		},
		passthroughEligibleProviders: map[string]struct{}{},
	}
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("pat")},
	})

	got := s.enabledProvidersForRequest(ctx, providers.ProviderAIAND, http.Header{})

	assert.Contains(t, got, providers.ProviderAIAND,
		"a vendor BYOK key is not a gateway and must not narrow the eligible set")
}
