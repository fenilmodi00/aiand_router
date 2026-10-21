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
		router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash"},
		map[string]providers.Client{
			providers.ProviderAIAND: &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					// A retry may reach the provider again; the signal closes once.
					select {
					case <-providerStarted:
					default:
						close(providerStarted)
					}
					<-releaseProvider
					// The upstream is the OpenAI-compatible surface and the turn
					// streams, so it answers with a chat/completions SSE the
					// translator reads.
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
					_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				},
			},
		},
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	writer := newObservedResponseWriter()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
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
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-key", stub.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithRetrySleep(noRetrySleep)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	respBody := rec.Body.String()
	assert.NotContains(t, respBody, "event: message_start")
	assert.NotContains(t, respBody, "✦ **Weave Router**")
	assert.Contains(t, respBody, "upstream unavailable", "translated upstream message reaches the client")
}

func TestProxyMessages_AnthropicSSEOverloadRetriesSameBinding(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if attempt == 1 {
			_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"recovered\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash"}},
		map[string]providers.Client{providers.ProviderAIAND: openaicompat.NewClient("test-key", upstream.URL)},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithRetrySleep(noRetrySleep)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
	mu.Lock()
	assert.Equal(t, 2, calls, "the first HTTP 200 overload is retried on the sole Anthropic binding")
	mu.Unlock()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "recovered")
	assert.NotContains(t, rec.Body.String(), "overloaded_error", "the failed attempt must never commit bytes")
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
		Provider:      providers.ProviderAIAND,
		Model:         "deepseek/deepseek-v4-pro",
		Reason:        "fresh",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-v4-pro", Reason: "fresh"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-fw-key", ossUpstream.URL),
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithPlannerEnabled(false).
		WithRetrySleep(noRetrySleep)

	ctx := authedCtx(uuid.New().String())
	// "model" resolves baselineFor to claude-haiku-4-5 (a known Anthropic
	// catalog model), so a retryable OSS exhaustion rescues onto Anthropic.
	body := []byte(`{"model":"z-ai/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

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
	assert.Equal(t, "deepseek/deepseek-v4-pro", store.pin.Model, "the OSS pin's identity must be untouched")
}

// A routed model with a larger window than the requested baseline can carry a
// prompt the baseline cannot; a pre-commit exhaustion on the routed model must
// not rescue onto a baseline that Anthropic would 400 as "prompt is too long".
func TestProxyMessages_BaselineFailoverSkipsBaselineOverContextWindow(t *testing.T) {
	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
	}))
	defer openAIUpstream.Close()

	baseline := &fakeProvider{}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.5", Reason: "test"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-key", openAIUpstream.URL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(noRetrySleep)

	// ~230K estimated tokens: fits gpt-5.5 (1.05M) but not claude-haiku-4-5 (200K).
	filler := strings.Repeat("tool output line ", 230_000*4/len("tool output line "))
	body := []byte(`{"model":"z-ai/glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"` + filler + `"}]}`)

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

func (*sequencedClient) SupportsSubscriptions() bool { return true }

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

// TestProxyMessages_GeminiNon400NotRetried guards the gate: a non-400 Gemini
// error (e.g. 503) must NOT trigger the AUTO downgrade — that path is reserved
// for VALIDATED-mode schema-grammar rejections, and re-emitting would waste an
// upstream call.
func TestProxyMessages_GeminiNon400NotRetried(t *testing.T) {
	client := &sequencedClient{
		responses: []func(w http.ResponseWriter) error{
			func(http.ResponseWriter) error {
				return &providers.UpstreamStatusError{Status: http.StatusServiceUnavailable}
			},
		},
	}

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gemini-3.1-pro-preview"}},
		map[string]providers.Client{providers.ProviderAIAND: client},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"gemini-3.1-pro-preview","stream":true,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}],` +
		`"messages":[{"role":"user","content":"who are you"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Len(t, client.bodies, 1, "a 503 is not a VALIDATED-schema 400 — no AUTO retry")
}

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
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-v4-pro"}},
		map[string]providers.Client{providers.ProviderAIAND: client},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","stream":true,"max_tokens":1024,` +
		`"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":"hi"}]}`)

	_ = svc.ProxyMessages(context.Background(), body, rec, req)

	assert.Len(t, client.bodies, 1, "only a knob rejection licenses the unstructured retry")
}
