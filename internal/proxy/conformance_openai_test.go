package proxy_test

// OpenAI /v1/chat/completions conformance cases, shared by every OpenAI-compat
// upstream. Each pins a known translation behavior or past regression.

import (
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func aiandClient(baseURL string) providers.Client {
	return openaicompat.NewClient("test-key", baseURL)
}

const weatherTool = `[{"name":"get_weather","description":"Get the weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`

// readTool mirrors the Claude Code Read tool shape: required param, optional
// string, integer, closed schema — for toolcheck validate/repair cases.
const readTool = `[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"pages":{"type":"string"},"limit":{"type":"integer"}},"required":["file_path"],"additionalProperties":false}}]`

func TestConformance_OpenAIChat(t *testing.T) {
	cases := []conformanceCase{
		{
			name:            "openai_chat/basic_text",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-pro",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-pro","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Say hi."}]}`,
			stream:          true,
			upstreamFixture: "openai_chat/basic_text.upstream.sse",
			wantUpstream: func(t *testing.T, path string, body []byte, _ http.Header) {
				assert.True(t, strings.HasSuffix(path, "/chat/completions"), "path=%s", path)
				assert.Equal(t, "deepseek-ai/deepseek-v4-pro", gjson.GetBytes(body, "model").String())
				assert.True(t, gjson.GetBytes(body, "stream").Bool(), "inbound stream must propagate upstream")
				assert.Equal(t, "user", gjson.GetBytes(body, "messages.0.role").String())
			},
		},
		{
			name:            "openai_chat/basic_text_nonstream",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-pro",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-pro","stream":false,"max_tokens":1024,"messages":[{"role":"user","content":"Say hi."}]}`,
			stream:          false,
			upstreamFixture: "openai_chat/basic_text.upstream.json",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.False(t, gjson.GetBytes(body, "stream").Bool(), "non-streaming inbound must not request an upstream stream")
			},
		},
		{
			// Reasoning + tools is promoted to the Responses API (narrow gate
			// bypasses the broad flag), so the tool translation this case pins
			// happens on the Responses wire; the chat-shaped fixture and golden
			// for that turn live under responses/toolcall_stream.
			name:            "openai_chat/toolcall",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":1024,"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]}`,
			stream:          true,
			upstreamFixture: "responses/toolcall.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.Equal(t, "get_weather", gjson.GetBytes(body, "tools.0.name").String(), "Anthropic tool must translate to the Responses tool shape")
			},
		},
		{
			// Guards #293: finish_reason="tool_calls" with no usable tool_use
			// block must demote stop_reason to end_turn, not strand the agent.
			// Promoted to the Responses wire (reasoning + tools), so the
			// degenerate shape is served from the Responses fixture.
			name:            "openai_chat/degenerate_toolcall_demotes",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":1024,"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Run the tool."}]}`,
			stream:          true,
			upstreamFixture: "responses/degenerate_toolcall.upstream.sse",
		},
		{
			name:            "openai_chat/system_prompt",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-pro",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-pro","stream":true,"max_tokens":1024,"system":"You are a helpful assistant.","messages":[{"role":"user","content":"Say hi."}]}`,
			stream:          true,
			upstreamFixture: "openai_chat/basic_text.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.Equal(t, "system", gjson.GetBytes(body, "messages.0.role").String(), "top-level Anthropic system must become a leading system message")
				assert.Contains(t, gjson.GetBytes(body, "messages.0.content").String(), "helpful assistant")
			},
		},
		{
			// Unrepairable args degrade to `{}` (never raw malformed bytes)
			// so the tool_use block stays dispatchable. Promoted to the
			// Responses wire (reasoning + tools).
			name:            "openai_chat/invalid_toolcall_args",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":1024,"tools":` + readTool + `,"messages":[{"role":"user","content":"Read a.go"}]}`,
			stream:          true,
			upstreamFixture: "responses/invalid_toolcall_args.upstream.sse",
		},
		{
			// Normalize+repair: empty-string optional dropped (#339),
			// hallucinated param dropped, "5"-for-integer coerced.
			// Promoted to the Responses wire (reasoning + tools).
			name:            "openai_chat/toolcall_repaired_args",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       aiandClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":1024,"tools":` + readTool + `,"messages":[{"role":"user","content":"Read a.go"}]}`,
			stream:          true,
			upstreamFixture: "responses/toolcall_repaired_args.upstream.sse",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runConformanceCase(t, c) })
	}
}
