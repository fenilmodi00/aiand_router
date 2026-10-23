package catalog

import (
	"testing"

	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalog_NoDuplicateIDs(t *testing.T) {
	seen := make(map[string]struct{}, len(Models))
	for _, m := range Models {
		_, dup := seen[m.ID]
		require.False(t, dup, "duplicate model ID %q in catalog", m.ID)
		seen[m.ID] = struct{}{}
	}
}

func TestCatalog_EveryModelHasAtLeastOneBinding(t *testing.T) {
	for _, m := range Models {
		require.NotEmpty(t, m.Providers, "model %q has empty Providers list", m.ID)
	}
}

func TestCatalog_BindingsReferenceCanonicalProviders(t *testing.T) {
	for _, m := range Models {
		for i, b := range m.Providers {
			assert.Equalf(t, providers.ProviderAIAND, b.Provider,
				"model %q binding %d uses unknown provider %q", m.ID, i, b.Provider)
		}
	}
}

func TestCatalog_BindingsHavePositivePrice(t *testing.T) {
	for _, m := range Models {
		for i, b := range m.Providers {
			assert.Greaterf(t, b.Price.InputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive InputUSDPer1M", m.ID, i, b.Provider)
			assert.Greaterf(t, b.Price.OutputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive OutputUSDPer1M", m.ID, i, b.Provider)
			if b.Price.LongContext != nil {
				assert.Greaterf(t, b.Price.LongContext.ThresholdTokens, 0, "%s binding %d (%s) has non-positive long-context threshold", m.ID, i, b.Provider)
				assert.Greaterf(t, b.Price.LongContext.InputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive long-context input price", m.ID, i, b.Provider)
				assert.Greaterf(t, b.Price.LongContext.OutputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive long-context output price", m.ID, i, b.Provider)
				assert.Lessf(t, b.Price.LongContext.ThresholdTokens, ContextWindowFor(m.ID), "%s binding %d (%s) long-context threshold must be below its context window", m.ID, i, b.Provider)
			}
		}
	}
}

func TestByID_UnknownReturnsFalse(t *testing.T) {
	_, ok := ByID("definitely-not-a-model")
	assert.False(t, ok)
}

func TestByID_DeepSeekV4Pro0813Resolves(t *testing.T) {
	m, ok := ByID("deepseek/deepseek-v4-pro-0813")
	require.True(t, ok)
	assert.Equal(t, "deepseek/deepseek-v4-pro-0813", m.ID)
}

func TestPriceFor_UnknownProviderForKnownModel(t *testing.T) {
	// Only ai& serves the catalog, so another provider must miss.
	_, ok := PriceFor("other-provider", "deepseek/deepseek-v4-pro")
	assert.False(t, ok)
}

func TestPriceFor_KnownPair(t *testing.T) {
	p, ok := PriceFor(providers.ProviderAIAND, "deepseek/deepseek-v4-pro")
	require.True(t, ok)
	assert.Equal(t, 1.00, p.InputUSDPer1M)
	assert.Equal(t, 2.50, p.OutputUSDPer1M)
}

func TestTierFor_KnownAndUnknown(t *testing.T) {
	assert.Equal(t, TierMid, TierFor("deepseek/deepseek-v4-pro"))
	assert.Equal(t, TierLow, TierFor("deepseek/deepseek-v4.1-flash"))
	assert.Equal(t, TierHigh, TierFor("z-ai/glm-5.3"))
	assert.Equal(t, TierUnknown, TierFor("definitely-not-a-model"))
}

// The pin-only rows carry no tier: they are dispatchable but never automatic
// routing targets.
func TestTierFor_PinOnlyRowsAreUntiered(t *testing.T) {
	for _, id := range []string{
		"z-ai/glm-5.2",
		"moonshotai/kimi-k2.7",
		"deepseek/deepseek-v4-pro-0813",
		"google/gemma-4-31b",
		"qwen/qwen3.6-27b",
		"openai/gpt-oss-120b",
	} {
		assert.Equal(t, TierUnknown, TierFor(id), id)
	}
}

func TestResolveBinding_PicksFirstAvailable(t *testing.T) {
	b, ok := ResolveBinding("deepseek/deepseek-v4.1-flash", map[string]struct{}{providers.ProviderAIAND: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, b.Provider)

	_, ok = ResolveBinding("deepseek/deepseek-v4.1-flash", map[string]struct{}{"other-provider": {}})
	assert.False(t, ok)
}

func TestRoutingTargetSet_FiltersByTierAndRegisteredProviders(t *testing.T) {
	targets := RoutingTargetSet(map[string]struct{}{providers.ProviderAIAND: {}})

	assert.Contains(t, targets, "z-ai/glm-5.3")
	assert.Contains(t, targets, "moonshotai/kimi-k3")
	assert.Contains(t, targets, "motif/motif-3")
	assert.Contains(t, targets, "qwen/qwen3.8-27b")
	assert.NotContains(t, targets, "z-ai/glm-5.2", "untiered pin-only models must not become routing targets")
	assert.NotContains(t, targets, "moonshotai/kimi-k2.7", "untiered pin-only models must not become routing targets")
	assert.NotContains(t, targets, "deepseek/deepseek-v4-pro-0813", "untiered pin-only models must not become routing targets")
}

// The catalog is 14 rows: 8 tiered routing targets and 6 pin-only rows.
func TestCatalog_TierSplitIsEightRoutingSixPinOnly(t *testing.T) {
	tiered, untiered := 0, 0
	for _, m := range Models {
		if m.Tier == TierUnknown {
			untiered++
			continue
		}
		tiered++
	}
	assert.Equal(t, 8, tiered, "routing (tiered) models")
	assert.Equal(t, 6, untiered, "pin-only (untiered) models")
	assert.Len(t, Models, 14)
}

func TestRoutingTargetSet_ExcludesUnregisteredProviders(t *testing.T) {
	targets := RoutingTargetSet(map[string]struct{}{"other-provider": {}})
	assert.Empty(t, targets, "no aiand key means no dispatchable catalog model")
}

func TestHMMRoutingTargetSetMatchesGenericTargets(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAIAND: {}}
	assert.Equal(t, RoutingTargetSet(available), HMMRoutingTargetSet(available))
}

func TestAllowedAtOrBelow_FiltersOutUnknownTier(t *testing.T) {
	allowed := AllowedAtOrBelow(TierMid)
	_, low := allowed["deepseek/deepseek-v4.1-flash"]
	_, mid := allowed["deepseek/deepseek-v4-pro"]
	_, high := allowed["z-ai/glm-5.3"]
	_, untiered := allowed["deepseek/deepseek-v4-pro-0813"]
	assert.True(t, low)
	assert.True(t, mid)
	assert.False(t, high)
	assert.False(t, untiered)
}

func TestToolUseLowSet_OmitsHealthyModels(t *testing.T) {
	set := ToolUseLowSet()
	for _, id := range []string{"z-ai/glm-5.2", "moonshotai/kimi-k3", "deepseek/deepseek-v4-pro"} {
		_, found := set[id]
		assert.Falsef(t, found, "%s must NOT be in the ToolUseLow set", id)
	}
}

func TestModel_ToolUseQualityDefaultsToUnknown(t *testing.T) {
	var m Model
	assert.Equal(t, ToolUseUnknown, m.ToolUseQuality)
}

func TestAgenticLowSet_IncludesHarnessIncapableModels(t *testing.T) {
	set := AgenticLowSet()
	_, found := set["deepseek/deepseek-v4-flash"]
	assert.True(t, found, "deepseek/deepseek-v4-flash must be marked AgenticLow")
}

func TestAgenticLowSet_OmitsHarnessCapableModels(t *testing.T) {
	set := AgenticLowSet()
	for _, id := range []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "moonshotai/kimi-k2.7"} {
		_, found := set[id]
		assert.Falsef(t, found, "%s must NOT be in the AgenticLow set", id)
	}
}

func TestModel_AgenticUseDefaultsToUnknown(t *testing.T) {
	var m Model
	assert.Equal(t, AgenticUnknown, m.AgenticUse)
}

func TestImageUnsupportedSet_IncludesTextOnlyModels(t *testing.T) {
	set := ImageUnsupportedSet()
	for _, id := range []string{"z-ai/glm-5.2", "z-ai/glm-5.3", "deepseek/deepseek-v4-pro", "moonshotai/kimi-k2.7"} {
		_, found := set[id]
		assert.Truef(t, found, "%s must be flagged ImageInputUnsupported", id)
	}
}

func TestImageUnsupportedSet_OmitsMultimodalModels(t *testing.T) {
	set := ImageUnsupportedSet()
	for _, id := range []string{"moonshotai/kimi-k3", "z-ai/glm-5.3-flash", "deepseek/deepseek-v4.1-flash"} {
		_, found := set[id]
		assert.Falsef(t, found, "%s must NOT be flagged ImageInputUnsupported", id)
	}
}

func TestAcceptsImages(t *testing.T) {
	assert.False(t, AcceptsImages("z-ai/glm-5.3"), "text-only model rejects images")
	assert.True(t, AcceptsImages("moonshotai/kimi-k3"), "multimodal model accepts images")
	// Unknown models default to image-capable so an unrecognized passthrough or
	// force-model target is never wrongly evicted from an image-bearing turn.
	assert.True(t, AcceptsImages("some-future-model"), "unknown model defaults to image-capable")
}

func TestModel_ImageInputDefaultsToUnknown(t *testing.T) {
	var m Model
	assert.Equal(t, ImageInputUnknown, m.ImageInput)
}

func TestContextWindowFor_KnownModels(t *testing.T) {
	assert.Equal(t, 1_048_576, ContextWindowFor("deepseek/deepseek-v4-pro"))
	assert.Equal(t, 1_048_576, ContextWindowFor("deepseek/deepseek-v4-flash"))
	assert.Equal(t, 1_048_576, ContextWindowFor("deepseek/deepseek-v4.1-flash"))
	assert.Equal(t, 262_144, ContextWindowFor("moonshotai/kimi-k2.7"))
	assert.Equal(t, 1_048_576, ContextWindowFor("moonshotai/kimi-k3"))
	assert.Equal(t, 1_048_576, ContextWindowFor("z-ai/glm-5.2"))
	assert.Equal(t, 1_048_576, ContextWindowFor("z-ai/glm-5.3"))
	assert.Equal(t, 1_048_550, ContextWindowFor("z-ai/glm-5.3-flash"))
	assert.Equal(t, 262_144, ContextWindowFor("motif/motif-3"))
	assert.Equal(t, 262_144, ContextWindowFor("qwen/qwen3.8-27b"))
	assert.Equal(t, 262_144, ContextWindowFor("qwen/qwen3.6-27b"))
	assert.Equal(t, 262_144, ContextWindowFor("google/gemma-4-31b"))
	assert.Equal(t, 131_072, ContextWindowFor("openai/gpt-oss-120b"))
	// Unknown model falls back to DefaultContextWindow.
	assert.Equal(t, DefaultContextWindow, ContextWindowFor("not-a-real-model"))
}

func TestValidateDeployed_FlagsMissingAndUntiered(t *testing.T) {
	err := ValidateDeployed([]string{"z-ai/glm-5.3"})
	assert.NoError(t, err)

	err = ValidateDeployed([]string{"definitely-not-a-model"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-a-model")

	// deepseek/deepseek-v4-pro-0813 is priced but has no tier (pin-only).
	err = ValidateDeployed([]string{"deepseek/deepseek-v4-pro-0813"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deepseek/deepseek-v4-pro-0813")
}

func TestGLM53FlashPricing(t *testing.T) {
	p, ok := PriceFor(providers.ProviderAIAND, "z-ai/glm-5.3-flash")
	require.True(t, ok)
	assert.InDelta(t, 0.150, p.InputUSDPer1M, 1e-9)
	assert.InDelta(t, 0.500, p.OutputUSDPer1M, 1e-9)
	assert.InDelta(t, 0.03/0.150, p.CacheReadMultiplier, 1e-9)
}
