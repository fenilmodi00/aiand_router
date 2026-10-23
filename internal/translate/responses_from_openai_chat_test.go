package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareChatOnResponses emits a chat/completions request onto the Responses
// wire format and returns the decoded body.
func prepareChatOnResponses(t *testing.T, body string, opts translate.EmitOptions) map[string]any {
	t.Helper()
	env, err := translate.ParseOpenAI([]byte(body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, opts)
	require.NoError(t, err)
	require.Equal(t, providers.EndpointResponses, prep.Endpoint)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	return out
}

func chatOnResponsesOpts() translate.EmitOptions {
	return translate.EmitOptions{TargetModel: "moonshotai/kimi-k3", Capabilities: router.Lookup("moonshotai/kimi-k3")}
}

func TestPrepareOpenAIResponses_FromChatCompletions_ClosesNestedWorkflowArgsSchemas(t *testing.T) {
	out := prepareChatOnResponses(t, `{
	  "model":"copilot",
	  "messages":[{"role":"user","content":"run workflow"}],
	  "tools":[{"type":"function","function":{"name":"run_dynamic_workflow","parameters":{
	    "type":"object",
	    "properties":{"args":{"type":"object","properties":{"workflow":{"type":"string","oneOf":[{"type":"string"},{"type":"null"}]}},"required":["workflow"]}},
	    "required":["args"]
	  }}}]
	}`, translate.EmitOptions{TargetModel: "moonshotai/kimi-k3", Capabilities: router.Lookup("moonshotai/kimi-k3")})

	tools, _ := out["tools"].([]any)
	require.Len(t, tools, 1)
	tool, _ := tools[0].(map[string]any)
	params, _ := tool["parameters"].(map[string]any)
	require.NotNil(t, params)
	assert.Equal(t, false, params["additionalProperties"])
	args := params["properties"].(map[string]any)["args"].(map[string]any)
	assert.Equal(t, false, args["additionalProperties"], "nested args objects must be closed for OpenAI")
	assert.Equal(t, false, tool["strict"], "object unions stay non-strict when strictification cannot preserve their semantics")
}

// A mid-conversation system message must stay in place: hoisting it to the
// front would shift the cacheable prefix on every turn.
func TestPrepareOpenAIResponses_FromChatCompletions_MidConversationSystemStaysInPlace(t *testing.T) {
	out := prepareChatOnResponses(t, `{
      "model":"moonshotai/kimi-k3",
      "messages":[
        {"role":"system","content":"lead"},
        {"role":"user","content":"one"},
        {"role":"system","content":"reminder"},
        {"role":"user","content":"two"}
      ]
    }`, chatOnResponsesOpts())

	assert.Equal(t, "lead", out["instructions"])
	input, _ := out["input"].([]any)
	require.Len(t, input, 3)
	mid, _ := input[1].(map[string]any)
	assert.Equal(t, "system", mid["role"], "the per-turn reminder keeps its position in the history")
}

// Multimodal parts a chat client sends must survive as typed Responses parts
// rather than being dropped on the floor.
func TestPrepareOpenAIResponses_FromChatCompletions_MultimodalParts(t *testing.T) {
	out := prepareChatOnResponses(t, `{
      "model":"moonshotai/kimi-k3",
      "messages":[{"role":"user","content":[
        {"type":"text","text":"what is this"},
        {"type":"image_url","image_url":{"url":"data:image/png;base64,AAA","detail":"high"}},
        {"type":"file","file":{"file_id":"file-123"}}
      ]}]
    }`, chatOnResponsesOpts())

	input, _ := out["input"].([]any)
	require.Len(t, input, 1)
	msg, _ := input[0].(map[string]any)
	parts, _ := msg["content"].([]any)
	require.Len(t, parts, 3)
	types := make([]string, 0, 3)
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		types = append(types, pm["type"].(string))
	}
	assert.Equal(t, []string{"input_text", "input_image", "input_file"}, types)
	img, _ := parts[1].(map[string]any)
	assert.Equal(t, "data:image/png;base64,AAA", img["image_url"])
	assert.Equal(t, "high", img["detail"])
	file, _ := parts[2].(map[string]any)
	assert.Equal(t, "file-123", file["file_id"])
}

