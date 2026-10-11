package proxy_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedResponseWriter struct {
	header  http.Header
	body    bytes.Buffer
	mu      sync.Mutex
	flushed chan struct{}
	once    sync.Once
}

func newObservedResponseWriter() *observedResponseWriter {
	return &observedResponseWriter{
		header:  make(http.Header),
		flushed: make(chan struct{}),
	}
}

func (w *observedResponseWriter) Header() http.Header {
	return w.header
}

func (w *observedResponseWriter) WriteHeader(int) {}

func (w *observedResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *observedResponseWriter) Flush() {
	w.once.Do(func() {
		close(w.flushed)
	})
}

func (w *observedResponseWriter) BodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// TestProxyMessages_FireworksFailureFallbackToOpenRouter was deleted with the
// AIand-only cut: it asserted cross-provider failover between the Fireworks and
// OpenRouter clients and the OpenRouter-only request gates those providers
// required. Both providers are gone, and every catalog row now carries a single
// AIAND binding, so a same-model cross-provider failover can no longer be
// constructed at all.

// TestProxyMessages_SoleBindingFailureRendersUpstreamError asserts the
// format-specific exhaustion renderer: when the sole binding errors, the
// Anthropic client sees the upstream error envelope translated to Anthropic
// shape via translate.OpenAIToAnthropicError, NOT the raw upstream JSON.
func TestProxyMessages_SoleBindingFailureRendersUpstreamError(t *testing.T) {
	aiand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"aiand also down","type":"upstream_error"}}`))
	}))
	defer aiand.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek-ai/deepseek-v4-pro"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-aiand-key", aiand.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "✦ **Weave Router**")
	assert.NotContains(t, rec.Body.String(), "event: error")
	assert.Contains(t, rec.Body.String(), "aiand also down")
}

// TestProxyMessages_SingleBindingPreservesEagerPrelude asserts that
// single-binding requests (every Anthropic-native model today) still
// fire translator.Prelude eagerly to the client writer — preserving
// main #220's TTFB win. The preludeBuffer is not engaged because
// resolveBindingsForDispatch returns a single-element slice.
func TestProxyMessages_SingleBindingPreservesEagerPrelude(t *testing.T) {
	// An Anthropic-shape upstream that emits SSE chunks. We don't assert
	// the chunks here; we assert that the response is committed (200) and
	// the client sees message_start before message_stop — i.e. the
	// translator's Prelude wasn't swallowed by an inadvertent buffer.
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Minimal valid Anthropic-shape stream.
		for _, c := range []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-haiku-4-5\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		} {
			_, _ = w.Write([]byte(c))
		}
	}))
	defer anth.Close()

	// claude-haiku-4-5 is single-binding (Anthropic only).
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					// Mirror the translator's expected SSE shape.
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-haiku-4-5\"}}\n\n"))
					_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
				},
			},
		},
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
	assert.Equal(t, http.StatusOK, rec.Code)
	respBody := rec.Body.String()
	assert.Contains(t, respBody, "message_start")
	assert.Contains(t, respBody, "message_stop")
	// No fallback header for single-binding requests.
	assert.Empty(t, rec.Header().Get(proxy.HeaderRouterFallbackFrom))
}

func TestProxyMessages_RoutingMarkerWaitsForProviderOutput(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	defer func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	}()
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"},
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					close(providerStarted)
					<-releaseProvider
					_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
				},
			},
		},
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	writer := newObservedResponseWriter()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	done := make(chan error, 1)
	go func() {
		done <- svc.ProxyMessages(context.Background(), body, writer, req)
	}()

	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider was not dispatched")
	}
	select {
	case <-writer.flushed:
		t.Fatal("routing marker committed HTTP 200 before provider output")
	case <-time.After(50 * time.Millisecond):
	}
	assert.Empty(t, writer.BodyString())

	close(releaseProvider)
	require.NoError(t, <-done)
	assert.Contains(t, writer.BodyString(), "✦ **Weave Router**")
}

// TestProxyMessages_SingleBindingStreamingPreCommitError asserts that an
// upstream failure before provider output remains an HTTP error. Claude Code
// can otherwise treat a marker-only 200 stream as a successful turn.
func TestProxyMessages_SingleBindingStreamingPreCommitError(t *testing.T) {
	// Stub upstream OpenAI-compat provider that 503s on every request.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable","type":"upstream_error"}}`))
	}))
	defer stub.Close()

	// gpt-5 is single-binding to openai in catalog; route there from an
	// inbound Anthropic Messages request so the cross-format
	// AnthropicSSETranslator + Prelude path runs.
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "moonshotai/kimi-k3"}},
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("test-key", stub.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
		WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"moonshotai/kimi-k3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	respBody := rec.Body.String()
	assert.NotContains(t, respBody, "event: message_start")
	assert.NotContains(t, respBody, "✦ **Weave Router**")
	assert.Contains(t, respBody, "upstream unavailable", "translated upstream message reaches the client")
}

