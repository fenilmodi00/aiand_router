package translate_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptiveReasoningDelegatesToCrossFormatTargetDefault(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`))
	require.NoError(t, err)

	t.Run("OpenAI Responses", func(t *testing.T) {
		prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3")})
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(prep.Body, &out))
		assert.NotContains(t, out, "reasoning")
	})

}

func TestApplyReasoningIntent_ClampsAndRejectsUnsupportedSemantics(t *testing.T) {
	spec := router.NewSpecWithReasoning(router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}})
	clamped, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningLevel, Level: "xhigh", Explicit: true}, spec, "")
	require.NoError(t, err)
	assert.Equal(t, "high", clamped.Level)
	assert.NotEmpty(t, clamped.NormalizationNotes)

	_, err = translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningBudget, BudgetTokens: 2048, Explicit: true}, spec, "")
	require.ErrorIs(t, err, translate.ErrReasoningIncompatible)
}

// A target whose menu covers every level passes each one through untouched,
// and a disable clamps to its floor level because it always reasons.
func TestApplyReasoningIntent_AcceptsEveryLevelOnTheMenuAndNeverDisables(t *testing.T) {
	spec := router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, AlwaysOn: true},
		router.CapReasoning, router.CapXhighEffort)
	for _, level := range []string{"low", "medium", "high", "xhigh"} {
		got, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningLevel, Level: level, Explicit: true}, spec, "")
		require.NoError(t, err, level)
		assert.Equal(t, level, got.Level)
		assert.Empty(t, got.NormalizationNotes, level)
	}
	got, err := translate.ApplyReasoningIntent(translate.ReasoningIntent{Kind: translate.ReasoningDisabled, Explicit: true}, spec, "")
	require.NoError(t, err)
	assert.Equal(t, translate.ReasoningLevel, got.Kind)
	assert.Equal(t, "low", got.Level)
	assert.Equal(t, "xhigh", translate.ResolveForceEffort(spec, "ultra"))
}

func TestPrepareAnthropic_ClientCacheControlFidelity(t *testing.T) {
	t.Run("ttl is preserved and router uses remaining capacity", func(t *testing.T) {
		env, err := translate.ParseOpenAI([]byte(`{"messages":[{"role":"system","content":"rules","cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"user","content":"hi"}]}`))
		require.NoError(t, err)
		prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3"})
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(prep.Body, &out))
		system := out["system"].([]any)
		assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"}, system[0].(map[string]any)["cache_control"])
		lastMessage := out["messages"].([]any)[0].(map[string]any)
		lastBlock := lastMessage["content"].([]any)[0].(map[string]any)
		assert.Equal(t, map[string]any{"type": "ephemeral"}, lastBlock["cache_control"])
	})

	t.Run("explicit overflow returns a stable validation error", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"one","cache_control":{"type":"ephemeral"}},{"role":"system","content":"two","cache_control":{"type":"ephemeral"}},{"role":"system","content":"three","cache_control":{"type":"ephemeral"}},{"role":"system","content":"four","cache_control":{"type":"ephemeral"}},{"role":"system","content":"five","cache_control":{"type":"ephemeral"}}]}`)
		env, err := translate.ParseOpenAI(body)
		require.NoError(t, err)
		_, err = env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3"})
		require.ErrorIs(t, err, translate.ErrAnthropicCacheControlOverflow)
		assert.False(t, errors.Is(err, translate.ErrAnthropicCacheControlInvalid))
	})
}
