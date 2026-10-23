package translate_test

import (
	"encoding/json"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const affinityKey = "0123456789abcdef0123456789abcdef"

func anthropicSrc() []byte {
	return []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"hi"}],"max_tokens":256}`)
}

func promptCacheKey(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	v, ok := doc["prompt_cache_key"].(string)
	return v, ok
}

func TestSessionAffinity_FireworksSetsHeaderNotBody(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:     "deepseek/deepseek-v4-pro",
		TargetProvider:  providers.ProviderAIAND,
		SessionAffinity: affinityKey,
	})
	require.NoError(t, err)

	assert.Equal(t, affinityKey, out.Headers.Get("x-session-affinity"))
	assert.Empty(t, out.Headers.Get("x-session-id"))
	_, hasBody := promptCacheKey(t, out.Body)
	assert.False(t, hasBody, "Fireworks must not carry the OpenAI prompt_cache_key body field")
}

// Makora and Together are serverless OpenAI-compat upstreams that were missing
// from the old literal affinity list, so their turns paid a cold-replica
// prefill. Keying the header off the OpenAI-compat family gives them replica
// stickiness with no per-provider edit.
func TestSessionAffinity_MakoraAndTogetherSetHeader(t *testing.T) {
	for _, provider := range []string{providers.ProviderAIAND, providers.ProviderAIAND} {
		t.Run(provider, func(t *testing.T) {
			env, err := translate.ParseAnthropic(anthropicSrc())
			require.NoError(t, err)

			out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
				TargetModel:     "deepseek/deepseek-v4-pro",
				TargetProvider:  provider,
				SessionAffinity: affinityKey,
			})
			require.NoError(t, err)

			assert.Equal(t, affinityKey, out.Headers.Get("x-session-affinity"))
			assert.Empty(t, out.Headers.Get("x-session-id"))
			_, hasBody := promptCacheKey(t, out.Body)
			assert.False(t, hasBody, "%s must not carry the OpenAI prompt_cache_key body field", provider)
		})
	}
}

func TestSessionAffinity_DirectOpenAIOmitsGrokConvID(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:     "grok-4.6",
		TargetProvider:  providers.ProviderAIAND,
		SessionAffinity: affinityKey,
	})
	require.NoError(t, err)

	assert.Empty(t, out.Headers.Get("x-grok-conv-id"))
}

func metadataUserID(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var doc struct {
		Metadata struct {
			UserID *string `json:"user_id"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	if doc.Metadata.UserID == nil {
		return "", false
	}
	return *doc.Metadata.UserID, true
}

func TestSessionAffinity_AnthropicGatewayPreservesCallerUserID(t *testing.T) {
	src := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"metadata":{"user_id":"caller-session-1"}}`)
	env, err := translate.ParseAnthropic(src)
	require.NoError(t, err)

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{
		TargetModel:     "z-ai/glm-5.3",
		TargetProvider:  providers.ProviderAIAND,
		SessionAffinity: affinityKey,
	})
	require.NoError(t, err)

	v, ok := metadataUserID(t, out.Body)
	require.True(t, ok)
	assert.Equal(t, "caller-session-1", v)
}

func TestSessionAffinity_DirectAnthropicStaysUnhinted(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{
		TargetModel:     "z-ai/glm-5.3",
		TargetProvider:  providers.ProviderAIAND,
		SessionAffinity: affinityKey,
	})
	require.NoError(t, err)

	_, ok := metadataUserID(t, out.Body)
	assert.False(t, ok, "first-party Anthropic must not get a synthetic metadata.user_id")
}

func TestSessionAffinity_AnthropicGatewayEmptyIsNoOp(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	_, ok := metadataUserID(t, out.Body)
	assert.False(t, ok)
}

func TestSessionAffinity_EmptyIsNoOp(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "deepseek/deepseek-v4-pro",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	assert.Empty(t, out.Headers.Get("x-session-affinity"))
	_, hasBody := promptCacheKey(t, out.Body)
	assert.False(t, hasBody)
}

// A same-format OpenAI caller that partitions caching with its own
// prompt_cache_key must keep it — the router's synthetic prefix hash must not
// clobber a deliberate caller key on the keyless fallback path.
func TestSessionAffinity_OpenAIPreservesCallerPromptCacheKey(t *testing.T) {
	const callerKey = "caller-partition-key"
	src := []byte(`{"model":"z-ai/glm-5.3","prompt_cache_key":"` + callerKey + `","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	v, ok := promptCacheKey(t, out.Body)
	require.True(t, ok)
	assert.Equal(t, callerKey, v, "caller-supplied prompt_cache_key must be preserved")
}

