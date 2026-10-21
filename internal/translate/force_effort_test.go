package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

// The same override pins gemini-3.x thinkingLevel — the policy forces gemini to
// "low" (effort-immune on hard tasks) regardless of the inbound budget.
func TestForceReasoningEffort_GeminiOverride(t *testing.T) {
	// Inbound high budget would map to thinkingLevel "high"; override forces low.
	body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":31999}}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:          "gemini-3.1-pro-preview",
		Capabilities:         alwaysOnSpec,
		ForceReasoningEffort: "low",
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	gc := out["generationConfig"].(map[string]any)
	tc := gc["thinkingConfig"].(map[string]any)
	assert.Equal(t, "low", tc["thinkingLevel"])
}

// The override also applies on the OpenAI→Gemini cross-format path (the
// request arrives as OpenAI chat/completions and is translated to native Gemini).
func TestForceReasoningEffort_GeminiFromOpenAI(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
		TargetModel:          "gemini-3.1-pro-preview",
		Capabilities:         alwaysOnSpec,
		ForceReasoningEffort: "low",
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	gc := out["generationConfig"].(map[string]any)
	tc := gc["thinkingConfig"].(map[string]any)
	assert.Equal(t, "low", tc["thinkingLevel"])
}

func TestForceEffort_CrossFormatOpenAI(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAI(http.Header{}, translate.EmitOptions{
		TargetModel:  "openrouter/deepseek-r1",
		Capabilities: router.NewSpec(router.CapReasoning),
		ForceEffort:  "high",
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	assert.Equal(t, "high", out["reasoning_effort"])
}

func TestForceEffort_GeminiMaxLevel(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	for _, level := range []string{"max", "xhigh"} {
		t.Run(level, func(t *testing.T) {
			prep, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{
				TargetModel:          "gemini-3.1-pro-preview",
				Capabilities:         alwaysOnSpec,
				ForceReasoningEffort: level,
			})
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			gc := out["generationConfig"].(map[string]any)
			tc := gc["thinkingConfig"].(map[string]any)
			assert.Equal(t, "high", tc["thinkingLevel"])
		})
	}
}

func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestResolveForceEffort applies per-model xhigh cap (xhigh→max on
// non-CapXhighEffort targets).
func TestResolveForceEffort(t *testing.T) {
	cases := []struct {
		name  string
		level string
		spec  router.ModelSpec
		want  string
	}{
		{"xhigh_capable_passes", "xhigh", router.NewSpec(router.CapXhighEffort), "xhigh"},
		{"xhigh_incapable_clamps_to_max", "xhigh", router.NewSpec(), "max"},
		{"low_no_cap", "low", router.NewSpec(), "low"},
		{"max_on_xhigh_ceiling_menu_serves_xhigh", "max", router.NewSpecWithReasoning(
			router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}},
			router.CapReasoning, router.CapXhighEffort), "xhigh"},
		{"max_on_menu_with_max_passes", "max", router.NewSpecWithReasoning(
			router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}},
			router.CapReasoning, router.CapXhighEffort), "max"},
		{"max_on_non_xhigh_target_passes", "max", router.NewSpec(), "max"},
		{"ultra_alias_resolved", "ultra", router.NewSpec(router.CapXhighEffort), "xhigh"},
		{"fast_alias_resolved", "fast", router.NewSpec(), "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, translate.ResolveForceEffort(tc.spec, tc.level))
		})
	}
}
