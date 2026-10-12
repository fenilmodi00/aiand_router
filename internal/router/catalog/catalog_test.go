package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiandRoster is the AIand-only serving roster: the eight models the deploy
// auto-routes on. Every catalog row not named here was cut.
var aiandRoster = []string{
	"deepseek-ai/deepseek-v4-flash",
	"deepseek-ai/deepseek-v4.1-flash",
	"deepseek-ai/deepseek-v4-pro",
	"zai-org/glm-5.3",
	"zai-org/glm-5.3-flash",
	"moonshotai/kimi-k3",
	"qwen/qwen3.8-27b",
	"motif-technologies/motif-3",
}

func TestCatalog_IsTheAIandRosterOnly(t *testing.T) {
	got := make([]string, 0, len(Models))
	for _, m := range Models {
		got = append(got, m.ID)
	}
	assert.ElementsMatch(t, aiandRoster, got, "the catalog must hold exactly the AIand serving roster")
}

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

func TestCatalog_BindingsAreAIandOnly(t *testing.T) {
	for _, m := range Models {
		for i, b := range m.Providers {
			assert.Equalf(t, providers.ProviderAIAND, b.Provider, "model %q binding %d must be AIand", m.ID, i)
		}
	}
}

func TestCatalog_BindingsHavePositivePrice(t *testing.T) {
	for _, m := range Models {
		for i, b := range m.Providers {
			assert.Greaterf(t, b.Price.InputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive InputUSDPer1M", m.ID, i, b.Provider)
			assert.Greaterf(t, b.Price.OutputUSDPer1M, 0.0, "%s binding %d (%s) has non-positive OutputUSDPer1M", m.ID, i, b.Provider)
		}
	}
}

func TestByID_DateStrippedFallback(t *testing.T) {
	// A dated variant resolves to its canonical roster row.
	m, ok := ByID("zai-org/glm-5.3-20260101")
	require.True(t, ok)
	assert.Equal(t, "zai-org/glm-5.3", m.ID)
}

func TestByID_UnknownReturnsFalse(t *testing.T) {
	_, ok := ByID("definitely-not-a-model")
	assert.False(t, ok)
}

func TestPriceFor_UnknownProviderForKnownModel(t *testing.T) {
	// The roster is AIand-only — asking for any other provider must miss.
	for _, provider := range []string{providers.ProviderAnthropic} {
		_, ok := PriceFor(provider, "zai-org/glm-5.3")
		assert.Falsef(t, ok, "roster row must not price under %s", provider)
	}
}

func TestPriceFor_KnownPair(t *testing.T) {
	p, ok := PriceFor(providers.ProviderAIAND, "zai-org/glm-5.3")
	require.True(t, ok)
	assert.Equal(t, 1.00, p.InputUSDPer1M)
	assert.Equal(t, 4.00, p.OutputUSDPer1M)
	assert.Equal(t, 0.30, p.CacheReadMultiplier)
}

func TestPrimaryPriceFor_RosterRows(t *testing.T) {
	p, ok := PrimaryPriceFor("moonshotai/kimi-k3")
	require.True(t, ok)
	assert.Equal(t, 3.00, p.InputUSDPer1M)
	assert.Equal(t, 12.50, p.OutputUSDPer1M)
}

func TestResolveBinding_PicksFirstAvailable(t *testing.T) {
	// Every roster row is AIand-only, so it resolves only when AIand is wired.
	b, ok := ResolveBinding("deepseek-ai/deepseek-v4-pro", map[string]struct{}{providers.ProviderAIAND: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, b.Provider)

	_, ok = ResolveBinding("deepseek-ai/deepseek-v4-pro", map[string]struct{}{
		providers.ProviderAnthropic: {},
	})
	assert.False(t, ok)
}

func TestTierFor_KnownAndUnknown(t *testing.T) {
	assert.Equal(t, TierHigh, TierFor("deepseek-ai/deepseek-v4-pro"))
	assert.Equal(t, TierLow, TierFor("deepseek-ai/deepseek-v4-flash"))
	assert.Equal(t, TierLow, TierFor("zai-org/glm-5.3-flash"))
	assert.Equal(t, TierMid, TierFor("qwen/qwen3.8-27b"))
	assert.Equal(t, TierUnknown, TierFor("definitely-not-a-model"))
}

func TestRoutingTargetSet_FiltersByRegisteredProviders(t *testing.T) {
	withAIand := RoutingTargetSet(map[string]struct{}{providers.ProviderAIAND: {}})
	for _, id := range aiandRoster {
		assert.Containsf(t, withAIand, id, "%s must be a routing target when AIand is wired", id)
	}

	withoutAIand := RoutingTargetSet(map[string]struct{}{providers.ProviderAnthropic: {}})
	assert.Empty(t, withoutAIand, "no roster row is reachable without AIand")
}

func TestAllowedAtOrBelow_FiltersOutHighTier(t *testing.T) {
	allowed := AllowedAtOrBelow(TierMid)
	assert.Contains(t, allowed, "deepseek-ai/deepseek-v4-flash")
	assert.Contains(t, allowed, "qwen/qwen3.8-27b")
	assert.NotContains(t, allowed, "deepseek-ai/deepseek-v4-pro")
	assert.NotContains(t, allowed, "zai-org/glm-5.3")
}

func TestToolUseLowSet_IsEmptyOnTheRoster(t *testing.T) {
	assert.Empty(t, ToolUseLowSet())
}

func TestModel_ToolUseQualityDefaultsToUnknown(t *testing.T) {
	// Zero-value must default to ToolUseUnknown (healthy) so a future iota
	// reorder can't silently flip every catalog row to ToolUseLow.
	var m Model
	assert.Equal(t, ToolUseUnknown, m.ToolUseQuality)
}

func TestAgenticLowSet_IsEmptyOnTheRoster(t *testing.T) {
	assert.Empty(t, AgenticLowSet())
}

func TestModel_AgenticUseDefaultsToUnknown(t *testing.T) {
	var m Model
	assert.Equal(t, AgenticUnknown, m.AgenticUse)
}

func TestImageUnsupportedSet_IncludesTextOnlyModels(t *testing.T) {
	set := ImageUnsupportedSet()
	for _, id := range []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4-flash", "motif-technologies/motif-3"} {
		_, found := set[id]
		assert.Truef(t, found, "%s must be flagged ImageInputUnsupported", id)
	}
}

func TestImageUnsupportedSet_OmitsMultimodalModels(t *testing.T) {
	// glm-5.3-flash, kimi-k3 and qwen3.8-27b are multimodal roster rows and are
	// deliberately left unflagged.
	set := ImageUnsupportedSet()
	for _, id := range []string{"zai-org/glm-5.3-flash", "moonshotai/kimi-k3", "qwen/qwen3.8-27b"} {
		_, found := set[id]
		assert.Falsef(t, found, "%s must NOT be flagged ImageInputUnsupported", id)
	}
}

func TestAcceptsImages(t *testing.T) {
	assert.False(t, AcceptsImages("zai-org/glm-5.3"), "text-only model rejects images")
	assert.True(t, AcceptsImages("moonshotai/kimi-k3"), "multimodal model accepts images")
	// Unknown models default to image-capable so an unrecognized passthrough or
	// force-model target is never wrongly evicted from an image-bearing turn.
	assert.True(t, AcceptsImages("some-future-model"), "unknown model defaults to image-capable")
}

func TestModel_ImageInputDefaultsToUnknown(t *testing.T) {
	// Zero-value must default to ImageInputUnknown (image-capable) so a new
	// first-party model is never silently excluded from image turns.
	var m Model
	assert.Equal(t, ImageInputUnknown, m.ImageInput)
}

func TestContextWindowFor_RosterModels(t *testing.T) {
	assert.Equal(t, 1_048_576, ContextWindowFor("deepseek-ai/deepseek-v4-pro"))
	assert.Equal(t, 1_048_576, ContextWindowFor("deepseek-ai/deepseek-v4-flash"))
	assert.Equal(t, 1_048_576, ContextWindowFor("moonshotai/kimi-k3"))
	// 1,310,720 is Cloudflare-only; the AIand-served GLM-5.3 rows report 1,048,576.
	assert.Equal(t, 1_048_576, ContextWindowFor("zai-org/glm-5.3"))
	assert.Equal(t, 1_048_576, ContextWindowFor("zai-org/glm-5.3-flash"))
	// Most remaining OSS roster rows serve a 256K window.
	assert.Equal(t, 262_144, ContextWindowFor("qwen/qwen3.8-27b"))
	assert.Equal(t, 262_144, ContextWindowFor("motif-technologies/motif-3"))
	// Unknown model falls back to DefaultContextWindow.
	assert.Equal(t, DefaultContextWindow, ContextWindowFor("not-a-real-model"))
}

func TestValidateDeployed_FlagsMissing(t *testing.T) {
	assert.NoError(t, ValidateDeployed([]string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro"}))

	err := ValidateDeployed([]string{"definitely-not-a-model"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-a-model")

	// A retired claude id is no longer deployable.
	err = ValidateDeployed([]string{"claude-opus-4-7"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claude-opus-4-7")
}

// TestResolveBinding_KimiK3AIandOnly pins that kimi-k3 resolves only through
// AIand (2026-10-04 roster decision); its AIand rates are pinned in
// TestAIandPricing.
func TestResolveBinding_KimiK3AIandOnly(t *testing.T) {
	b, ok := ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{providers.ProviderAIAND: {}})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, b.Provider)
	assert.Empty(t, b.UpstreamID)

	_, ok = ResolveBinding("moonshotai/kimi-k3", map[string]struct{}{
		providers.ProviderAnthropic: {},
	})
	assert.False(t, ok)
}

// TestAIandPricing pins the per-1M rates and cache multipliers of the
// AIand-only roster (live-probed 2026-10-04), since billing debits flow
// straight through these numbers.
func TestAIandPricing(t *testing.T) {
	cases := []struct {
		model     string
		inputUSD  float64
		outputUSD float64
		cacheRead float64
	}{
		{"deepseek-ai/deepseek-v4-flash", 0.150, 0.250, 0.08 / 0.150},
		{"deepseek-ai/deepseek-v4.1-flash", 0.300, 0.600, 0.02 / 0.300},
		{"deepseek-ai/deepseek-v4-pro", 1.000, 2.500, 0.25},
		{"zai-org/glm-5.3", 1.000, 4.000, 0.30},
		{"zai-org/glm-5.3-flash", 0.150, 0.500, 0.03 / 0.150},
		{"qwen/qwen3.8-27b", 0.400, 3.000, 0.20 / 0.400},
		{"moonshotai/kimi-k3", 3.000, 12.500, 0.50 / 3.000},
		{"motif-technologies/motif-3", 0.500, 2.000, 0.20 / 0.500},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			p, ok := PriceFor(providers.ProviderAIAND, tc.model)
			require.True(t, ok)
			assert.InDelta(t, tc.inputUSD, p.InputUSDPer1M, 1e-9)
			assert.InDelta(t, tc.outputUSD, p.OutputUSDPer1M, 1e-9)
			assert.InDelta(t, tc.cacheRead, p.CacheReadMultiplier, 1e-9)
		})
	}
}
