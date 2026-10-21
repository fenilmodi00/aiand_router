package proxy

import (
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestCustomBindingsFromKeys_SkipsUnusableDeclarations(t *testing.T) {
	got := customBindingsFromKeys([]*auth.ExternalAPIKey{
		{
			// No plaintext: enrolling it would route to an upstream that 401s.
			Provider:     providers.ProviderAIAND,
			ModelAliases: map[string]string{"gpt-5": "openai-gpt-5"},
		},
		{
			Provider:  providers.ProviderAIAND,
			Plaintext: []byte("pat"),
			ModelAliases: map[string]string{
				"not-a-catalog-model": "whatever",
			},
		},
	})

	assert.Empty(t, got)
}

// TestCustomBindingsFromKeys_ProvidersAreOrdered: alias maps iterate randomly,
// so without sorting two identical installations could pick different endpoints.
func TestCustomBindingsFromKeys_ProvidersAreOrdered(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{
			Provider:     providers.ProviderAIAND,
			Plaintext:    []byte("pat"),
			ModelAliases: map[string]string{"deepseek/deepseek-v4.1-flash": "deepseek/deepseek-v4.1-flash"},
		},
		{
			Provider:     providers.ProviderAIAND,
			Plaintext:    []byte("pat"),
			ModelAliases: map[string]string{"deepseek/deepseek-v4.1-flash": "deepseek/deepseek-v4.1-flash"},
		},
	}

	assert.Equal(t,
		[]string{providers.ProviderAIAND, providers.ProviderAIAND},
		customBindingsFromKeys(keys)["deepseek/deepseek-v4.1-flash"])
}

func TestGatewayProvidersFromKeys_NoGatewayKeys(t *testing.T) {
	got := gatewayProvidersFromKeys([]*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("pat")},
	})

	assert.Empty(t, got)
}
