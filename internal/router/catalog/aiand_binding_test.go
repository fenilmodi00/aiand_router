package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiandUpstreamIDs pins the upstream model IDs ai& publishes for the catalog
// rows it serves. ai& names models vendor-prefixed ("deepseek-ai/deepseek-v4.1-flash"),
// so a binding without an UpstreamID would dispatch a slug the upstream 404s on.
var aiandUpstreamIDs = map[string]string{
	"deepseek/deepseek-v4-flash":    "deepseek-ai/deepseek-v4-flash",
	"deepseek/deepseek-v4.1-flash":  "deepseek-ai/deepseek-v4.1-flash",
	"deepseek/deepseek-v4-pro":      "deepseek-ai/deepseek-v4-pro",
	"deepseek/deepseek-v4-pro-0813": "deepseek-ai/deepseek-v4-pro",
	"moonshotai/kimi-k2.7":          "moonshotai/kimi-k2.7-code",
	"moonshotai/kimi-k3":            "moonshotai/kimi-k3",
	"z-ai/glm-5.2":                  "zai-org/glm-5.2",
	"z-ai/glm-5.3":                  "zai-org/glm-5.3",
	"z-ai/glm-5.3-flash":            "zai-org/glm-5.3-flash",
}

// TestResolveBinding_AIANDCarriesUpstreamID pins ai&'s upstream IDs for every
// catalog row it serves: the router's slash-form slug is not what the upstream
// publishes, so the binding must carry the rewrite.
func TestResolveBinding_AIANDCarriesUpstreamID(t *testing.T) {
	for model, upstreamID := range aiandUpstreamIDs {
		t.Run(model, func(t *testing.T) {
			binding, ok := ResolveBinding(model, map[string]struct{}{providers.ProviderAIAND: {}})
			require.True(t, ok)
			assert.Equal(t, providers.ProviderAIAND, binding.Provider)
			assert.Equal(t, upstreamID, binding.UpstreamID)
		})
	}
}

// TestResolveBinding_AIANDTrailsOtherProviders pins that ai& is a trailing
// binding: a deploy that already has an earlier provider for the model keeps
// serving from it, so wiring an ai& key never displaces the incumbent.
func TestResolveBinding_AIANDTrailsOtherProviders(t *testing.T) {
	b, ok := ResolveBinding("deepseek/deepseek-v4.1-flash", map[string]struct{}{
		providers.ProviderMakora: {}, providers.ProviderAIAND: {},
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderMakora, b.Provider)

	b, ok = ResolveBinding("z-ai/glm-5.3-flash", map[string]struct{}{
		providers.ProviderFireworks: {}, providers.ProviderAIAND: {},
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderFireworks, b.Provider)

	b, ok = ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{
		providers.ProviderOpenRouter: {}, providers.ProviderAIAND: {},
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenRouter, b.Provider)
}

// TestResolveBinding_AIANDIsOnlyBindingWhenAlone is the other half: with no
// other provider wired, ai& must resolve rather than leaving the model
// unroutable.
func TestResolveBinding_AIANDIsOnlyBindingWhenAlone(t *testing.T) {
	for model := range aiandUpstreamIDs {
		t.Run(model, func(t *testing.T) {
			binding, ok := ResolveBinding(model, map[string]struct{}{providers.ProviderAIAND: {}})
			require.True(t, ok)
			assert.Equal(t, providers.ProviderAIAND, binding.Provider)
		})
	}
}
