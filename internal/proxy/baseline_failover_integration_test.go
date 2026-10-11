package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// anthropicMessageSSE is a minimal but well-formed Anthropic Messages stream
// (message_start … message_stop) for stubbing the native Anthropic upstream.
const anthropicMessageSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-8\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// aiandChatSSE is a minimal but well-formed OpenAI Chat Completions stream for
// stubbing the AIand (OpenAI-compatible) upstream. It reports 32000 output
// tokens so a rescue can prove the served turn was recorded on the pin.
const aiandChatSSE = "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"zai-org/glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"zai-org/glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":32000,\"total_tokens\":32005}}\n\n" +
	"data: [DONE]\n\n"

// TestProxyMessages_OSSOutageFailsOverToBaselineAIand: when the cost-routed
// model fails on every binding, the turn re-dispatches the requested model
// (zai-org/glm-5.3) on AIand instead of surfacing "model may not exist" (the
// Redwood demo double outage).
func TestProxyMessages_OSSOutageFailsOverToBaselineAIand(t *testing.T) {
	var (
		mu                   sync.Mutex
		openAICount          int
		aiandCount           int
		aiandReceivedModel   string
		anthropicRescueCount int
	)

	fail503 := func(counter *int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*counter++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"provider unavailable"}}`))
		}
	}
	openAIUpstream := httptest.NewServer(fail503(&openAICount))
	defer openAIUpstream.Close()

	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		aiandCount++
		aiandReceivedModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(aiandChatSSE))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer aiandUpstream.Close()

	// Registered but never a rescue target: the baseline family is AIand.
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicRescueCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.ReplaceAll(anthropicMessageSSE, `"output_tokens":1}`, `"output_tokens":32000}`)))
	}))
	defer anthropicUpstream.Close()

	store := newFakePinStore()
	// A telemetry sink makes usageRequired() true so the usage extractor runs
	// and recordTurnUsage fires with the served-turn token counts.
	tel := newCaptureTelemetry()
	svc := proxy.NewService(
		// Router cost-routes the AIand-bound request to an OpenAI model.
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "moonshotai/kimi-k3"}},
		map[string]providers.Client{
			providers.ProviderOpenAI:    openaicompat.NewClient("test-openai-key", openAIUpstream.URL),
			providers.ProviderAIAND:     openaicompat.NewClient("test-aiand-key", aiandUpstream.URL),
			providers.ProviderAnthropic: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", tel,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI:    {},
		providers.ProviderAIAND:     {},
		providers.ProviderAnthropic: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	// The caller asked for the AIand model; the router's cost decision sent it
	// to OpenAI. The baseline-failover target is the requested model.
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "ProxyMessages should succeed via baseline failover to AIand")

	mu.Lock()
	defer mu.Unlock()
	assert.Positive(t, openAICount, "the OpenAI routed binding was attempted before failover")
	assert.Equal(t, 1, aiandCount, "AIand baseline failover dispatched once")
	assert.Equal(t, 0, anthropicRescueCount, "the Anthropic baseline family was retargeted to AIand")
	assert.Equal(t, "zai-org/glm-5.3", aiandReceivedModel, "baseline failover must request the caller's model on AIand")

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start", "client sees the Anthropic-shaped stream start")
	assert.Contains(t, respBody, "event: message_stop", "client sees the Anthropic-shaped stream end")
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider), "served provider header reflects the baseline failover")
	// The buffered initial marker is replaced before it becomes visible, so the
	// client sees only the model that produced provider output.
	assert.Equal(t, "zai-org/glm-5.3", rec.Header().Get(proxy.HeaderRouterModel), "x-router-model reflects the baseline model that served")
	initialMarker := strings.Index(respBody, "moonshotai/kimi-k3")
	fallbackMarker := strings.Index(respBody, "zai-org/glm-5.3")
	require.Equal(t, -1, initialMarker, "failed initial decision marker stays hidden")
	require.NotEqual(t, -1, fallbackMarker, "fallback correction names the serving model")

	// The session pin must record the baseline model that actually served, not
	// the cost-routed id — otherwise next-turn switch detection is wrong.
	require.NotEmpty(t, store.usages, "baseline failover must write pin usage")
	assert.Equal(t, "zai-org/glm-5.3", store.usages[len(store.usages)-1].ServedModel, "pin usage records the served baseline model")
	assert.Equal(t, 32000, store.usages[len(store.usages)-1].OutputTokens)
	assert.False(t, store.usages[len(store.usages)-1].OutputLimitReached, "the healthy final attempt must not be excluded for its output size")
}

func TestProxyMessages_AuthoritativePolicyNeverChangesModelOnFailover(t *testing.T) {
	var (
		mu          sync.Mutex
		openAICount int
		aiandCount  int
	)
	fail503 := func(counter *int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			*counter++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"provider unavailable"}}`))
		}
	}
	openAIUpstream := httptest.NewServer(fail503(&openAICount))
	defer openAIUpstream.Close()
	// The baseline rescue would land here; an authoritative-per-turn policy must
	// surface the primary failure instead of taking it.
	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		aiandCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer aiandUpstream.Close()

	strategy := router.Strategy("authoritative-failover-test")
	policyRouter := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    "moonshotai/kimi-k3",
		Reason:   "authoritative-test_policy",
		Metadata: &router.RoutingMetadata{
			RouteID:                       "route-authoritative",
			Strategy:                      string(strategy),
			AuthoritativePerTurnSelection: true,
		},
	}}
	svc := proxy.NewService(
		nil,
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("test-openai-key", openAIUpstream.URL),
			providers.ProviderAIAND:  openaicompat.NewClient("test-aiand-key", aiandUpstream.URL),
		},
		nil,
		false,
		nil,
		newFakePinStore(),
		false,
		providers.ProviderAIAND,
		"zai-org/glm-5.3-flash",
		newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   policyRouter,
		Capabilities: policy.Capabilities{
			SchemaVersion:                 policy.SchemaVersionV1,
			AuthoritativePerTurnSelection: true,
		},
	})
	ctx := router.WithStrategy(
		authedCtx("11111111-1111-1111-1111-111111111111"),
		strategy,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(ctx, body, rec, req)

	mu.Lock()
	defer mu.Unlock()
	assert.Positive(t, openAICount)
	assert.Equal(t, 0, aiandCount, "authoritative policy must surface failure instead of substituting another model")
}

