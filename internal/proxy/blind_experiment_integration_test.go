package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func blindExperimentPassthroughContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 auth.BlindExperimentArmPassthrough,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
}

// aiandRefusalChatSSE is the chat-completions dialect the AIand adapter speaks.
// The requested model's upstream returns a cyber refusal in it; a passthrough
// arm must surface that refusal verbatim instead of re-dispatching.
const aiandRefusalChatSSE = "data: {\"id\":\"chatcmpl_refusal\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"moonshotai/kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"This content was flagged for possible cybersecurity risk.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_refusal\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"moonshotai/kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n" +
	"data: [DONE]\n\n"

func TestProxyOpenAIResponses_BlindPassthroughDoesNotRescueOrMutatePins(t *testing.T) {
	// The requested model (kimi-k3) is served by AIand, so its upstream is the
	// refusal server (speaking the AIand chat-completions dialect); the
	// OpenAI-provider client points at the rescue server as the fallback a
	// passthrough arm must never reach. The refusal is therefore the requested
	// model's own first response, and passthrough must return it verbatim
	// without re-dispatching.
	upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(aiandRefusalChatSSE)}
	upstreams.start(t)
	store := newFakePinStore()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "moonshotai/kimi-k3",
			Reason:   "scorer-decision",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"moonshotai/kimi-k3"}},
		}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-aiand-key", upstreams.aiandURL),
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", newCaptureTelemetry(),
	).
		WithCyberRefusalFallbackModel("deepseek-ai/deepseek-v4.1-flash").
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAIAND: {},
		})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	ctx := blindExperimentPassthroughContext(authedCtx(cyberRefusalInstallationID))
	ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})

	_ = svc.ProxyOpenAIResponses(ctx, []byte(responsesTurnBody), recorder, request)

	refusalHits, fallbackHits := upstreams.counts()
	assert.Equal(t, 1, refusalHits, "the requested model's own upstream serves the turn exactly once")
	assert.Zero(t, fallbackHits, "passthrough must not substitute a fallback model after a refusal")
	assert.Contains(t, recorder.Body.String(), "cybersecurity risk", "the requested model's response must be returned unchanged")
	assert.Equal(t, "moonshotai/kimi-k3", recorder.Header().Get(proxy.HeaderRouterModel))

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.getCalls, "passthrough may read only the explicit force-model control row")
	assert.Empty(t, store.upserts, "passthrough must leave automatic pins untouched")
	assert.Empty(t, store.usages, "passthrough must not write automatic pin history")
	assert.Zero(t, store.incrementCalls)
	assert.Zero(t, store.resetCalls)
	assert.Zero(t, store.overloadIncrementCalls)
	assert.Zero(t, store.overloadResetCalls)
	assert.Empty(t, store.disabledProviders)
}
