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

const contextManagementBody = `{"model":"deepseek/deepseek-v4-pro","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}}`

func TestContextManagement_InjectsRequiredBeta(t *testing.T) {
	for _, target := range []string{"z-ai/glm-5.2", "z-ai/glm-5.2"} {
		t.Run(target, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(contextManagementBody))
			require.NoError(t, err)

			prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
				TargetModel:   target,
				Capabilities:  router.Lookup(target),
				ModelSwitched: true,
			})

			require.NoError(t, err)
			var body map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &body))
			assert.Equal(t, target, body["model"])
			assert.Contains(t, body, "context_management")
			assert.Equal(t, "context-management-2025-06-27", prep.Headers.Get("anthropic-beta"))
		})
	}
}

func TestContextManagement_DedupesClientBeta(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(contextManagementBody))
	require.NoError(t, err)
	in := http.Header{}
	in.Set("anthropic-beta", "context-management-2025-06-27")

	prep, err := env.PrepareAnthropic(in, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})

	require.NoError(t, err)
	assert.Equal(t, "context-management-2025-06-27", prep.Headers.Get("anthropic-beta"))
}

func TestContextManagement_AbsentDoesNotInjectBeta(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.2",
		Capabilities: router.Lookup("z-ai/glm-5.2"),
	})

	require.NoError(t, err)
	assert.Empty(t, prep.Headers.Get("anthropic-beta"))
}
