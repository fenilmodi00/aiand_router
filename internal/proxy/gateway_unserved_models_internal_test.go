package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func ctxWithKeys(keys ...*auth.ExternalAPIKey) context.Context {
	return context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, keys)
}

func gatewayKey(provider, baseURL string, models ...string) *auth.ExternalAPIKey {
	aliases := make(map[string]string, len(models))
	for _, m := range models {
		aliases[m] = m
	}
	return &auth.ExternalAPIKey{
		Provider:     provider,
		Plaintext:    []byte("token"),
		BaseURL:      baseURL,
		ModelAliases: aliases,
	}
}

func TestGatewayUnservedModelsForRequest_EmptyWithoutGatewayKeys(t *testing.T) {
	s := &Service{}
	s.rememberGatewayLacksModel(context.Background(), providers.ProviderAIAND, "grok-4.6")

	assert.Nil(t, s.gatewayUnservedModelsForRequest(context.Background()))
	assert.Nil(t, s.gatewayUnservedModelsForRequest(ctxWithKeys(
		&auth.ExternalAPIKey{Provider: providers.ProviderAIAND, Plaintext: []byte("t"),
			ModelAliases: map[string]string{"grok-4.6": "grok-4.6"}},
	)), "a vendor BYOK key is not gateway-exclusive")
}

// Another endpoint aliasing the same model may still serve it; excluding the
// model outright would strand routing on the one gateway that refused it.
func TestGatewayUnservedModelsForRequest_KeepsModelWhenAnotherGatewayServesIt(t *testing.T) {
	const refused = "https://cortex.example.com/api/v2/cortex"
	const other = "https://gw.example.com/v1"
	s := &Service{}
	s.unservedGatewayModels.Store(gatewayModelKey(refused, providers.ProviderAIAND, "grok-4.6"), struct{}{})

	got := s.gatewayUnservedModelsForRequest(ctxWithKeys(
		gatewayKey(providers.ProviderAIAND, refused, "grok-4.6"),
		gatewayKey(providers.ProviderAIAND, other, "grok-4.6"),
	))

	assert.NotContains(t, got, "grok-4.6")
}

// An unknown alias key never reaches routing, so it must not be reported.
func TestGatewayUnservedModelsForRequest_IgnoresNonCatalogAliases(t *testing.T) {
	const endpoint = "https://cortex.example.com/api/v2/cortex"
	s := &Service{}
	s.unservedGatewayModels.Store(gatewayModelKey(endpoint, providers.ProviderAIAND, "not-a-catalog-model"), struct{}{})

	got := s.gatewayUnservedModelsForRequest(ctxWithKeys(
		gatewayKey(providers.ProviderAIAND, endpoint, "not-a-catalog-model"),
	))

	assert.Nil(t, got)
}
