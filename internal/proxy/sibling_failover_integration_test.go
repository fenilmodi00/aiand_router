package proxy_test

import (
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
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// statusOverloaded is Anthropic's upstream-overload status; net/http has no constant for it.
const statusOverloaded = 529

const overloadedSSE = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

// siblingClusterDecision routes to an Anthropic stand-in (a provider that does
// not serve the roster row — it only ever answers 529 here) whose cluster
// candidate deepseek-ai/deepseek-v4-pro is served by AIAND.
func siblingClusterDecision(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "zai-org/glm-5.3",
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			PolicyGroup:     "cluster-7",
			CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"zai-org/glm-5.3":             providers.ProviderAIAND,
				"deepseek-ai/deepseek-v4-pro": providers.ProviderAIAND,
			},
		},
	}
}

// TestProxyMessages_OverloadedModelDegradesToSameClusterCandidate: a sole-binding
// Anthropic model 529s on every attempt; the turn must re-dispatch the next
// policy candidate rather than surfacing the overload to the client.
func TestProxyMessages_OverloadedModelDegradesToSameClusterCandidate(t *testing.T) {
	var (
		mu             sync.Mutex
		anthropicCount int
		fireworksCount int
		fireworksModel string
	)

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
	}))
	defer anthropicUpstream.Close()

	// AIand turns are promoted to POST /v1/responses (see
	// translate.UseOpenAIResponsesAPI), so the candidate upstream must answer a
	// Responses stream, not Chat Completions.
	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		fireworksCount++
		fireworksModel = gjson.GetBytes(body, "model").String()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, c := range []string{
			`data: {"type":"response.created","response":{"id":"fw-1","status":"in_progress"}}` + "\n\n",
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi"}` + "\n\n",
			`data: {"type":"response.completed","response":{"id":"fw-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n",
		} {
			_, _ = w.Write([]byte(c))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer fireworks.Close()

	store := newFakePinStore()
	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderAIAND:     openaicompat.NewClient("test-aiand-key", fireworks.URL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderAIAND:     {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "ProxyMessages should succeed via the same-cluster candidate")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "the overloaded model exhausts its same-binding retries first")
	assert.Equal(t, 1, fireworksCount, "the same-cluster candidate is dispatched once")
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", fireworksModel,
		"the candidate request must be re-emitted for the candidate, not the overloaded model")

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "event: message_start", "client sees the candidate's stream")
	assert.Contains(t, respBody, "event: message_stop")
	assert.NotContains(t, respBody, "overloaded_error", "the upstream overload must not reach the client")
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", rec.Header().Get(proxy.HeaderRouterModel))

	require.NotEmpty(t, store.usages, "the served candidate must be recorded on the pin")
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", store.usages[len(store.usages)-1].ServedModel)
}

// rankedRescueFixture routes to an Anthropic model that always overloads and
// names two OpenAI-served candidates, ranked kimi then deepseek; kimi rejects
// every request so the rescue must hand off to deepseek.
type rankedRescueFixture struct {
	svc             *proxy.Service
	mu              sync.Mutex
	fireworksModels []string
}

func newRankedRescueFixture(t *testing.T) *rankedRescueFixture {
	t.Helper()
	f := &rankedRescueFixture{}

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
	}))
	t.Cleanup(anthropicUpstream.Close)

	// Responses-shaped frames: the rescue candidates are AIand models, and a
	// promoted turn dispatches them to POST /v1/responses. A chat/completions
	// turn without tools dispatches them on chat/completions, so the endpoint
	// the sibling was dispatched to decides which dialect it answers in.
	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		f.mu.Lock()
		f.fireworksModels = append(f.fireworksModels, model)
		f.mu.Unlock()
		if model == "moonshotai/kimi-k3" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = io.WriteString(w, `data: {"type":"response.created","response":{"id":"fw-1","status":"in_progress"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi"}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"fw-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1}}}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek-ai/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek-ai/deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`+"\n\n")
		_, _ = io.WriteString(w, `data: [DONE]`+"\n\n")
	}))
	t.Cleanup(fireworks.Close)

	decision := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "zai-org/glm-5.3",
		Metadata: &router.RoutingMetadata{
			PolicyGroup:    "medium",
			RosterFailover: true,
			// The scored pool lists deepseek first; the ranked fallback puts kimi ahead of it.
			CandidateModels: []string{"deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3", "zai-org/glm-5.3"},
			RescueModels:    []string{"zai-org/glm-5.3", "moonshotai/kimi-k3", "deepseek-ai/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"zai-org/glm-5.3":             providers.ProviderAIAND,
				"moonshotai/kimi-k3":          providers.ProviderAIAND,
				"deepseek-ai/deepseek-v4-pro": providers.ProviderAIAND,
			},
		},
	}
	f.svc = proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{
			providers.ProviderAnthropic: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderAIAND:     openaicompat.NewClient("test-aiand-key", fireworks.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderAIAND:     {},
	}).WithRetrySleep(noRetrySleep)
	return f
}

func (f *rankedRescueFixture) assertHandedOffToDeepseek(t *testing.T, rec *httptest.ResponseRecorder, terminalFrame string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"moonshotai/kimi-k3", "deepseek-ai/deepseek-v4-pro"}, f.fireworksModels,
		"ranked-fallback order, then hand-off after the first rescuer is rejected")
	respBody := rec.Body.String()
	assert.Contains(t, respBody, terminalFrame)
	assert.NotContains(t, respBody, "overloaded_error")
	assert.NotContains(t, respBody, "unsupported parameter", "the rejected rescuer's error never reaches the client")
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", rec.Header().Get(proxy.HeaderRouterModel))
}

// TestProxyMessages_FailedRescuerHandsOffToNextRankedCandidate: the ranked
// fallback names the rescue order, and a rescuer that is itself rejected
// pre-commit must not end the turn while another candidate remains.
func TestProxyMessages_FailedRescuerHandsOffToNextRankedCandidate(t *testing.T) {
	f := newRankedRescueFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := f.svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "the second ranked candidate serves the turn")
	f.assertHandedOffToDeepseek(t, rec, "event: message_stop")
}

// TestProxyOpenAIChatCompletion_FailedRescuerHandsOffToNextRankedCandidate: the
// chat/completions surface walks the same ranked rescue order.
func TestProxyOpenAIChatCompletion_FailedRescuerHandsOffToNextRankedCandidate(t *testing.T) {
	f := newRankedRescueFixture(t)
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))

	err := f.svc.ProxyOpenAIChatCompletion(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.NoError(t, err, "the second ranked candidate serves the turn")
	f.assertHandedOffToDeepseek(t, rec, "data: [DONE]")
}

// TestProxyMessages_OverloadAfterCommitKeepsServingModel: once the first
// upstream bytes are on the wire the turn is committed to its model, so a
// mid-stream overload must be rendered in-stream rather than answered by a
// second model whose output would interleave with the first's.
func TestProxyMessages_OverloadAfterCommitKeepsServingModel(t *testing.T) {
	var (
		mu             sync.Mutex
		fireworksCount int
	)
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
		_, _ = io.WriteString(w, overloadedSSE)
	}))
	defer anthropicUpstream.Close()

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fireworksCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fireworks.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: siblingClusterDecision("")},
		map[string]providers.Client{
			providers.ProviderAnthropic: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
			providers.ProviderAIAND:     openaicompat.NewClient("test-fw-key", fireworks.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAnthropic: {},
		providers.ProviderAIAND:     {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, fireworksCount, "no model switch once the response is committed")
	assert.Contains(t, rec.Body.String(), "partial", "the committed stream is preserved")
	assert.Equal(t, "zai-org/glm-5.3", rec.Header().Get(proxy.HeaderRouterModel))
}

// TestProxyMessages_ForceModelOverloadDoesNotDegrade: an explicit /force-model
// request asked for one model specifically; silently serving another would
// answer a question the user didn't ask.
func TestProxyMessages_ForceModelOverloadDoesNotDegrade(t *testing.T) {
	var (
		mu        sync.Mutex
		otherHits int
	)
	// The forced model is AIAND-served; a different model reaching this upstream
	// would prove the force was silently swapped out.
	aiandUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "model").String() != "zai-org/glm-5.3" {
			mu.Lock()
			otherHits++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"error":{"message":"Overloaded","type":"overloaded_error"}}`))
	}))
	defer aiandUpstream.Close()

	forced := siblingClusterDecision(translate.ReasonUserForceModel)
	forced.Provider = providers.ProviderAIAND
	svc := proxy.NewService(
		&fakeRouter{decision: forced},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-aiand-key", aiandUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)

	require.Error(t, err, "the forced model's overload surfaces instead of degrading")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, otherHits, "a forced model is never swapped out")
	assert.Contains(t, rec.Body.String(), "overloaded_error")
}

// TestProxyMessages_OverloadWithoutCandidatesSurfacesUpstreamError: with no
// same-cluster candidate the deferred upstream error must still reach the
// client — a rescue that can't run must never swallow the failure.
func TestProxyMessages_OverloadWithoutCandidatesSurfacesUpstreamError(t *testing.T) {
	var mu sync.Mutex
	anthropicCount := 0
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		anthropicCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer anthropicUpstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "zai-org/glm-5.3",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"zai-org/glm-5.3"}},
		}},
		map[string]providers.Client{
			providers.ProviderAnthropic: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.Error(t, err, "no candidate to degrade to, so the overload surfaces")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "same-binding retries still run")
	assert.Contains(t, rec.Body.String(), "Overloaded", "the deferred upstream error must reach the client")
}

// TestProxyMessages_SubscriptionOverloadSurfacesOnceAfterRetry: a customer
// credential binds the turn to its provider, so sibling rescue is ineligible;
// the Weave-key retry runs and the overload surfaces exactly once.
