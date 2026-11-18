package proxy

import (
	"testing"

	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
)

// Ported from upstream 7d1eed8a (#1862), with catalog model names adapted to
// v0's aiand catalog. The upstream integration tests were dropped: they drive
// the multi-provider dispatch path v0 does not serve.

func TestHigherTierCandidates_TopTierFirst(t *testing.T) {
	got := higherTierCandidates(catalog.TierLow,
		[]string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro"},
		map[string]float32{"z-ai/glm-5.3": 0.9, "moonshotai/kimi-k3.1-pro": 0.4, "deepseek/deepseek-v4-pro": 0.99})
	assert.Equal(t, []string{"z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro", "deepseek/deepseek-v4-pro"}, got)
	assert.Empty(t, higherTierCandidates(catalog.TierHigh, []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.3"}, nil))
}

func TestLowerTierCandidates(t *testing.T) {
	assert.Equal(t, []string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro"},
		lowerTierCandidates("z-ai/glm-5.3", []string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro"}))
}
