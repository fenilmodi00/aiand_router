package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/policy"
)

func TestUtilityTarget_PrefersDefaultWhenServable(t *testing.T) {
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderAIAND, providers.ProviderAIAND),
	})
	require.True(t, ok)
	assert.Equal(t, policy.UtilityHardPinDefaultProvider, provider)
	assert.Equal(t, policy.UtilityHardPinDefaultModel, model)
}

func TestUtilityTarget_ExcludedDefaultFallsToCheapestLowTier(t *testing.T) {
	provider, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
		EnabledProviders: set(providers.ProviderAIAND),
		ExcludedModels:   set(policy.UtilityHardPinDefaultModel),
	})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, provider)
	assert.NotEqual(t, policy.UtilityHardPinDefaultModel, model)
	assert.Equal(t, catalog.TierLow, catalog.TierFor(model))
}

func TestUtilityTarget_NoServableModel(t *testing.T) {
	cases := map[string]policy.UtilityTargetRequest{
		"no providers": {},
		"every servable model excluded": {
			EnabledProviders: set(providers.ProviderAIAND),
			ExcludedModels:   excludeAllServedBy(providers.ProviderAIAND),
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			provider, model, ok := policy.UtilityTarget(req)
			assert.False(t, ok)
			assert.Empty(t, provider)
			assert.Empty(t, model)
		})
	}
}

func excludeAllServedBy(provider string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == provider {
				out[m.ID] = struct{}{}
			}
		}
	}
	return out
}

func TestUtilityTarget_NeverPicksTextOnlyModel(t *testing.T) {
	for _, provider := range catalogProviders() {
		t.Run(provider, func(t *testing.T) {
			_, model, ok := policy.UtilityTarget(policy.UtilityTargetRequest{
				EnabledProviders: set(provider),
				ExcludedModels:   set(policy.UtilityHardPinDefaultModel),
			})
			if !ok {
				t.Skip("no image-capable utility model on this provider")
			}
			m, known := catalog.ByID(model)
			require.True(t, known)
			assert.NotEqual(t, catalog.ImageInputUnsupported, m.ImageInput, model)
		})
	}
}

func catalogProviders() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if _, dup := seen[b.Provider]; !dup {
				seen[b.Provider] = struct{}{}
				out = append(out, b.Provider)
			}
		}
	}
	return out
}