// TestProxyMessages_ForcedModelUnavailableDoesNotSubstituteAnthropic ensures a
// forced-model request hard-fails instead of silently substituting Anthropic.
func TestProxyMessages_ForcedModelUnavailableDoesNotSubstituteAnthropic(t *testing.T) {
	var anthropicCount int
	var aiandCount int
	anthropic := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		anthropicCount++
		w.WriteHeader(http.StatusOK)
	}}
	aiand := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		aiandCount++
		w.WriteHeader(http.StatusOK)
	}}
	svc := makeProxyService(
		router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "zai-org/glm-5.3",
			Reason:   translate.ReasonUserForceModel,
		},
		map[string]providers.Client{
			providers.ProviderAnthropic: anthropic,
			providers.ProviderAIAND:     aiand,
		},
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(context.Background(), body, rec, req)
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "forced model zai-org/glm-5.3 unavailable: provider aiand not configured")
	assert.Equal(t, 0, anthropicCount, "forced model requests must never substitute Anthropic")
	assert.Equal(t, 0, aiandCount, "unwired forced provider must not be dispatched")
}

// TestProxyMessages_NoBaselineWhenRequestedModelIsRoutedModel: when the caller
// requested the routed model directly (baseline == routed model), exhaustion
// surfaces the real upstream error instead of masking it via failover.
func TestProxyMessages_NoBaselineWhenRequestedModelIsRoutedModel(t *testing.T) {
	var aiandCount int
	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		aiandCount++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer aiandUpstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "zai-org/glm-5.3"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("k", aiandUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	// Caller requested the routed model directly: baseline == routed model.
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(context.Background(), body, rec, req)
	require.Error(t, err, "exhaustion must surface instead of rescuing onto the model that already failed")
	assert.Positive(t, aiandCount, "the sole binding is attempted before exhaustion; baseline == routed leaves nothing to retry onto")
}

// TestProxyMessages_NoBaselineWhenAIandExcluded: when AIand is excluded,
// baseline failover must not retry against it — the original upstream error
// surfaces instead.
func TestProxyMessages_NoBaselineWhenAIandExcluded(t *testing.T) {
	var aiandCount int
	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		aiandCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer aiandUpstream.Close()

	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"provider unavailable"}}`))
	}))
	defer openAIUpstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "moonshotai/kimi-k3"}},
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("k", openAIUpstream.URL),
			providers.ProviderAIAND:  openaicompat.NewClient("k", aiandUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithExcludedProvidersOverride([]string{providers.ProviderAIAND})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(context.Background(), body, rec, req)
	require.Error(t, err, "the routed failure must surface")
	assert.Equal(t, 0, aiandCount, "baseline failover must not hit AIand when it is excluded")
	assert.NotEqual(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider), "served provider must not be the excluded AIand")
}

// TestProxyMessages_FailedBaselineReportsBaselineProvider: when the routed
// binding and the AIand baseline retry both fail, telemetry must pair the
// baseline model with the AIand provider, not the routed primary.
func TestProxyMessages_FailedBaselineReportsBaselineProvider(t *testing.T) {
	fail := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}
	openAIUpstream := httptest.NewServer(http.HandlerFunc(fail))
	defer openAIUpstream.Close()
	// The AIand baseline retry also fails (503), so winnerIdx stays -1.
	aiandUpstream := httptest.NewServer(http.HandlerFunc(fail))
	defer aiandUpstream.Close()

	tel := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "moonshotai/kimi-k3"}},
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("k", openAIUpstream.URL),
			providers.ProviderAIAND:  openaicompat.NewClient("k", aiandUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", tel,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	// A non-nil installation ID gates the telemetry write.
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	_ = svc.ProxyMessages(ctx, body, rec, req)

	row := tel.firstRow(t)
	assert.Equal(t, "zai-org/glm-5.3", row.DecisionModel, "telemetry records the baseline model after failover")
	assert.Equal(t, providers.ProviderAIAND, row.DecisionProvider, "failed-baseline provider must match the baseline model, not the routed primary")
}

// TestProxyMessages_AuthoritativePolicyFailsOverOnCapabilityRejection verifies
// that a capability rejection rescues via baseline even under authoritative-per-turn policy.
func TestProxyMessages_AuthoritativePolicyFailsOverOnCapabilityRejection(t *testing.T) {
	var (
		mu                 sync.Mutex
		openAICount        int
		aiandCount         int
		aiandReceivedModel string
	)
	rejectCapability := func(counter *int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			*counter++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"deepseek-ai/DeepSeek-V4-Pro is not a multimodal model"}}`))
		}
	}
	openAIUpstream := httptest.NewServer(rejectCapability(&openAICount))
	defer openAIUpstream.Close()
	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		aiandCount++
		aiandReceivedModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(aiandChatSSE))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer aiandUpstream.Close()

	strategy := router.Strategy("authoritative-capability-test")
	policyRouter := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderOpenAI,
		// A non-catalog OpenAI-bound variant: the routed model must differ from
		// the caller's requested model, or baseline rescue has nothing to fall
		// back to (baseline == routed short-circuits).
		Model:  "moonshotai/kimi-k3-preview",
		Reason: "authoritative-capability_policy",
		Metadata: &router.RoutingMetadata{
			RouteID:                       "route-capability",
			Strategy:                      string(strategy),
			AuthoritativePerTurnSelection: true,
		},
	}}
	svc := proxy.NewService(
		nil,
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("test-openai-key", openAIUpstream.URL),
			providers.ProviderAIAND:  openaicompat.NewClient("test-aiand-key", aiandUpstream.URL),
		},
		nil, false, nil, newFakePinStore(), false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   policyRouter,
		Capabilities: policy.Capabilities{
			SchemaVersion:                 policy.SchemaVersionV1,
			AuthoritativePerTurnSelection: true,
		},
	})
	ctx := router.WithStrategy(authedCtx("11111111-1111-1111-1111-111111111111"), strategy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	// kimi-k3 is the AIand-bound multimodal model the caller asked for.
	body := []byte(`{"model":"moonshotai/kimi-k3","stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)
	require.NoError(t, err, "the turn must be rescued on the requested AIand model")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, openAICount, "routed model tried once")
	assert.Equal(t, 1, aiandCount, "rescued on AIand despite authoritative-per-turn selection")
	assert.Equal(t, "moonshotai/kimi-k3", aiandReceivedModel, "rescue targets the model the caller asked for")

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_stop", "client sees a complete stream, not the provider's 400")
	assert.NotContains(t, respBody, "is not a multimodal model", "the raw provider capability error must not reach the client")
}
