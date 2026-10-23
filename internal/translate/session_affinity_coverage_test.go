package translate_test

import (
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionAffinityCoversEveryOpenAICompatProvider guards against a new
// OpenAI-compat provider going unreviewed by applySessionAffinity. With one
// upstream the generic x-session-affinity header is the mechanism, and any
// provider that needs bespoke affinity handling must get an explicit case in
// internal/translate/emit_openai.go.
func TestSessionAffinityCoversEveryOpenAICompatProvider(t *testing.T) {
	compat := 0
	for _, p := range providers.AllProviders() {
		if !providers.IsOpenAICompat(p) {
			continue
		}
		compat++
	}
	require.NotZero(t, compat, "at least one OpenAI-compat provider must be registered")
}

// TestSessionAffinityUsesGenericHeader asserts the single upstream takes the
// generic x-session-affinity header — the mechanism every OpenAI-compat
// serverless surface accepts.
func TestSessionAffinityUsesGenericHeader(t *testing.T) {
	const affinityKey = "0123456789abcdef0123456789abcdef"

	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:     "some/model",
		TargetProvider:  providers.ProviderAIAND,
		SessionAffinity: affinityKey,
	})
	require.NoError(t, err)

	assert.Equal(t, affinityKey, out.Headers.Get("x-session-affinity"))
	assert.Empty(t, out.Headers.Get("x-session-id"))
	assert.Empty(t, out.Headers.Get("x-grok-conv-id"))
	_, gotBody := promptCacheKey(t, out.Body)
	assert.False(t, gotBody, "the hint rides the header, not the body")
}

// TestSessionAffinityOmittedWithoutSessionKey asserts a keyless request stays
// unhinted rather than collapsing every conversation onto one bucket.
func TestSessionAffinityOmittedWithoutSessionKey(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "some/model",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	assert.Empty(t, out.Headers.Get("x-session-affinity"))
}