// Structured outputs must reach the Responses `text.format` object; dropping
// them would hand the client free-form prose it can't parse.
func TestPrepareOpenAIResponses_FromChatCompletions_ResponseFormat(t *testing.T) {
	out := prepareChatOnResponses(t, `{
      "model":"moonshotai/kimi-k3",
      "messages":[{"role":"user","content":"hi"}],
      "response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}
    }`, chatOnResponsesOpts())

	text, _ := out["text"].(map[string]any)
	require.NotNil(t, text)
	format, _ := text["format"].(map[string]any)
	require.NotNil(t, format)
	assert.Equal(t, "json_schema", format["type"])
	assert.Equal(t, "answer", format["name"])
	assert.Equal(t, true, format["strict"])
	assert.NotNil(t, format["schema"])
}

func TestPrepareOpenAIResponses_FromChatCompletions_NamedToolChoice(t *testing.T) {
	out := prepareChatOnResponses(t, `{
      "model":"moonshotai/kimi-k3",
      "messages":[{"role":"user","content":"hi"}],
      "tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}],
      "tool_choice":{"type":"function","function":{"name":"bash"}}
    }`, chatOnResponsesOpts())

	choice, _ := out["tool_choice"].(map[string]any)
	require.NotNil(t, choice)
	assert.Equal(t, "function", choice["type"])
	assert.Equal(t, "bash", choice["name"])
}

// Requests using a chat-only knob must report as such so the proxy keeps them
// on chat/completions instead of silently dropping the field.
func TestRequiresChatCompletionsParams_OpenAIChatOnlyKnobs(t *testing.T) {
	caps := router.Lookup("moonshotai/kimi-k3")
	for _, tc := range []struct {
		name  string
		extra string
		want  bool
	}{
		{name: "plain turn is expressible", extra: ``},
		{name: "n>1", extra: `,"n":2`, want: true},
		{name: "frequency_penalty", extra: `,"frequency_penalty":0.5`, want: true},
		{name: "presence_penalty", extra: `,"presence_penalty":0.5`, want: true},
		{name: "logprobs", extra: `,"logprobs":true`, want: true},
		{name: "logit_bias", extra: `,"logit_bias":{"123":-100}`, want: true},
		{name: "seed", extra: `,"seed":7`, want: true},
		{name: "n=1 is the default and expressible", extra: `,"n":1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseOpenAI([]byte(`{"model":"moonshotai/kimi-k3","messages":[{"role":"user","content":"hi"}]` + tc.extra + `}`))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.RequiresChatCompletionsParams(caps))
		})
	}
}

// Inline audio has no Responses input part, so such a turn must stay on
// chat/completions rather than lose the audio.
func TestRequiresChatCompletionsParams_OpenAIInlineAudio(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{"model":"moonshotai/kimi-k3","messages":[{"role":"user","content":[
      {"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}
    ]}]}`))
	require.NoError(t, err)
	assert.True(t, env.RequiresChatCompletionsParams(router.Lookup("moonshotai/kimi-k3")))
}

// The compiled validator must cover chat function tools too, or a chat-ingress
// turn would silently skip schema validation an Anthropic one gets.
func TestToolValidator_CompilesOpenAIChatTools(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{"model":"moonshotai/kimi-k3","messages":[{"role":"user","content":"hi"}],
      "tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]}`))
	require.NoError(t, err)
	v := env.ToolValidator()
	require.NotNil(t, v)
	assert.True(t, v.KnownTool("bash"))
	assert.False(t, v.KnownTool("nope"))
}
