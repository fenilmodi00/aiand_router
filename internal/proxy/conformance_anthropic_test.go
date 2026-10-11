package proxy_test

// Anthropic same-format conformance cases. The Anthropic provider is a
// passthrough (no response translation), so these mainly guard request-side
// emit behavior — notably hoisting a role:system message out of the messages
// array, which otherwise 400s on a same-format bounce.

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

// anthropicClient is the AIAND-only stand-in for the deleted Anthropic adapter:
// AIand speaks OpenAI-compatible, so the same-format emit assertions run
// through openaicompat with ProviderAnthropic as the family-selecting label.
func anthropicClient(baseURL string) providers.Client {
	return openaicompat.NewClient("test-key", baseURL)
}

func TestConformance_Anthropic(t *testing.T) {
	cases := []conformanceCase{
		{
			name:            "anthropic/passthrough_text",
			provider:        providers.ProviderAnthropic,
			model:           "zai-org/glm-5.3",
			newClient:       anthropicClient,
			inbound:         `{"model":"zai-org/glm-5.3","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Say hi."}]}`,
			stream:          true,
			upstreamFixture: "anthropic/basic_text.upstream.sse",
			wantUpstream: func(t *testing.T, path string, _ []byte, _ http.Header) {
				// AIand is the sole upstream and speaks OpenAI-compatible, so an
				// Anthropic-format inbound is TRANSLATED, never passed through:
				// the router must emit at the chat-completions surface. The same
				// bounce is what makes anthropic/system_role_hoisted and
				// xhigh_effort_pruned_on_reroute meaningful.
				assert.Equal(t, "/chat/completions", path)
			},
		},
		{
			// Guards router #332: a role:system message inside the messages array
			// must be hoisted to the top-level system field; left in place it 400s
			// on a same-format Anthropic bounce.
			name:            "anthropic/system_role_hoisted",
			provider:        providers.ProviderAnthropic,
			model:           "zai-org/glm-5.3",
			newClient:       anthropicClient,
			inbound:         `{"model":"zai-org/glm-5.3","stream":true,"max_tokens":1024,"messages":[{"role":"system","content":"Be terse."},{"role":"user","content":"hi"}]}`,
			stream:          true,
			upstreamFixture: "anthropic/basic_text.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.Contains(t, gjson.GetBytes(body, "system").Raw, "Be terse", "role:system must be hoisted into the top-level system field")
				assert.NotEqual(t, "system", gjson.GetBytes(body, "messages.0.role").String(), "no system role may remain in the messages array")
			},
		},
		{
			// Guards the re-route 400 class: a session running with
			// output_config.effort="xhigh" (only ever valid for an
			// xhigh-capable source) re-routed onto a roster row must not
			// forward the unusable level. Every roster model's menu tops out
			// at "high" and none is adaptive, so the emit path prunes the
			// client-supplied output_config rather than 400 the turn.
			name:            "anthropic/xhigh_effort_pruned_on_reroute",
			provider:        providers.ProviderAnthropic,
			model:           "deepseek-ai/deepseek-v4.1-flash",
			newClient:       anthropicClient,
			inbound:         `{"model":"deepseek-ai/deepseek-v4-pro","stream":true,"max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`,
			stream:          true,
			upstreamFixture: "anthropic/basic_text.upstream.sse",
			wantUpstream: func(t *testing.T, _ string, body []byte, _ http.Header) {
				assert.Equal(t, "deepseek-ai/deepseek-v4.1-flash", gjson.GetBytes(body, "model").String())
				assert.NotContains(t, string(body), "xhigh",
					"a roster target's menu is low/medium/high; the unusable xhigh level must never reach it")
				assert.Empty(t, gjson.GetBytes(body, "output_config.effort").String(),
					"the non-adaptive roster target prunes the client's output_config entirely")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runConformanceCase(t, c) })
	}
}
