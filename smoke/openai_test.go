//go:build smoke

package smoke

import "testing"

// TestOpenAIResponsesAPI exercises the OpenAI Responses-API path, including a tool
// with a typeless optional param — end-to-end companion to strictify_openai_test.go.
func TestOpenAIResponsesAPI(t *testing.T) {
	t.Run("tool with typeless optional arg does not 400", func(t *testing.T) {
		// Optional property with no "type"/"anyOf"/"enum" — deliberately accepts any JSON
		// value verbatim. makeNullable's fallback produced a typeless anyOf[0] that OpenAI 400'd.
		workflowLike := tool("Workflow", "Execute a workflow script", map[string]any{
			"scriptPath": map[string]any{"type": "string", "description": "Path to a workflow script file"},
			"args":       map[string]any{"description": "Optional input value, verbatim — any JSON value, no fixed shape"},
		})

		body := newRequest("smoke-openai-typeless-arg").tokens(64).
			withTool(workflowLike).
			text("Reply with exactly the word: ok. Do not call any tool.").
			build(t)
		r := callModel(t, body, cfg.PinModel)

		requireOKMessage(t, r)
		assertServedByModel(t, r, cfg.PinModel, "aiand")
	})

	t.Run("basic turn served by the pinned model", func(t *testing.T) {
		body := newRequest("smoke-openai-basic").tokens(64).
			text("Reply with exactly the word: ok").build(t)
		r := callModel(t, body, cfg.PinModel)

		requireOKMessage(t, r)
		if r.message.Usage.InputTokens <= 0 {
			t.Errorf("want input_tokens > 0, got %d", r.message.Usage.InputTokens)
		}
		assertServedByModel(t, r, cfg.PinModel, "aiand")
	})
}
