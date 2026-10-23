package catalog

import (
	"testing"

	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiandUpstreamIDs pins the upstream model IDs ai& publishes for the catalog
// rows it serves. ai& names models vendor-prefixed ("deepseek-ai/deepseek-v4.1-flash"),
// so a binding without an UpstreamID would dispatch a slug the upstream 404s on.
var aiandUpstreamIDs = map[string]string{
	"deepseek/deepseek-v4.1-flash":  "deepseek-ai/deepseek-v4.1-flash",
	"deepseek/deepseek-v4-pro":      "deepseek-ai/deepseek-v4-pro",
	"deepseek/deepseek-v4-pro-0813": "deepseek-ai/deepseek-v4-pro",
	"moonshotai/kimi-k3":            "moonshotai/kimi-k3",
	"z-ai/glm-5.3":                  "zai-org/glm-5.3",
	"z-ai/glm-5.3-flash":            "zai-org/glm-5.3-flash",
	"motif/motif-3":                 "motif-technologies/motif-3",
	"qwen/qwen3.8-27b":              "qwen/qwen3.8-27b",
	"qwen/qwen3.6-27b":              "qwen/qwen3.6-27b",
	"google/gemma-4-31b":            "google/gemma-4-31b-it",
	"openai/gpt-oss-120b":           "openai/gpt-oss-120b",
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

// TestResolveBinding_AIANDIsOnlyBindingWhenAlone: ai& is the only binding, so
// with it wired the model must resolve rather than staying unroutable.
func TestResolveBinding_AIANDIsOnlyBindingWhenAlone(t *testing.T) {
	for model := range aiandUpstreamIDs {
		t.Run(model, func(t *testing.T) {
			binding, ok := ResolveBinding(model, map[string]struct{}{providers.ProviderAIAND: {}})
			require.True(t, ok)
			assert.Equal(t, providers.ProviderAIAND, binding.Provider)
		})
	}
}

// TestCatalog_EveryModelBindsOnlyAIAND pins the single-provider cut: every
// catalog row resolves through ai& and nothing else.
func TestCatalog_EveryModelBindsOnlyAIAND(t *testing.T) {
	for _, m := range Models {
		require.NotEmpty(t, m.Providers, "model %q has empty Providers list", m.ID)
		for i, b := range m.Providers {
			assert.Equalf(t, providers.ProviderAIAND, b.Provider,
				"model %q binding %d must be ai&", m.ID, i)
		}
	}
}
