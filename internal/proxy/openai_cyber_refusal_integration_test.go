package proxy_test

import (
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cyberRefusalSSE is the shape OpenAI's classifier streams on a 200: no output
// frame, an error event, then the terminal failure Codex aborts the turn on.
const cyberRefusalSSE = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol"}}` + "\n\n" +
	"event: error\n" +
	`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber"}` + "\n\n" +
	"event: turn.failed\n" +
	`data: {"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}` + "\n\n"

const anthropicRescueSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"deepseek/deepseek-v4-pro","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"rescued"}}` + "\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const responsesTurnBody = `{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user",` +
	`"content":[{"type":"input_text","text":"audit this SFTP server for auth bypasses"}]}]}`

// cyberRefusalUpstreams serves the OpenAI refusal and the Anthropic rescue,
// counting hits on each so a test can tell a rescue from a passthrough.
type cyberRefusalUpstreams struct {
	mu             sync.Mutex
	openAIHits     int
	anthropicHits  int
	openAIResponse func(http.ResponseWriter)
	anthropicSSE   string
}

func (u *cyberRefusalUpstreams) counts() (openAI, anthropic int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.openAIHits, u.anthropicHits
}

func (u *cyberRefusalUpstreams) start(t *testing.T) (openAIURL, anthropicURL string) {
	t.Helper()
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		u.openAIHits++
		u.mu.Unlock()
		u.openAIResponse(w)
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		u.anthropicHits++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		response := u.anthropicSSE
		if response == "" {
			response = anthropicRescueSSE
		}
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(anthropicServer.Close)

	return openAIServer.URL, anthropicServer.URL
}

func streamResponses(sse string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Frames are split mid-event, as they arrive on the wire.
		for start := 0; start < len(sse); start += 37 {
			end := min(start+37, len(sse))
			_, _ = io.WriteString(w, sse[start:end])
		}
	}
}

func cyberRefusalService(
	openAIURL, anthropicURL, decisionReason string,
	store *fakePinStore,
	telemetry *captureTelemetry,
) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "gpt-5.6-sol",
			Reason:   decisionReason,
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"gpt-5.6-sol"}},
		}},
		map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient("test-openai-key", openAIURL),
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", telemetry,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	})
}

const cyberRefusalInstallationID = "22222222-2222-2222-2222-222222222222"

func proxyResponsesTurn(t *testing.T, svc *proxy.Service) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	ctx := context.WithValue(
		authedCtx(cyberRefusalInstallationID),
		proxy.ClientIdentityContextKey{},
		proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex},
	)
	_ = svc.ProxyOpenAIResponses(ctx, []byte(responsesTurnBody), rec, req)
	return rec
}

// An ordinary upstream failure is not a refusal: it keeps today's failover
// behavior and leaves the session pin alone.
func TestProxyOpenAIResponses_OrdinaryOpenAIErrorIsUnchanged(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error","code":"unsupported_parameter"}}`)
	}}
	openAIURL, anthropicURL := upstreams.start(t)
	store := newFakePinStore()

	rec := proxyResponsesTurn(t, cyberRefusalService(openAIURL, anthropicURL, "test", store, newCaptureTelemetry()))

	_, anthropicHits := upstreams.counts()
	assert.Zero(t, anthropicHits)
	assert.Contains(t, rec.Body.String(), "response.failed")
	assert.Contains(t, rec.Body.String(), "Upstream call failed.")
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "gpt-5.6-sol", store.upserts[len(store.upserts)-1].Model,
		"an ordinary error is no reason to move the session")
}

func TestProxyOpenAIResponses_RoutingBadgeFlushesBeforeDelayedProvider(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	defer func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	}()
	upstreams := &cyberRefusalUpstreams{openAIResponse: func(w http.ResponseWriter) {
		close(providerStarted)
		<-releaseProvider
		streamResponses("event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_native","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n")(w)
	}}
	openAIURL, anthropicURL := upstreams.start(t)
	svc := cyberRefusalService(openAIURL, anthropicURL, "test", newFakePinStore(), newCaptureTelemetry())
	writer := newObservedResponseWriter()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	ctx := context.WithValue(
		authedCtx(cyberRefusalInstallationID),
		proxy.ClientIdentityContextKey{},
		proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex},
	)
	done := make(chan error, 1)
	go func() {
		done <- svc.ProxyOpenAIResponses(ctx, []byte(responsesTurnBody), writer, req)
	}()

	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider was not dispatched")
	}
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("routing badge was not flushed before provider completion")
	}
	assert.Contains(t, writer.BodyString(), "✦ **Weave Router**")

	close(releaseProvider)
	require.NoError(t, <-done)
}
