package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/planner"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"
)

// The share must come from two MEASURED counters on the pin, not from measured
// cached tokens over an ESTIMATED current total — the latter biases k toward 1,
// which is the assumption the corrected economics exist to remove.
func TestCacheablePrefixUsesMeasuredRatioNotCurrentEstimate(t *testing.T) {
	pin := sessionpin.Pin{
		Provider:             providers.ProviderAIAND,
		LastInputTokens:      10_000,
		LastCachedReadTokens: 90_000,
	}
	// Prior turn: 90k cached of 100k total = 0.9. Projected onto a 50k prompt
	// that is 45k, even though 90k cached exceeds this turn's whole estimate.
	got, known := cacheablePrefixTokens(pin, 50_000, false)
	assert.True(t, known)
	assert.Equal(t, 45_000, got, "share is scale-free; it must not clamp to the current total")
}

// A pin with no usage telemetry must report "unknown" so the planner can take
// the legacy fallback, while a client trim is a MEASURED eviction of zero.
func TestCacheablePrefixDistinguishesUnknownFromMeasuredZero(t *testing.T) {
	_, known := cacheablePrefixTokens(sessionpin.Pin{}, 50_000, false)
	assert.False(t, known, "a pin that never completed a turn has no evidence")

	trimmed, trimmedKnown := cacheablePrefixTokens(
		sessionpin.Pin{Provider: providers.ProviderAIAND, LastInputTokens: 10_000, LastCachedReadTokens: 90_000},
		50_000, true,
	)
	assert.True(t, trimmedKnown, "a client trim is observed, not unknown")
	assert.Zero(t, trimmed)
}

// The corrected token estimate must stay behind the flag. Legacy EV scales
// linearly with token count against a fixed dollar threshold, so feeding it a
// larger number would move STAY/SWITCH the moment this deploys.
func TestPlannerTokensStayLegacyUntilFlagIsArmed(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(
		`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hello"}],` +
			`"tools":[{"name":"t","description":"` + longDescription(4000) + `"}]}`,
	))
	require.NoError(t, err)
	feats := translate.RoutingFeatures{Tokens: 10}

	off := &Service{planner: planner.EVConfig{CorrectedEconomics: false}}
	assert.Equal(t, feats.Tokens, off.plannerTokensFor(env, feats),
		"flag off must leave the legacy estimate untouched")

	on := &Service{planner: planner.EVConfig{CorrectedEconomics: true}}
	assert.Greater(t, on.plannerTokensFor(env, feats), feats.Tokens,
		"flag on counts tool definitions the text-only estimate misses")
}

func longDescription(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}
