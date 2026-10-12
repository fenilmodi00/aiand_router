package proxy_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// aiandRescueChatSSE is the OpenAI Responses stream the single OpenAI-compat
// upstream serves for a turn the linked Codex subscription cannot fund. The
// funding preference must not replace the forced model with a
// subscription-covered one. Responses-shaped because every roster model is an
// always-on reasoning model, so an AIand turn promotes to POST /v1/responses.
const aiandRescueChatSSE = `data: {"type":"response.created","response":{"id":"resp_rescue","status":"in_progress"}}` + "\n\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"ok"}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_rescue","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n"

func TestBoostLinkedFirstHonorsForcedModel(t *testing.T) {
	const astraModel = "deepseek-ai/deepseek-v4-pro"
	const opusModel = "zai-org/glm-5.3"
	// Every roster row is AIand-served, so the linked Codex (OpenAI)
	// subscription covers none of them: linked-first funding must still honor
	// the forced model and serve it on infrastructure credentials.
	for _, tc := range []struct {
		name      string
		model     string
		stored    bool
		responses bool
		messages  bool
	}{
		{name: "Astra header", model: astraModel},
		{name: "Astra stored pin", model: astraModel, stored: true},
		{name: "Astra Responses", model: astraModel, responses: true},
		{name: "Astra Messages", model: astraModel, messages: true},
		{name: "Glm Messages", model: opusModel, messages: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: astraModel}}
			upstream := &fakeProvider{proxyResponse: streamResponses(aiandRescueChatSSE)}
			store := newFakePinStore()
			if tc.stored {
				store.hasPin = true
				store.pin = sessionpin.Pin{
					Model: tc.model, Provider: providers.ProviderAIAND, Effort: "high",
					Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour),
				}
			}
			service := proxy.NewService(selection, map[string]providers.Client{
				providers.ProviderAIAND: upstream,
			}, nil, false, nil, store, false, providers.ProviderAIAND, astraModel, nil).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})
			body := `{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"Review this change."}],"max_tokens":4096,"stream":true}`
			if tc.responses {
				body = `{"model":"deepseek-ai/deepseek-v4-pro","input":[{"role":"user","content":"Review this change."}],"stream":true}`
			} else if tc.messages {
				body = `{"model":"zai-org/glm-5.3","messages":[{"role":"user","content":"Review this change."}],"max_tokens":4096,"stream":true}`
			}
			recorder, request := codexSubRequest(t, body)
			if !tc.stored {
				request.Header.Set(proxy.ForceModelHeader, tc.model+":high")
			}
			ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanBoost)
			ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
			var err error
			switch {
			case tc.responses:
				err = service.ProxyOpenAIResponses(ctx, []byte(body), recorder, request)
			case tc.messages:
				err = service.ProxyMessages(ctx, []byte(body), recorder, request)
			default:
				err = service.ProxyOpenAIChatCompletion(ctx, []byte(body), recorder, request)
			}
			require.NoError(t, err)
			assert.Zero(t, selection.routeCalls, "funding preference must not replace a forced model")
			assert.Equal(t, tc.model, recorder.Header().Get(proxy.HeaderRouterModel))
			require.Len(t, upstream.proxyBodies, 1)
			assert.Equal(t, tc.model, gjson.GetBytes(upstream.proxyBodies[0], "model").String())
			if upstream.proxyCreds[0] != nil {
				assert.False(t, upstream.proxyCreds[0].OAuth,
					"a model outside the linked Codex subscription must use infrastructure credentials")
			}
			assert.NotContains(t, recorder.Body.String(), "could not be served")
			assert.Equal(t, http.StatusOK, recorder.Code)
		})
	}
}
