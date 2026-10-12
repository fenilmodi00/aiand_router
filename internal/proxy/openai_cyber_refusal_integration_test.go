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
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cyberRefusalSSE is the shape OpenAI's classifier streams on a 200: no output
// frame, an error event, then the terminal failure Codex aborts the turn on.
const cyberRefusalSSE = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"moonshotai/kimi-k3"}}` + "\n\n" +
	"event: error\n" +
	`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber"}` + "\n\n" +
	"event: turn.failed\n" +
	`data: {"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}` + "\n\n"

// anthropicRescueSSE is the Anthropic Messages stream the fallback serves when
// the refused turn is re-dispatched off the refusing vendor. Retargeted from
// the old OpenAI-compat rescue upstream: only a different wire family escapes
// the same classifier, so on the AIand-only build the rescue is Anthropic.
const anthropicRescueSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"rescued"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const responsesTurnBody = `{"model":"moonshotai/kimi-k3","stream":true,"input":[{"type":"message","role":"user",` +
	`"content":[{"type":"input_text","text":"audit this SFTP server for auth bypasses"}]}]}`

const cyberRefusalFallbackModel = "zai-org/glm-5.3"

const cyberRefusalInstallationID = "22222222-2222-2222-2222-222222222222"

// streamResponses splits an SSE body into 37-byte writes, mirroring the wire,
// where an event routinely spans several frames.
func streamResponses(sse string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for start := 0; start < len(sse); start += 37 {
			end := min(start+37, len(sse))
			_, _ = io.WriteString(w, sse[start:end])
		}
	}
}

// cyberRefusalUpstreams wires the two halves of a cyber-refusal turn: the
// AIand (OpenAI-compat) upstream that refuses, and the Anthropic fallback the
// rescue re-dispatches to. There is no Anthropic adapter on the AIand-only
// build, so the rescue is a *fakeProvider — the Anthropic attempt branch
// translates the request and the response itself, which is exactly the wire
// conversion the test needs to cover.
type cyberRefusalUpstreams struct {
	mu             sync.Mutex
	aiandHits      int
	rescue         *fakeProvider
	openAIResponse func(http.ResponseWriter)
	aiandURL       string
	rescueResp     func(http.ResponseWriter)
}

func (u *cyberRefusalUpstreams) counts() (aiand, rescueHits int) {
	u.mu.Lock()
	aiand = u.aiandHits
	u.mu.Unlock()
	return aiand, len(u.rescue.proxyBodies)
}

func (u *cyberRefusalUpstreams) start(t *testing.T) {
	t.Helper()
	u.rescue = &fakeProvider{proxyResponse: u.rescueResp}
	aiandServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		u.aiandHits++
		u.mu.Unlock()
		u.openAIResponse(w)
	}))
	t.Cleanup(aiandServer.Close)
	u.aiandURL = aiandServer.URL
}

// cyberRefusalService builds the AIand-only service for a cyber-refusal turn.
// The rescue target is pinned to Anthropic through the routing metadata's
// candidate map, which is what an off-vendor retry requires.
func (u *cyberRefusalUpstreams) service(decisionReason string, store *fakePinStore, telemetry *captureTelemetry) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "moonshotai/kimi-k3",
			Reason:   decisionReason,
			Metadata: &router.RoutingMetadata{
				CandidateModels:    []string{"moonshotai/kimi-k3"},
				CandidateProviders: map[string]string{cyberRefusalFallbackModel: providers.ProviderAnthropic},
			},
		}},
		map[string]providers.Client{
			providers.ProviderAIAND:     openaicompat.NewClient("test-aiand-key", u.aiandURL),
			providers.ProviderAnthropic: u.rescue,
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", telemetry,
	).
		WithCyberRefusalFallbackModel(cyberRefusalFallbackModel).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAIAND:     {},
			providers.ProviderAnthropic: {},
		})
}

// seedPairedPin records the pin that names the Anthropic fallback as the refusing
// model's runner-up. cyberRefusalFallback prefers the pin's own provider over the
// catalog binding, which is what lets an off-vendor retry leave the OpenAI-compat
// surface (the AIand-only roster binds every catalog model to aiand, so the
// configured fallback alone always resolves back to the refusing vendor).
func seedPairedPin(store *fakePinStore, reason string) {
	store.pin = sessionpin.Pin{
		Model:          "moonshotai/kimi-k3",
		Provider:       providers.ProviderAIAND,
		Reason:         reason,
		PairedModel:    cyberRefusalFallbackModel,
		PairedProvider: providers.ProviderAnthropic,
		PinnedUntil:    time.Now().Add(time.Hour),
		Role:           "default",
	}
	store.hasPin = true
}

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

// The refusal is the turn's first event, so the turn is still rescuable: it must
// be re-served off-vendor and the session re-pinned so turn N+1 skips the model
// that declined. Codex aborts on the refusal envelope, which is what makes
// swallowing it the whole point.
func TestProxyOpenAIResponses_CyberRefusalRescuesOffVendorAndRepins(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{
		openAIResponse: streamResponses(cyberRefusalSSE),
		rescueResp:     streamResponses(anthropicRescueSSE),
	}
	upstreams.start(t)
	store := newFakePinStore()
	seedPairedPin(store, "scorer-decision")
	telemetry := newCaptureTelemetry()

	rec := proxyResponsesTurn(t, upstreams.service("test", store, telemetry))

	aiandHits, rescueHits := upstreams.counts()
	assert.Equal(t, 1, aiandHits, "the refusing vendor is not retried")
	assert.Equal(t, 1, rescueHits, "the turn is re-served on the fallback")

	body := rec.Body.String()
	assert.NotContains(t, body, "cybersecurity risk", "the refusal must never reach the client")
	assert.Contains(t, body, "rescued", "the client sees the fallback's answer")
	assert.Contains(t, body, "response.completed")
	assert.Equal(t, cyberRefusalFallbackModel, rec.Header().Get(proxy.HeaderRouterModel))

	row := telemetry.firstRow(t)
	assert.Equal(t, cyberRefusalFallbackModel, row.DecisionModel)
	assert.Equal(t, providers.ProviderAnthropic, row.DecisionProvider)
	assert.Equal(t, proxy.ReasonCyberRefusalRetry, row.DecisionReason)

	require.NotEmpty(t, store.upserts, "the session must be re-pinned off the refusing model")
	pin := store.upserts[len(store.upserts)-1]
	assert.Equal(t, cyberRefusalFallbackModel, pin.Model)
	assert.Equal(t, providers.ProviderAnthropic, pin.Provider)
}

