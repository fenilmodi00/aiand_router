package proxy_test

// OpenAI Responses API conformance: a reasoning-capable target + tools routes
// here instead of chat/completions (the dispatch gate keys on
// router.CapReasoning). Guards #331 (must stream upstream) and #328
// (medium-effort preservation).
//
// The OpenAI wire format is exercised through ProviderOpenAI:
// translate.UseOpenAIResponsesAPI only routes /v1/responses for that provider,
// so it stays the fixture here even though the AIand-only catalog serves the
// model over OpenAI-compat.

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func openAIClient(baseURL string) providers.Client {
	return openaicompat.NewClient("test-key", baseURL+"/v1")
}

func TestConformance_OpenAIResponses(t *testing.T) {
	// A CapReasoning target + tools is what trips the Responses dispatch; the
	// roster reasons always-on, so the level rides output_config.effort rather
	// than a legacy thinking budget (which roster models reject).
	const reasoningToolTurn = `"max_tokens":2048,"output_config":{"effort":"high"},"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]`

	cases := []conformanceCase{
		{
			// Full Responses-SSE -> Anthropic translation plus the load-bearing stream:true guard.
			name:            "responses/toolcall_stream",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       openAIClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,` + reasoningToolTurn + `}`,
			stream:          true,
			upstreamFixture: "responses/toolcall.upstream.sse",
			wantUpstream: func(t *testing.T, path string, body []byte, _ http.Header) {
				assert.Equal(t, "/v1/responses", path, "reasoning+tools must use the Responses API, not chat/completions")
				assert.True(t, gjson.GetBytes(body, "stream").Bool(),
					"Responses request MUST set stream:true — a stream:false regression reintroduces the #331 header-timeout hang")
				assert.Equal(t, "high", gjson.GetBytes(body, "reasoning.effort").String())
			},
		},
		{
			// Non-streaming client still streams UPSTREAM (#331) and gets a
			// reconstructed one-shot Anthropic body.
			name:            "responses/toolcall_nonstream_client",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       openAIClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":false,` + reasoningToolTurn + `}`,
			stream:          false,
			upstreamFixture: "responses/toolcall.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.True(t, gjson.GetBytes(body, "stream").Bool(),
					"Responses upstream MUST stream even for a non-streaming client (#331)")
			},
		},
		{
			// Translation must preserve a valid client-selected medium level.
			name:            "responses/effort_medium_preserved",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       openAIClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":2048,"output_config":{"effort":"medium"},"tools":` + weatherTool + `,"messages":[{"role":"user","content":"Weather in NYC?"}]}`,
			stream:          true,
			upstreamFixture: "responses/toolcall.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.Equal(t, "medium", gjson.GetBytes(body, "reasoning.effort").String())
			},
		},
		{
			// Strictifiable schemas must go out strict:true, additionalProperties:false,
			// all-required, optionals as null unions.
			name:            "responses/strict_tools",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       openAIClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":2048,"output_config":{"effort":"high"},"tools":` + readTool + `,"messages":[{"role":"user","content":"Read a.go"}]}`,
			stream:          true,
			upstreamFixture: "responses/toolcall.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				tool := gjson.GetBytes(body, "tools.0")
				assert.True(t, tool.Get("strict").Bool(), "strictifiable schema must opt into strict mode")
				params := tool.Get("parameters")
				assert.False(t, params.Get("additionalProperties").Bool(), "strict requires additionalProperties:false")
				required := []string{}
				params.Get("required").ForEach(func(_, r gjson.Result) bool {
					required = append(required, r.String())
					return true
				})
				assert.ElementsMatch(t, []string{"file_path", "pages", "limit"}, required,
					"strict requires every property listed in required")
				assert.Equal(t, `["string","null"]`, params.Get("properties.pages.type").Raw,
					"originally-optional params must become null unions, not stay omittable")
			},
		},
		{
			// toolcheck must repair a truncated function_call arguments payload
			// (missing closing brace) before it reaches the client.
			name:            "responses/invalid_toolcall",
			provider:        providers.ProviderAIAND,
			model:           "deepseek-ai/deepseek-v4-flash",
			newClient:       openAIClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-flash","stream":true,"max_tokens":2048,"output_config":{"effort":"high"},"tools":` + readTool + `,"messages":[{"role":"user","content":"Read a.go"}]}`,
			stream:          true,
			upstreamFixture: "responses/invalid_toolcall.upstream.sse",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runConformanceCase(t, c) })
	}
}
