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
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"motif-technologies/motif-3","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"motif-technologies/motif-3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		_, _ = w.Write([]byte(c))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

const anthropicToollessTurn = `{"model":"motif-technologies/motif-3","stream":true,"max_tokens":1024,` +
	`"messages":[{"role":"user","content":"summarize this repo"}]}`

func directOpenAIService(t *testing.T, baseURL string, broad bool) *proxy.Service {
	t.Helper()
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "motif-technologies/motif-3"}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-key", baseURL+"/v1"),
		},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithOpenAIResponsesBroad(broad)
}

// Under the broad rollout, direct-OpenAI serves every expressible turn on
// Responses; with it off a toolless turn keeps the chat projection.
func TestProxyMessages_DirectOpenAIToollessTurnFollowsRollout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		broad     bool
		wantPaths []string
	}{
		{name: "rollout on", broad: true, wantPaths: []string{"/v1/responses"}},
		{name: "rollout off", broad: false, wantPaths: []string{"/v1/chat/completions"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				paths []string
			)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/responses") {
					writeOpenAIResponsesSSE(w)
					return
				}
				writeOpenAIChatSSE(w)
			}))
			defer upstream.Close()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, directOpenAIService(t, upstream.URL, tc.broad).
				ProxyMessages(context.Background(), []byte(anthropicToollessTurn), rec, req))

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.wantPaths, paths)
			assert.Contains(t, rec.Body.String(), "event: message_start")
		})
	}
}

// TestProxyMessages_DirectOpenAIStopSequencesStayOnChat was deleted with the
// AIand-only cut: the chat projection is only chosen when the target lacks
// CapReasoning (RequiresChatCompletionsParams short-circuits otherwise), and
// every roster model is an always-on reasoning model. With no non-reasoning
// model left, stop_sequences can no longer keep an OpenAI-bound turn off the
// Responses endpoint.