// TestProxyMessages_OverloadRetriesSameBinding asserts a pre-commit
// upstream overload is retried on the sole binding. The overload is
// an HTTP 529: the openaicompat client has no SSE error-event
// scanner, so the old 200-plus-event:error construct (an
// Anthropic-adapter behavior) cannot be built any more.
func TestProxyMessages_OverloadRetriesSameBinding(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusOverloaded)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_recovered\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"recovered\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}},
		map[string]providers.Client{providers.ProviderAnthropic: openaicompat.NewClient("test-key", upstream.URL)},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noRetrySleep)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
	mu.Lock()
	assert.Equal(t, 2, calls, "the first overload is retried on the sole binding")
	mu.Unlock()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "recovered")
	assert.NotContains(t, rec.Body.String(), "overloaded_error", "the failed attempt must never commit bytes")
}

// TestProxyMessages_OverloadExhaustionRecords529 asserts that
// exhausting the sole binding on upstream overloads leaves a 529 the
// router records as is_error. The overload is an HTTP 529: the
// openaicompat client has no SSE error-event scanner, so the old
// 200-plus-event:error construct (an Anthropic-adapter behavior)
// cannot be built any more.
func TestProxyMessages_OverloadExhaustionRecords529(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer upstream.Close()

	telemetry := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}},
		map[string]providers.Client{providers.ProviderAnthropic: openaicompat.NewClient("test-key", upstream.URL)},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", telemetry,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noRetrySleep)
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
	ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "org-test")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, rec, req)

	var upstreamErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstreamErr, "exhaustion must remain an error so router.call records is_error")
	assert.Equal(t, 529, upstreamErr.Status)
	mu.Lock()
	assert.Equal(t, 3, calls, "initial attempt plus two same-binding retries")
	mu.Unlock()
	assert.Equal(t, 529, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "event: message_start")
	assert.NotContains(t, rec.Body.String(), "event: error")
	assert.Contains(t, rec.Body.String(), "overloaded_error")
	row := telemetry.firstRow(t)
	assert.Equal(t, int32(529), row.UpstreamStatusCode)
}

