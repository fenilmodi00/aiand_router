package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Anthropic Messages requires max_tokens; we inject a per-model default when
// absent. Always-on adaptive targets receive the reasoning floor.

func TestAnthropicSameFormat_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	}
	out := parseAndEmit(t, body, "anthropic", opts)
	assert.Equal(t, float64(16000), out["max_tokens"])
}

func TestAnthropicSameFormat_ExistingMaxTokensUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"max_tokens":1024}`)
	opts := translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	}
	out := parseAndEmit(t, body, "anthropic", opts)
	assert.Equal(t, float64(1024), out["max_tokens"])
}

func TestAnthropicSameFormat_AdaptiveDefaultsHaveReasoningHeadroom(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	for _, model := range []string{
		"z-ai/glm-5.2", "z-ai/glm-5.2", "z-ai/glm-5.2", "z-ai/glm-5.2",
		"z-ai/glm-5.2", "z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4.1-flash",
	} {
		t.Run(model, func(t *testing.T) {
			out := parseAndEmit(t, body, "anthropic", translate.EmitOptions{
				TargetModel: model, Capabilities: router.Lookup(model),
			})
			assert.Equal(t, float64(16000), out["max_tokens"])
		})
	}

	control := parseAndEmit(t, body, "anthropic", translate.EmitOptions{
		TargetModel: "z-ai/glm-5.2", Capabilities: router.Lookup("z-ai/glm-5.2"),
	})
	assert.Equal(t, float64(8192), control["max_tokens"])
}

func TestAnthropicCrossFormat_AdaptiveDefaultHasReasoningHeadroom(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(16000), out["max_tokens"])

	responsesBody := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	conversion, err := translate.ConvertResponsesToChatCompletions(responsesBody)
	require.NoError(t, err)
	env, err = translate.ParseOpenAI(conversion.Body)
	require.NoError(t, err)
	prep, err = env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(16000), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultMaxTokensInjectedForNonReasoningTarget(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_tokens"])
	assert.NotContains(t, out, "max_completion_tokens")
}

func TestOpenAISameFormat_DefaultMaxCompletionTokensInjectedForReasoningTarget(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// gpt-4.1 caps at 32768 above the global default (8192); the global cap is
// the binding floor for the default.
func TestOpenAISameFormat_DefaultCappedByGlobalCap(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4.1",
		Capabilities: router.Lookup("gpt-4.1"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(8192), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultNotInjectedWhenMaxTokensPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":512}`)
	opts := translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(512), out["max_tokens"])
}

func TestOpenAISameFormat_DefaultNotInjectedWhenMaxCompletionTokensPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":512}`)
	opts := translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: router.Lookup("o3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(512), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Regression: pullMaxTokens previously hardcoded 4096; the per-model default replaces it.
func TestCrossFormat_OpenAIToAnthropic_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(16000), out["max_tokens"])
}

// Source omits max_tokens, non-reasoning target: injection populates max_tokens.
func TestCrossFormat_AnthropicToOpenAI_DefaultMaxTokensInjectedWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, float64(8192), out["max_tokens"])
}

// Invariant: default-injection must not mutate the source body bytes.
func TestAnthropicSameFormat_DefaultInjectionPreservesSourceBytes(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`)
	original := make([]byte, len(body))
	copy(original, body)

	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})
	require.NoError(t, err)

	assert.Equal(t, original, body)
}

// Regression: an explicit max_tokens is clamped to modelMaxOutputTokens, whose
// absent-key zero value falls back to the global 8192. Kimi K3 really accepts
// 131072 output tokens, so without an entry a large request was silently
// truncated 16x.
func TestOpenAISameFormat_ExplicitMaxTokensClampsToKimiK3Ceiling(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":32000}`)
	opts := translate.EmitOptions{
		TargetModel:  "moonshotai/kimi-k3",
		Capabilities: router.Lookup("moonshotai/kimi-k3"),
	}
	out := parseAndEmit(t, body, "openai", opts)
	assert.Equal(t, float64(32000), out["max_tokens"])
}

// Regression: both GLM-5.3 arms missing from modelMaxOutputTokens; max_tokens was
// clamped to 8192 — always-on reasoning exhausts that budget before answering.
func TestOpenAISameFormat_ExplicitMaxTokensNotClampedTo8192ForGLM53(t *testing.T) {
	for _, model := range []string{"z-ai/glm-5.3", "z-ai/glm-5.3-flash"} {
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
		opts := translate.EmitOptions{
			TargetModel:  model,
			Capabilities: router.Lookup(model),
		}
		out := parseAndEmit(t, body, "openai", opts)
		assert.Equal(t, float64(64000), out["max_tokens"], model)
	}
}
