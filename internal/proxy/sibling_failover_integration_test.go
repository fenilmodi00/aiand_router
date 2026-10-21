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

// siblingClusterDecision routes to an Anthropic model that shares its cluster
// with one Fireworks-served candidate.
func siblingClusterDecision(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.2",
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			PolicyGroup:     "cluster-7",
			CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"z-ai/glm-5.2":             providers.ProviderAIAND,
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		},
	}
}

// rankedRescueFixture routes to an Anthropic model that always overloads and
// names two Fireworks-served candidates, ranked kimi then deepseek; kimi rejects
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

	fireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		f.mu.Lock()
		f.fireworksModels = append(f.fireworksModels, model)
		f.mu.Unlock()
		if model == "moonshotai/kimi-k2.6" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"fw-1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(fireworks.Close)

	decision := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.2",
		Metadata: &router.RoutingMetadata{
			PolicyGroup:    "medium",
			RosterFailover: true,
			// The scored pool lists deepseek first; the ranked fallback puts kimi ahead of it.
			CandidateModels: []string{"deepseek/deepseek-v4-pro", "moonshotai/kimi-k2.6", "z-ai/glm-5.2"},
			RescueModels:    []string{"z-ai/glm-5.2", "moonshotai/kimi-k2.6", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"z-ai/glm-5.2":             providers.ProviderAIAND,
				"moonshotai/kimi-k2.6":     providers.ProviderAIAND,
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		},
	}
	f.svc = proxy.NewService(
		&fakeRouter{decision: decision},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(noRetrySleep)
	return f
}

func (f *rankedRescueFixture) assertHandedOffToDeepseek(t *testing.T, rec *httptest.ResponseRecorder, terminalFrame string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"moonshotai/kimi-k2.6", "deepseek/deepseek-v4-pro"}, f.fireworksModels,
		"ranked-fallback order, then hand-off after the first rescuer is rejected")
	respBody := rec.Body.String()
	assert.Contains(t, respBody, terminalFrame)
	assert.NotContains(t, respBody, "overloaded_error")
	assert.NotContains(t, respBody, "unsupported parameter", "the rejected rescuer's error never reaches the client")
	assert.Equal(t, "deepseek/deepseek-v4-pro", rec.Header().Get(proxy.HeaderRouterModel))
}

// TestProxyMessages_ForceModelOverloadDoesNotDegrade: an explicit /force-model
// request asked for one model specifically; silently serving another would
// answer a question the user didn't ask.
func TestProxyMessages_ForceModelOverloadDoesNotDegrade(t *testing.T) {
	var (
		mu             sync.Mutex
		fireworksCount int
	)
	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(overloadedSSE))
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
		&fakeRouter{decision: siblingClusterDecision(translate.ReasonUserForceModel)},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)

	require.Error(t, err, "the forced model's overload surfaces instead of degrading")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, fireworksCount, "a forced model is never swapped out")
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
			Provider: providers.ProviderAIAND,
			Model:    "z-ai/glm-5.2",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"z-ai/glm-5.2"}},
		}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.Error(t, err, "no candidate to degrade to, so the overload surfaces")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "same-binding retries still run")
	assert.Contains(t, rec.Body.String(), "Overloaded", "the deferred upstream error must reach the client")
}