// TestProxyMessages_TwoConsecutiveOverloadExhaustionsDisableProvider drives
// two full turns exhausting on 529, asserts disabled_providers gains
// "anthropic" after the second, and the third turn excludes it from
// EnabledProviders.
func TestProxyMessages_TwoConsecutiveOverloadExhaustionsDisableProvider(t *testing.T) {
	// AIand speaks through openaicompat, which has no SSE error-event scanner:
	// overload can only arrive as an HTTP 529, the same construct
	// TestProxyMessages_OverloadExhaustionRecords529 drives.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer upstream.Close()

	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderAnthropic,
		Model:         "zai-org/glm-5.3-flash",
		Reason:        "fresh",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash", Reason: "fresh"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAnthropic: openaicompat.NewClient("test-key", upstream.URL)},
		nil, false, nil, store, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithPlannerEnabled(false).
		WithRetrySleep(noRetrySleep) // first-decision-wins: a pin hit serves straight through without scorer-vs-planner EV noise.

	ctx := authedCtx(uuid.New().String())
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	// Turn 1: exhausts on 529, one strike recorded, not yet disabled.
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err1 := svc.ProxyMessages(ctx, body, rec1, req1)
	var upstreamErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err1, &upstreamErr)
	assert.Equal(t, 529, upstreamErr.Status)
	assert.Empty(t, store.disabledProviders, "one exhausted 529 must not yet disable the provider")

	// Turn 2: exhausts on 529 again — second consecutive strike disables
	// anthropic for the session and evicts the pin.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err2 := svc.ProxyMessages(ctx, body, rec2, req2)
	require.ErrorAs(t, err2, &upstreamErr)
	require.Contains(t, store.disabledProviders, providers.ProviderAnthropic,
		"second consecutive exhausted 529 must strike the provider out for the session")

	// Turn 3: pin was evicted, so this is a fresh scorer call. The
	// disabled provider must be excluded from EnabledProviders, proving
	// the exclusion reaches the scorer rather than just the pin row.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	_ = svc.ProxyMessages(ctx, body, rec3, req3)
	require.NotNil(t, fr.capturedReq)
	_, stillEnabled := fr.capturedReq.EnabledProviders[providers.ProviderAnthropic]
	assert.False(t, stillEnabled, "anthropic must be excluded from EnabledProviders on the turn after being struck out")
}

