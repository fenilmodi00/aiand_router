package translate_test

import (
	"encoding/json"
	"testing"

	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForceModelCommand_UnforceModel(t *testing.T) {
	for _, input := range []string{"/unforce-model", "/ufm", "$unforce-model", "$ufm"} {
		t.Run(input, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, input))
			require.NoError(t, err)

			res, found := env.ExtractForceModelCommand()
			require.True(t, found)
			assert.True(t, res.Clear)
			assert.Empty(t, res.Model)
		})
	}
}

func TestExtractForceModelCommand_DollarAliases(t *testing.T) {
	for _, input := range []string{"$force-model gpt-5", "$fm gpt-5"} {
		t.Run(input, func(t *testing.T) {
			env, err := translate.ParseAnthropic(buildAnthropicBody(t, input))
			require.NoError(t, err)
			res, found := env.ExtractForceModelCommand()
			require.True(t, found)
			assert.Equal(t, "gpt-5", res.Model)
		})
	}
}

func TestExtractForceModelCommand_CodexExecPreamble(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4-pro",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call_skill", "type": "function", "function": map[string]any{"name": "exec", "arguments": "{}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "Script completed\nWall time: 0.0 seconds\nOutput:\n /force-model gpt-5"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.True(t, res.FromToolResult)
	assert.Equal(t, "gpt-5", res.Model)
}

func TestExtractForceModelCommand_CodexExecDocumentationIsNotCommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4-pro",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call_skill", "type": "function", "function": map[string]any{"name": "exec", "arguments": "{}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "Script completed\nOutput:\n---\nname: force-model\n```text\n /force-model gpt-5\n```"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a command example in exec output must not pin the session")
}

func TestExtractForceModelCommand_OpenAIFormat(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "/force-model gpt-5\nhelp me."},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", res.Model)
	assert.False(t, res.Clear)
	assert.Equal(t, "help me.", lastOpenAIUserMessageText(t, env))
}

func TestExtractForceModelCommand_ArrayContent(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "/force-model gpt-5\nHelp me."},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", res.Model)

	stripped := lastUserMessageArrayText(t, env)
	assert.Equal(t, "Help me.", stripped)
}

func TestExtractForceModelCommand_ArrayContentMultipleTextBlocks(t *testing.T) {
	// The directive can land in a non-first text block (e.g. after an
	// injected <command-name> block), so the parser must scan all of them.
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "<command-message>force-model</command-message>\n<command-name>/force-model</command-name>\n<command-args>qwen/qwen3.6-35b-a3b</command-args>"},
					map[string]any{"type": "text", "text": "/force-model qwen/qwen3.6-35b-a3b"},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	res, found := env.ExtractForceModelCommand()
	require.True(t, found, "directive in a non-first text block must still be recognized")
	assert.Equal(t, "qwen/qwen3.6-35b-a3b", res.Model)

	// The directive block should be empty (or stripped) after extraction.
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4.1-flash"})
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw.Body, &got))
	msgs, _ := got["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := last["content"].([]any)
	require.Len(t, blocks, 2)
	second, _ := blocks[1].(map[string]any)
	assert.Equal(t, "", second["text"], "the directive-bearing text block must be stripped")
}

func TestExtractForceModelCommand_AgentToolResultString(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{"role": "user", "content": "/force-model opus"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)
	assert.Equal(t, "", lastUserMessageText(t, env))
}

func TestExtractForceModelCommand_AgentToolResultBlock(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "toolu_skill", "content": "/force-model opus",
				}},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)
}

func TestExtractForceModelCommand_ModelAliasInUserTextAfterAnthropicToolUse(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_output", "name": "exec", "input": map[string]any{},
				}},
			},
			map[string]any{"role": "user", "content": "/model opus"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found, "direct user text must remain eligible after assistant tool use")
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult, "the continuation is still agent-issued")
}

func TestExtractForceModelCommand_ModelAliasInAnthropicToolResultIsIgnored(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_output", "name": "exec", "input": map[string]any{},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "toolu_output", "content": "/model opus",
				}},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "arbitrary Anthropic tool output must not create a persistent model pin")
}

func TestExtractForceModelCommand_AgentSoleTextBlockDropsWholeMessage(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "/force-model opus"}},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	msgs := anthropicMessages(t, env)
	require.Len(t, msgs, 2, "the command-only user message must be dropped, not left with empty content")
	for _, msg := range msgs {
		if blocks, ok := msg["content"].([]any); ok {
			assert.NotEmpty(t, blocks, "no message may be left with an empty content array")
		}
	}
}

func TestExtractForceModelCommand_AgentToolResultSiblingBlockSurvives(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_skill", "content": "Launching skill: fm"},
					map[string]any{"type": "text", "text": "/force-model opus"},
				},
			},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	msgs := anthropicMessages(t, env)
	require.Len(t, msgs, 2)
	blocks, ok := msgs[1]["content"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1, "only the command block is dropped")
	block, _ := blocks[0].(map[string]any)
	assert.Equal(t, "tool_result", block["type"], "the tool_result pairing the assistant tool_use must survive")
}