// A keyless request with no cacheable prefix (no system, no tools) must stay
// unhinted: hashing the empty prefix would herd every such conversation onto
// one synthetic cache key.
func TestSessionAffinity_OpenAIEmptyPrefixStaysUnhinted(t *testing.T) {
	src := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	_, ok := promptCacheKey(t, out.Body)
	assert.False(t, ok, "a prefix-less keyless request must not carry a synthetic prompt_cache_key")
}

// An empty tools array ("tools":[]) is not a cacheable prefix: gjson's
// .Exists() is true and .Raw is "[]" (non-empty), so a naive presence check
// would herd every such keyless request onto one synthetic key. It must stay
// unhinted just like a request with no tools field at all.
func TestSessionAffinity_OpenAIEmptyToolsArrayStaysUnhinted(t *testing.T) {
	src := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"hi"}],"tools":[]}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3",
		TargetProvider: providers.ProviderAIAND,
	})
	require.NoError(t, err)

	_, ok := promptCacheKey(t, out.Body)
	assert.False(t, ok, "an empty tools array must not count as a cacheable prefix")
}

// StripPromptCacheKey must suppress the affinity hint even when a session key is present.
func TestSessionAffinity_StripPromptCacheKeySkipsInjection(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:         "grok-4.6",
		TargetProvider:      providers.ProviderAIAND,
		SessionAffinity:     affinityKey,
		StripPromptCacheKey: true,
	})
	require.NoError(t, err)

	_, ok := promptCacheKey(t, out.Body)
	assert.False(t, ok, "StripPromptCacheKey must suppress the affinity hint")
}

// Responses turns that promote to /v1/responses must carry the same prompt_cache_key hint;
// otherwise gateway reasoning-tool turns fan across replicas unhinted.
func TestSessionAffinity_ResponsesCarriesPromptCacheKey(t *testing.T) {
	for _, provider := range []string{providers.ProviderAIAND, providers.ProviderAIAND} {
		env, err := translate.ParseAnthropic(anthropicSrc())
		require.NoError(t, err)

		out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{
			TargetModel:     "z-ai/glm-5.3",
			TargetProvider:  provider,
			SessionAffinity: affinityKey,
		})
		require.NoError(t, err)

		v, ok := promptCacheKey(t, out.Body)
		require.True(t, ok, "%s Responses body must carry prompt_cache_key", provider)
		assert.Equal(t, affinityKey, v)
	}
}

// StripPromptCacheKey must suppress the Responses-surface hint too.
func TestSessionAffinity_ResponsesStripPromptCacheKey(t *testing.T) {
	env, err := translate.ParseAnthropic(anthropicSrc())
	require.NoError(t, err)

	out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{
		TargetModel:         "grok-4.6",
		TargetProvider:      providers.ProviderAIAND,
		SessionAffinity:     affinityKey,
		StripPromptCacheKey: true,
	})
	require.NoError(t, err)

	_, ok := promptCacheKey(t, out.Body)
	assert.False(t, ok, "a rejecting endpoint must not receive prompt_cache_key on Responses")
}

// Keyless Responses requests fall back to the stable prefix hash (instructions
// + tools), matching the chat/completions fallback semantics.
func TestSessionAffinity_ResponsesFallbackPrefixHashStable(t *testing.T) {
	src := []byte(`{"model":"z-ai/glm-5.3","system":"you are helpful","messages":[{"role":"user","content":"hi"}],"max_tokens":256}`)
	keys := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		env, err := translate.ParseAnthropic(src)
		require.NoError(t, err)
		out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{
			TargetModel:    "z-ai/glm-5.3",
			TargetProvider: providers.ProviderAIAND,
		})
		require.NoError(t, err)
		v, ok := promptCacheKey(t, out.Body)
		require.True(t, ok, "a prefixed keyless Responses request must carry a fallback key")
		keys = append(keys, v)
	}
	assert.Equal(t, keys[0], keys[1], "fallback Responses prompt_cache_key must be stable")
}
