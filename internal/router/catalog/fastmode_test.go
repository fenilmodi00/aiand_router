package catalog

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestFastPriceFor_NoCatalogRowHasAFastTier(t *testing.T) {
	for _, id := range []string{
		"deepseek/deepseek-v4-pro",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.3-flash",
		"no-such-model",
	} {
		_, ok := FastPriceFor(providers.ProviderAIAND, id)
		assert.False(t, ok, id)
	}
}

func TestSupportsFastMode(t *testing.T) {
	for _, id := range []string{
		"deepseek/deepseek-v4-pro",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.3-flash",
		"unknown",
	} {
		assert.Falsef(t, SupportsFastMode(id), "%s has no fast tier", id)
	}
}

func TestCatalog_FastPriceOnlyOnFirstPartyBindingsAndAboveList(t *testing.T) {
	for _, m := range Models {
		for _, b := range m.Providers {
			fast, ok := b.FastPricing()
			if !ok {
				continue
			}
			assert.Greater(t, fast.OutputUSDPer1M, b.Price.OutputUSDPer1M, "%s/%s fast output must cost more than list", m.ID, b.Provider)
			assert.Equal(t, b.Price.CacheReadMultiplier, fast.CacheReadMultiplier, "%s/%s cache discount carries over", m.ID, b.Provider)
		}
	}
}
