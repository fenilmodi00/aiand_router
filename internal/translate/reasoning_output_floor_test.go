package translate_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"
)

// Claude Code's security-monitor Stage 1: severity-only verdict, tiny budget,
// thinking disabled, stop on the closing tag.
const classifierStage1Body = `{
	"model":"deepseek/deepseek-v4-pro",
	"max_tokens":64,
	"stop_sequences":["</severity>"],
	"thinking":{"type":"disabled"},
	"system":"You are a security monitor for autonomous AI coding agents.",
	"messages":[{"role":"user","content":"Respond with <severity>N</severity> ONLY."}]
}`

func geminiGenConfig(t *testing.T, target string) map[string]any {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:  target,
		Capabilities: reasoningSpec,
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	return out["generationConfig"].(map[string]any)
}

// Gemini 2.5 honors thinking:disabled as thinkingBudget 0, so no thought
// tokens compete with the caller's budget and it passes through unchanged.
func TestPrepareGemini_TinyBudgetKeptWhenThinkingDisabled(t *testing.T) {
	gc := geminiGenConfig(t, "gemini-2.5-flash")
	assert.Equal(t, float64(64), gc["maxOutputTokens"])
	tc := gc["thinkingConfig"].(map[string]any)
	assert.EqualValues(t, 0, tc["thinkingBudget"])
}

// Effort "none" disables reasoning, so the caller's budget is all visible text.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptWhenEffortNone(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:          "o3",
		Capabilities:         reasoningSpec,
		ForceReasoningEffort: "none",
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	assert.Equal(t, float64(64), out["max_completion_tokens"])
}

// Non-reasoning chat targets keep the caller's budget verbatim.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptForNonReasoningTarget(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: router.Lookup("gpt-4o"),
	})
	require.NoError(t, err)
	out := mustUnmarshal(t, prep.Body)
	assert.Equal(t, float64(64), out["max_tokens"])
	assert.Equal(t, []any{"</severity>"}, out["stop"])
}
