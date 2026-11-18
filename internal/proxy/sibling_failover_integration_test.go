package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/providers/openaicompat"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusOverloaded is Anthropic's upstream-overload status; net/http has no constant for it.
const statusOverloaded = 529

const overloadedSSE = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

// siblingClusterDecision routes to an Anthropic model that shares its cluster
// with one Fireworks-served candidate.
func siblingClusterDecision(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.3",
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			PolicyGroup:     "cluster-7",
			CandidateModels: []string{"z-ai/glm-5.3", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"z-ai/glm-5.3":             providers.ProviderAIAND,
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		},
	}
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

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
			Model:    "z-ai/glm-5.3",
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"z-ai/glm-5.3"}},
		}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-anthropic-key", anthropicUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(authedCtx("11111111-1111-1111-1111-111111111111"), body, rec, req)
	require.Error(t, err, "no candidate to degrade to, so the overload surfaces")

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, anthropicCount, 1, "same-binding retries still run")
	assert.Contains(t, rec.Body.String(), "Overloaded", "the deferred upstream error must reach the client")
}
