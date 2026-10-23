package translate_test

import (
	"encoding/json"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samplerFields reports presence of temperature/top_p on an emitted body.
func samplerFields(t *testing.T, body []byte) (map[string]any, bool, bool) {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	_, hasTemp := doc["temperature"]
	_, hasTopP := doc["top_p"]
	return doc, hasTemp, hasTopP
}

func TestReasoningSamplers_AnthropicSource_KeepsTemperatureForNonGPT5(t *testing.T) {
	src := []byte(`{
		"model":"claude-3-5-haiku-20241022",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0,
		"max_tokens":256
	}`)
	env, err := translate.ParseAnthropic(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:    "deepseek/deepseek-v4-flash",
		TargetProvider: providers.ProviderAIAND,
		Capabilities:   router.Lookup("deepseek/deepseek-v4-flash"),
	})
	require.NoError(t, err)

	_, hasTemp, _ := samplerFields(t, out.Body)
	assert.True(t, hasTemp, "non-reasoning targets keep the client's temperature")
}

func TestReasoningSamplers_OpenAIResponses_DropsTemperatureForOpenAIReasoningModel(t *testing.T) {
	src := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)
	out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{
		TargetModel: "o4-mini", TargetProvider: providers.ProviderAIAND,
		Capabilities: router.NewSpec(router.CapReasoning),
	})
	require.NoError(t, err)
	_, hasTemp, _ := samplerFields(t, out.Body)
	assert.False(t, hasTemp, "OpenAI reasoning models reject temperature independent of model generation")
}

func TestReasoningSamplers_OpenAISource_KeepsTemperatureForOSSReasoning(t *testing.T) {
	src := []byte(`{
		"model":"x",
		"messages":[{"role":"user","content":"hi"}],
		"temperature":0
	}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{
		TargetModel:  "deepseek/deepseek-v4-pro",
		Capabilities: router.NewSpec(router.CapReasoning),
	})
	require.NoError(t, err)

	_, hasTemp, _ := samplerFields(t, out.Body)
	assert.True(t, hasTemp, "OpenRouter OSS reasoning models sample normally")
}
