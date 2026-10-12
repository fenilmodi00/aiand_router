package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// Claude Code's security-monitor Stage 1: severity-only verdict, tiny budget,
// thinking disabled, stop on the closing tag.
const classifierStage1Body = `{
	"model":"claude-sonnet-5",
	"max_tokens":64,
	"stop_sequences":["</severity>"],
	"thinking":{"type":"disabled"},
	"system":"You are a security monitor for autonomous AI coding agents.",
	"messages":[{"role":"user","content":"Respond with <severity>N</severity> ONLY."}]
}`

// chat/completions reasoning targets spend max_completion_tokens on reasoning
// before visible text, so a tiny Anthropic budget is floored there too.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetFlooredForReasoningTarget(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "o3",
		Capabilities: capsFor("o3"),
	})
	require.NoError(t, err)
	out := mustUnmarshalJSON(t, prep.Body)
	assert.Equal(t, float64(16000), out["max_completion_tokens"])
	assert.NotContains(t, out, "max_tokens")
}

// Effort "none" disables reasoning, so the caller's budget is all visible text.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptWhenEffortNone(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:          "o3",
		Capabilities:         capsFor("o3"),
		ForceReasoningEffort: "none",
	})
	require.NoError(t, err)
	out := mustUnmarshalJSON(t, prep.Body)
	assert.Equal(t, float64(64), out["max_completion_tokens"])
}

// Non-reasoning chat targets keep the caller's budget verbatim.
func TestPrepareOpenAI_FromAnthropic_TinyBudgetKeptForNonReasoningTarget(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(classifierStage1Body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-4o",
		Capabilities: capsFor("gpt-4o"),
	})
	require.NoError(t, err)
	out := mustUnmarshalJSON(t, prep.Body)
	assert.Equal(t, float64(64), out["max_tokens"])
	assert.Equal(t, []any{"</severity>"}, out["stop"])
}

func mustUnmarshalJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}