// TestProxyMessages_BaselineOverloadExhaustionDoesNotDisableAnthropic
// asserts that a baseline-rescue Anthropic 529 (OSS primary 503 -> Anthropic
// failover) never disables Anthropic or evicts the unrelated OSS pin.
func TestProxyMessages_BaselineOverloadExhaustionDoesNotDisableAnthropic(t *testing.T) {
	ossUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"oss provider down"}}`))
	}))
	defer ossUpstream.Close()

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	}))
	defer anthropicUpstream.Close()

	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderOpenAI,
		Model:         "deepseek-ai/deepseek-v4-pro",
		Reason:        "fresh",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "deepseek-ai/deepseek-v4-pro", Reason: "fresh"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderOpenAI:    openaicompat.NewClient("test-fw-key", ossUpstream.URL),
			providers.ProviderAnthropic: openaicompat.NewClient("test-key", anthropicUpstream.URL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI:    {},
		providers.ProviderAnthropic: {},
	}).WithPlannerEnabled(false).
		WithRetrySleep(noRetrySleep)

	ctx := authedCtx(uuid.New().String())
	// "model" resolves baselineFor to claude-haiku-4-5 (a known Anthropic
	// catalog model), so a retryable OSS exhaustion rescues onto Anthropic.
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	// Turn 1: OSS primary 503s (retryable), baseline rescues onto Anthropic,
	// which itself exhausts on 529. First Anthropic strike, not yet disabled.
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err1 := svc.ProxyMessages(ctx, body, rec1, req1)
	require.Error(t, err1)
	assert.Empty(t, store.disabledProviders, "one baseline-rescue exhaustion must not yet disable anthropic")

	// Turn 2: same OSS-fails-then-Anthropic-529s sequence. Without the fix,
	// this would be the second consecutive Anthropic strike and disable it.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err2 := svc.ProxyMessages(ctx, body, rec2, req2)
	require.Error(t, err2)
	assert.Empty(t, store.disabledProviders,
		"anthropic must never be disabled for 529s hit only via baseline rescue of an unrelated OSS pin")
	assert.True(t, store.hasPin, "the OSS pin must not be evicted by a baseline-rescue overload strike")
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", store.pin.Model, "the OSS pin's identity must be untouched")
}

func TestProxyMessages_ResponsesFailureBeforeOutputFallsBackToBaseline(t *testing.T) {
	var (
		mu          sync.Mutex
		openAICalls int
	)
	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		openAICalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.failed\n"+
			`data: {"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"server_error","message":"request rejected before output"},"output":[]}}`+"\n\n")
	}))
	defer openAIUpstream.Close()

	// The AIand baseline is OpenAI-compatible: it answers with a
	// chat-completions stream, which the proxy translates back for the client.
	baseline := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl_baseline","object":"chat.completion.chunk","created":1,"model":"zai-org/glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":"recovered"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl_baseline","object":"chat.completion.chunk","created":1,"model":"zai-org/glm-5.3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}}

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "deepseek-ai/deepseek-v4-flash", Reason: "test"}},
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("test-key", openAIUpstream.URL),
			providers.ProviderAIAND:  baseline,
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}],"messages":[{"role":"user","content":"inspect this"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
	mu.Lock()
	assert.GreaterOrEqual(t, openAICalls, 1)
	mu.Unlock()
	require.Len(t, baseline.proxyBodies, 1, "pre-output Responses failure must retry on the requested AIand model")
	assert.Contains(t, rec.Body.String(), "recovered")
	assert.Contains(t, rec.Body.String(), "event: message_stop")
	assert.NotContains(t, rec.Body.String(), "event: error")
}

// A routed model with a larger window than the requested baseline can carry a
// prompt the baseline cannot; a pre-commit exhaustion on the routed model must
// not rescue onto a baseline AIand would 400 as "prompt is too long".
func TestProxyMessages_BaselineFailoverSkipsBaselineOverContextWindow(t *testing.T) {
	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
	}))
	defer openAIUpstream.Close()

	baseline := &fakeProvider{}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "deepseek-ai/deepseek-v4-flash", Reason: "test"}},
		map[string]providers.Client{
			providers.ProviderOpenAI: openaicompat.NewClient("test-key", openAIUpstream.URL),
			providers.ProviderAIAND:  baseline,
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI: {},
		providers.ProviderAIAND:  {},
	}).WithRetrySleep(noRetrySleep)

	// ~300K estimated tokens: fits gpt-5.5 (1.05M) but not qwen3.8-27b (262K).
	filler := strings.Repeat("tool output line ", 300_000*4/len("tool output line "))
	body := []byte(`{"model":"qwen/qwen3.8-27b","stream":true,"messages":[{"role":"user","content":"` + filler + `"}]}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err := svc.ProxyMessages(context.Background(), body, rec, req)
	require.Error(t, err, "the routed model's 502 surfaces when no baseline can fit the prompt")
	assert.Empty(t, baseline.proxyBodies, "baseline failover must not dispatch a prompt larger than the baseline's context window")
}

// sequencedClient is a providers.Client that returns a scripted result
// per call (and captures the prepared body each time) so a test can assert the
// router re-emitted a different body on retry.
type sequencedClient struct {
	mu        sync.Mutex
	bodies    [][]byte
	responses []func(w http.ResponseWriter) error
}

func (c *sequencedClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.mu.Lock()
	i := len(c.bodies)
	c.bodies = append(c.bodies, append([]byte(nil), prep.Body...))
	c.mu.Unlock()
	if i < len(c.responses) {
		return c.responses[i](w)
	}
	return nil
}

func (c *sequencedClient) Passthrough(_ context.Context, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

// TestProxyMessages_OutputConfigFormat400RetriesWithoutIt was deleted with the
// AIand-only cut. The retry only fires when the emitted body still carries
// output_config.format, and resolveAnthropicOverrides drops output_config for
// every target lacking CapAdaptiveThinking — an Anthropic-only capability that
// no roster row has. The premise (the knob goes out as written) can therefore
// no longer be constructed without a deleted claude-* model.

// A 400 that doesn't name the structured-output knob must not burn a second
// upstream call — an identical re-emit would just 400 again.
func TestProxyMessages_UnrelatedAnthropic400NotRetried(t *testing.T) {
	client := &sequencedClient{
		responses: []func(w http.ResponseWriter) error{
			func(http.ResponseWriter) error {
				return &providers.UpstreamErrorResponse{
					Status: http.StatusBadRequest,
					Body:   []byte(`{"message":"messages: at least one message is required"}`),
				}
			},
		},
	}

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "deepseek-ai/deepseek-v4.1-flash"}},
		map[string]providers.Client{providers.ProviderAnthropic: client},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash","stream":true,"max_tokens":1024,` +
		`"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Len(t, client.bodies, 1, "only a knob rejection licenses the unstructured retry")
}
