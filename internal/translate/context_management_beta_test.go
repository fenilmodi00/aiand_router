package translate_test

import (
	"net/http"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const contextManagementBody = `{"model":"deepseek/deepseek-v4-pro","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}}`

func TestContextManagement_DedupesClientBeta(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(contextManagementBody))
	require.NoError(t, err)
	in := http.Header{}
	in.Set("anthropic-beta", "context-management-2025-06-27")

	prep, err := env.PrepareAnthropic(in, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.3",
		Capabilities: router.Lookup("z-ai/glm-5.3"),
	})

	require.NoError(t, err)
	assert.Equal(t, "context-management-2025-06-27", prep.Headers.Get("anthropic-beta"))
}

func TestContextManagement_AbsentDoesNotInjectBeta(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "z-ai/glm-5.3",
		Capabilities: router.Lookup("z-ai/glm-5.3"),
	})

	require.NoError(t, err)
	assert.Empty(t, prep.Headers.Get("anthropic-beta"))
}
