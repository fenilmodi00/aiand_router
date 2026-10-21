package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeOpenAIResponsesSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, c := range []string{
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n",
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n",
	} {
		_, _ = w.Write([]byte(c))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func writeOpenAIChatSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunks := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5.4-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5.4-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		_, _ = w.Write([]byte(c))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

const gatewayToolTurn = `{"model":"gpt-5.4-mini","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"list files"}],` +
	`"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`

func gatewayService(t *testing.T, baseURL string) *proxy.Service {
	t.Helper()
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.4-mini"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewGatewayClient("test-key", baseURL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})
}

// An upstream that mounts the Responses surface serves a direct Responses
// ingress on it: the caller's own bytes go upstream unchanged apart from the
// routed model.
func TestProxyOpenAIResponses_GatewayServesTheResponsesSurface(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		writeOpenAIResponsesSSE(w)
	}))
	defer gateway.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.4-mini"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewGatewayClient("test-key", gateway.URL+"/v1"),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	body := []byte(`{"model":"gpt-5.4-mini","input":"list files"}`)
	require.NoError(t, svc.ProxyOpenAIResponses(context.Background(), body, rec, req))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/v1/responses"}, paths)
}

// A Responses request the upstream genuinely rejected is the client's error,
// not a signal to retry on chat/completions.
func TestProxyOpenAIResponses_GatewayRejectionIsNotRetried(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid request parameters: unknown tool type"}`))
	}))
	defer gateway.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.4-mini"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewGatewayClient("test-key", gateway.URL+"/v1"),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	body := []byte(`{"model":"gpt-5.4-mini","input":"list files"}`)
	_ = svc.ProxyOpenAIResponses(context.Background(), body, rec, req)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/v1/responses"}, paths)
	assert.Contains(t, rec.Body.String(), "unknown tool type")
}

const anthropicToollessTurn = `{"model":"gpt-5.4-mini","stream":true,"max_tokens":1024,` +
	`"messages":[{"role":"user","content":"summarize this repo"}]}`

func directOpenAIService(t *testing.T, baseURL string, broad bool) *proxy.Service {
	t.Helper()
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.4-mini"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-key", baseURL),
		},
		nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithOpenAIResponsesBroad(broad)
}
