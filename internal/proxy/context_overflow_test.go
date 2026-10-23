package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/hmm"
	"aiand-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const overflowInstallationID = "00000000-0000-0000-0000-000000000001"

func overflowingProvider(body string) *fakeProvider {
	return &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(body)}}
}

// An upstream overflow must reach Claude Code as its native prompt-too-long,
// never the upstream's own body: the handler renders the classified error.
func TestProxyMessages_UpstreamOverflowIsLeftForNativeRendering(t *testing.T) {
	upstream := overflowingProvider(`{"error":{"message":"Your input exceeds the context window of this model.","code":"context_length_exceeded"}}`)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAIAND: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	)
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"z-ai/glm-5.3-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	err := svc.ProxyMessages(authedCtx(overflowInstallationID), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind)
	assert.Len(t, upstream.proxyBodies, 1, "an overflow is not retried against the same window")
	assert.Zero(t, rec.Body.Len(), "the upstream's OpenAI-shaped body must not reach an Anthropic client")
}

func proxyResponsesOverflow(t *testing.T, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	upstream := overflowingProvider(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAIAND: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	)
	rec := httptest.NewRecorder()
	body := `{"model":"z-ai/glm-5.3-flash","input":"hi","stream":false}`
	if stream {
		body = `{"model":"z-ai/glm-5.3-flash","input":"hi","stream":true}`
	}
	err := svc.ProxyOpenAIResponses(authedCtx(overflowInstallationID), []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind)
	return rec
}

// Codex compacts only on an in-stream response.failed carrying
// context_length_exceeded; opencode classifies the same shape.
func TestProxyOpenAIResponses_StreamingOverflowIsNativeResponseFailed(t *testing.T) {
	rec := proxyResponsesOverflow(t, true)
	assert.Equal(t, http.StatusOK, rec.Code)
	out := rec.Body.String()
	assert.Contains(t, out, "event: response.created")
	assert.Contains(t, out, "event: response.failed")
	assert.Contains(t, out, `"code":"context_length_exceeded"`)
	assert.NotContains(t, out, "1050000 tokens", "the upstream's own body never leaks")
}

func TestProxyOpenAIResponses_NonStreamingOverflowIsNativeError(t *testing.T) {
	rec := proxyResponsesOverflow(t, false)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "context_length_exceeded", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	assert.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}

// responsesOverflowClient answers the way OpenAI's Responses API does for an
// over-window prompt: HTTP 200, then an in-stream error and response.failed.
type responsesOverflowClient struct {
	endpoints []providers.Endpoint
}

func (c *responsesOverflowClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.endpoints = append(c.endpoints, prep.Endpoint)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]},"sequence_number":0}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":1}`,
		`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."},"output":[]},"sequence_number":2}`,
	} {
		if _, err := io.WriteString(w, "data: "+payload+"\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func (c *responsesOverflowClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// overflowPolicy is a sidecar that always picks the first offered candidate.
type overflowPolicy struct{}

func (overflowPolicy) Decide(_ context.Context, query policy.Query) (policy.Result, error) {
	if len(query.Candidates) == 0 {
		return policy.Result{}, errors.New("no candidates offered")
	}
	return policy.Result{Model: query.Candidates[0].CatalogID, Provider: query.Candidates[0].Provider}, nil
}

// hmmOverflowService routes on the HMM policy path over models, all served by
// provider through upstream.
func hmmOverflowService(provider string, upstream providers.Client, models ...string) *proxy.Service {
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[model] = struct{}{}
	}
	resolver := policy.NewResolver(available, map[string]struct{}{provider: {}}, func(m catalog.Model) string { return m.ID })
	routing := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM, Unavailable: hmm.ErrHMMUnavailable}, overflowPolicy{}, resolver)
	return proxy.NewService(&fakeRouter{}, map[string]providers.Client{provider: upstream},
		nil, false, nil, newFakePinStore(), false, provider, models[0], nil).
		WithAvailableModels(available).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: routing, Unavailable: hmm.ErrHMMUnavailable})
}

// A wider-window model the request cannot reach (its provider is not enabled)
// must not mask a total overflow of every servable model: the widest servable
// one is still dispatched so the provider decides.
func TestTotalOverflowIgnoresWiderUnservableModel(t *testing.T) {
	// ~900k-token estimate plus a 128k output reserve: past a 1M window, inside
	// Gemini's 1,048,576 one.
	prompt := strings.Repeat("overflow ", 400_000)
	body := `{"model":"z-ai/glm-5.2","max_tokens":128000,"messages":[{"role":"user","content":"` + prompt + `"}]}`
	upstream := overflowingProvider(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1100000 tokens > 1000000 maximum"}}`)
	svc := hmmOverflowService(providers.ProviderAIAND, upstream, "z-ai/glm-5.2", "gemini-3.5-flash")
	ctx := router.WithStrategy(authedCtx(overflowInstallationID), router.StrategyHMM)

	err := svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind, "got %v", err)
	require.Len(t, upstream.proxyBodies, 1, "the widest servable model is dispatched so the provider decides")
}

// When a policy reason, not size, empties the pool, the org still hears it is
// its model selection to widen.
func TestHMMPolicyEmptiedPoolStaysNoRoutableModels(t *testing.T) {
	upstream := &fakeProvider{}
	svc := hmmOverflowService(providers.ProviderAIAND, upstream, "z-ai/glm-5.2", "z-ai/glm-5.3-flash")
	ctx := context.WithValue(router.WithStrategy(authedCtx(overflowInstallationID), router.StrategyHMM),
		proxy.InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.2", "z-ai/glm-5.3-flash"})
	body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorNoRoutableModels, cls.Kind, "got %v", err)
	assert.Empty(t, upstream.proxyBodies)
}