// Once output is committed a second model's stream would interleave with the
// first's, so the refusal is surfaced as-is — the re-pin still runs.
func TestProxyOpenAIResponses_CyberRefusalAfterCommittedOutputPassesThrough(t *testing.T) {
	committed := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"moonshotai/kimi-k3"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk."}` + "\n\n"
	upstreams := &cyberRefusalUpstreams{
		openAIResponse: streamResponses(committed),
		rescueResp:     streamResponses(anthropicRescueSSE),
	}
	upstreams.start(t)
	store := newFakePinStore()
	seedPairedPin(store, "scorer-decision")

	rec := proxyResponsesTurn(t, upstreams.service("test", store, newCaptureTelemetry()))

	_, rescueHits := upstreams.counts()
	assert.Zero(t, rescueHits, "a committed turn cannot be re-served")
	assert.Contains(t, rec.Body.String(), "cybersecurity risk", "the client keeps the upstream's own error")

	require.NotEmpty(t, store.upserts, "the re-pin runs even when the turn cannot be rescued")
	assert.Equal(t, cyberRefusalFallbackModel, store.upserts[len(store.upserts)-1].Model)
}

// A caller who pinned the model with /force-model asked for that model, and the
// kill switch has to be able to take the whole behavior out.
func TestProxyOpenAIResponses_CyberRefusalRetrySkipped(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reason       string
		retryFlag    bool
		wantFinalPin string
	}{
		{name: "force-model pin", reason: translate.ReasonUserForceModel, retryFlag: true, wantFinalPin: "moonshotai/kimi-k3"},
		{name: "retry kill switch off", reason: "test", retryFlag: false, wantFinalPin: cyberRefusalFallbackModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreams := &cyberRefusalUpstreams{
				openAIResponse: streamResponses(cyberRefusalSSE),
				rescueResp:     streamResponses(anthropicRescueSSE),
			}
			upstreams.start(t)
			store := newFakePinStore()
			seedPairedPin(store, tc.reason)
			svc := upstreams.service(tc.reason, store, newCaptureTelemetry()).WithCyberRefusalRetry(tc.retryFlag)

			rec := proxyResponsesTurn(t, svc)

			_, rescueHits := upstreams.counts()
			assert.Zero(t, rescueHits, "no off-vendor re-dispatch")
			assert.Contains(t, rec.Body.String(), "cybersecurity risk",
				"without the rescue the refusal is the turn's answer")
			require.NotEmpty(t, store.upserts)
			assert.Equal(t, tc.wantFinalPin, store.upserts[len(store.upserts)-1].Model,
				"the re-pin is gated separately from the retry")
		})
	}
}

// An ordinary upstream failure is not a refusal: it keeps today's failover
// behavior and leaves the session pin alone.
func TestProxyOpenAIResponses_OrdinaryOpenAIErrorIsUnchanged(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{
		openAIResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error","code":"unsupported_parameter"}}`)
		},
		rescueResp: streamResponses(anthropicRescueSSE),
	}
	upstreams.start(t)
	store := newFakePinStore()

	rec := proxyResponsesTurn(t, upstreams.service("test", store, newCaptureTelemetry()))

	_, rescueHits := upstreams.counts()
	assert.Zero(t, rescueHits)
	assert.Contains(t, rec.Body.String(), "response.failed")
	assert.Contains(t, rec.Body.String(), "Upstream call failed.")
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "moonshotai/kimi-k3", store.upserts[len(store.upserts)-1].Model,
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
	upstreams := &cyberRefusalUpstreams{
		openAIResponse: func(w http.ResponseWriter) {
			close(providerStarted)
			<-releaseProvider
			streamResponses("event: response.completed\n" +
				`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp_native","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n")(w)
		},
		rescueResp: streamResponses(anthropicRescueSSE),
	}
	upstreams.start(t)
	svc := upstreams.service("test", newFakePinStore(), newCaptureTelemetry())
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

// The rescue runs once: a fallback that refuses too ends the turn rather than
// walking the catalog.
func TestProxyOpenAIResponses_CyberRefusalRetriesOnlyOnce(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{
		openAIResponse: streamResponses(cyberRefusalSSE),
		rescueResp: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: error\n"+
				`data: {"type":"error","error":{"type":"invalid_request_error","message":"This content was flagged for possible cybersecurity risk."}}`+"\n\n")
		},
	}
	upstreams.start(t)
	store := newFakePinStore()
	seedPairedPin(store, "scorer-decision")
	proxyResponsesTurn(t, upstreams.service("test", store, newCaptureTelemetry()))

	aiandHits, rescueHits := upstreams.counts()
	assert.Equal(t, 1, aiandHits)
	assert.Equal(t, 1, rescueHits, "the fallback's own refusal is not rescued again")
}