func TestExtractForceModelCommand_OpenAIToolResult(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_skill", "type": "function",
					"function": map[string]any{"name": "Skill", "arguments": `{"skill":"fm"}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_skill", "content": "/force-model gpt-5"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "gpt-5", cmd.Model)
	assert.True(t, cmd.FromToolResult)

	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5"})
	require.NoError(t, err)
	var prepared map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &prepared))
	msgs, _ := prepared["messages"].([]any)
	require.Len(t, msgs, 3, "the tool message must remain to answer the assistant tool call")
	tool, _ := msgs[2].(map[string]any)
	assert.Equal(t, "", tool["content"], "only the command text is stripped")
}

func TestExtractForceModelCommand_ModelAliasInUserTextAfterOpenAIToolUse(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_exec", "type": "function",
					"function": map[string]any{"name": "exec", "arguments": `{}`},
				}},
			},
			map[string]any{"role": "user", "content": "/model opus"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found, "direct user text must remain eligible after assistant tool use")
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult, "the continuation is still agent-issued")
}

func TestExtractForceModelCommand_ModelAliasInOpenAIToolResultIsIgnored(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "analyze usage"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_exec", "type": "function",
					"function": map[string]any{"name": "exec", "arguments": `{}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_exec", "content": "/model opus"},
		},
	})
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "arbitrary OpenAI tool output must not create a persistent model pin")
}

func TestExtractForceModelCommand_TrailingSystemNoticeAfterUserTurn(t *testing.T) {
	// Claude Code appends a role:"system" deferred-tools notice after the user
	// turn; it must not make the user's command look historical.
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "<system-reminder>context</system-reminder>"},
					map[string]any{"type": "text", "text": "/force-model opus"},
				},
			},
			map[string]any{"role": "system", "content": "The following deferred tools are now available."},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.False(t, cmd.FromToolResult, "a typed command is not agent-issued")
}

func TestExtractForceModelCommand_AgentToolResultWithTrailingSystemNotice(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "do the thing"},
			map[string]any{"role": "system", "content": "deferred tools notice"},
			map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_skill", "name": "Skill", "input": map[string]any{"skill": "fm"},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "toolu_skill", "content": "/force-model opus",
				}},
			},
			map[string]any{"role": "system", "content": "deferred tools notice"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found)
	assert.Equal(t, "opus", cmd.Model)
	assert.True(t, cmd.FromToolResult, "an interleaved system notice must not hide tool provenance")
}

func TestExtractForceModelCommand_IgnoresHistoricalCommand(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "/force-model opus"},
			map[string]any{"role": "assistant", "content": "Acknowledged."},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a command from an earlier turn must not be replayed")
}

func TestExtractForceModelCommand_NoUserMessage(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "assistant", "content": "Hello!"},
		},
		"max_tokens": 1024,
	})
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}

func TestExtractForceModelCommand_GeminiFormatIgnored(t *testing.T) {
	body := mustMarshalJSON(t, map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "/force-model gpt-5"}}},
		},
	})
	env, err := translate.ParseGemini(body)
	require.NoError(t, err)

	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "Gemini format should not be scanned for force-model commands")
}

// buildAnthropicBody creates a minimal Anthropic Messages request with text as
// the sole user message content.
func buildAnthropicBody(t *testing.T, text string) []byte {
	t.Helper()
	return mustMarshalJSON(t, map[string]any{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": text},
		},
		"max_tokens": 1024,
	})
}

func lastUserMessageText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4.1-flash"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] == "user" {
			if content, ok := msg["content"].(string); ok {
				return content
			}
			blocks, _ := msg["content"].([]any)
			if len(blocks) > 0 {
				block, _ := blocks[len(blocks)-1].(map[string]any)
				if block["type"] == "text" {
					return block["text"].(string)
				}
			}
		}
	}
	return ""
}

func lastUserMessageArrayText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4.1-flash"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			if block["type"] == "text" {
				return block["text"].(string)
			}
		}
	}
	return ""
}

func anthropicMessages(t *testing.T, env *translate.RequestEnvelope) []map[string]any {
	t.Helper()
	var body map[string]any
	raw, _ := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "deepseek/deepseek-v4.1-flash"})
	require.NoError(t, json.Unmarshal(raw.Body, &body))
	raws, _ := body["messages"].([]any)
	msgs := make([]map[string]any, 0, len(raws))
	for _, m := range raws {
		msg, _ := m.(map[string]any)
		msgs = append(msgs, msg)
	}
	return msgs
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func lastOpenAIUserMessageText(t *testing.T, env *translate.RequestEnvelope) string {
	t.Helper()
	prep, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &body))
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		if msg["role"] == "user" {
			content, _ := msg["content"].(string)
			return content
		}
	}
	return ""
}
