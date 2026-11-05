package translate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emittedMessages returns the `messages` array from an emitted OpenAI body.
func emittedMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	raw, _ := doc["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		msg, _ := m.(map[string]any)
		if msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

// systemContent returns the concatenated text content of the first system
// message, or "" when none is present.
func systemContent(msgs []map[string]any) string {
	for _, msg := range msgs {
		if role, _ := msg["role"].(string); role != "system" {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			return c
		case []any:
			var parts []string
			for _, b := range c {
				block, _ := b.(map[string]any)
				if block == nil {
					continue
				}
				if text, _ := block["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

const reminderSnippet = "byte-for-byte"

func TestSystemReminder_AnthropicSource_SkipsWithoutTools(t *testing.T) {
	src := []byte(`{
		"model":"z-ai/glm-5.3",
		"system":"You are a helpful coding assistant.",
		"messages":[{"role":"user","content":"hi"}],
		"max_tokens":256
	}`)
	env, err := translate.ParseAnthropic(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro"})
	require.NoError(t, err)

	assert.NotContains(t, systemContent(emittedMessages(t, out.Body)), reminderSnippet,
		"requests without tools must not get the reminder")
}

func TestSystemReminder_AnthropicSource_NoReminderForNonDeepSeek(t *testing.T) {
	cases := []string{"gpt-5", "moonshotai/kimi-k2.5", "qwen/qwen3-max", "google/gemini-2.5-pro"}
	for _, model := range cases {
		t.Run(model, func(t *testing.T) {
			src := []byte(`{
				"model":"z-ai/glm-5.3",
				"system":"sys",
				"messages":[{"role":"user","content":"hi"}],
				"tools":[{"name":"Edit","description":"edit","input_schema":{"type":"object"}}],
				"max_tokens":256
			}`)
			env, err := translate.ParseAnthropic(src)
			require.NoError(t, err)

			out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: model})
			require.NoError(t, err)

			assert.NotContains(t, systemContent(emittedMessages(t, out.Body)), reminderSnippet,
				"non-deepseek targets must not receive the reminder")
		})
	}
}

func TestSystemReminder_OpenAISource_SkipsWithoutTools(t *testing.T) {
	src := []byte(`{
		"model":"x",
		"messages":[
			{"role":"system","content":"be concise"},
			{"role":"user","content":"hi"}
		],
		"max_tokens":256
	}`)
	env, err := translate.ParseOpenAI(src)
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4-pro"})
	require.NoError(t, err)

	assert.NotContains(t, systemContent(emittedMessages(t, out.Body)), reminderSnippet)
}
