package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/policy"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureTelemetry records every InsertTelemetryParams the proxy fires.
// notify channel is the rendezvous for the async goroutine.
type captureTelemetry struct {
	mu           sync.Mutex
	rows         []proxy.InsertTelemetryParams
	shadowRows   []proxy.PolicyShadowDecision
	notify       chan struct{}
	shadowNotify chan struct{}
	// seqResult and seqErr configure GetTelemetryBySessionSequence; seqCalls
	// records the sequence arguments the code under test resolved against.
	seqResult proxy.TelemetryTurnResult
	seqErr    error
	seqCalls  []int
}

func newCaptureTelemetry() *captureTelemetry {
	return &captureTelemetry{
		notify:       make(chan struct{}, 4),
		shadowNotify: make(chan struct{}, 4),
	}
}

func (c *captureTelemetry) InsertRequestTelemetry(_ context.Context, p proxy.InsertTelemetryParams) error {
	c.mu.Lock()
	c.rows = append(c.rows, p)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

func (c *captureTelemetry) InsertPolicyShadowDecision(_ context.Context, event proxy.PolicyShadowDecision) error {
	c.mu.Lock()
	c.shadowRows = append(c.shadowRows, event)
	c.mu.Unlock()
	select {
	case c.shadowNotify <- struct{}{}:
	default:
	}
	return nil
}

func (c *captureTelemetry) GetTelemetrySummary(context.Context, string, time.Time, time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}

func (c *captureTelemetry) GetTelemetryTimeseries(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}

func (c *captureTelemetry) GetTelemetrySummaryAll(context.Context, time.Time, time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}

func (c *captureTelemetry) GetTelemetryTimeseriesAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}

func (c *captureTelemetry) GetTelemetryRows(context.Context, string, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}

func (c *captureTelemetry) GetTelemetryRowsAll(context.Context, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}

func (c *captureTelemetry) GetTelemetryModelBreakdown(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}

func (c *captureTelemetry) GetTelemetryModelBreakdownAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}

func (c *captureTelemetry) GetSessionCost(context.Context, string, string) (proxy.SessionCost, error) {
	return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
}

func (c *captureTelemetry) GetTelemetryBySessionSequence(_ context.Context, _ uuid.UUID, _ []byte, _ string, seq int) (proxy.TelemetryTurnResult, error) {
	c.mu.Lock()
	c.seqCalls = append(c.seqCalls, seq)
	res := c.seqResult
	err := c.seqErr
	c.mu.Unlock()
	return res, err
}

func (c *captureTelemetry) firstRow(t *testing.T) proxy.InsertTelemetryParams {
	t.Helper()
	select {
	case <-c.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("expected an async telemetry insert within 2s; none observed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.rows)
	return c.rows[0]
}

func (c *captureTelemetry) firstShadowRow(t *testing.T) proxy.PolicyShadowDecision {
	t.Helper()
	select {
	case <-c.shadowNotify:
	case <-time.After(4 * time.Second):
		t.Fatal("expected an async policy shadow insert within 4s; none observed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.shadowRows)
	return c.shadowRows[0]
}

type shadowRequestRouter struct {
	decision router.Decision
	requests chan router.Request
}

func (r *shadowRequestRouter) Route(_ context.Context, request router.Request) (router.Decision, error) {
	r.requests <- request
	return r.decision, nil
}

func TestPolicyShadowComparisonCollectsUsageBypassRoute(t *testing.T) {
	const installID = "77777777-7777-7777-7777-777777777777"
	shadowStrategy := router.Strategy("future-policy")
	shadowRouter := &shadowRequestRouter{
		decision: router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "gpt-5.5",
		},
		requests: make(chan router.Request, 1),
	}
	telem := newCaptureTelemetry()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: bypassScorerPickMdl}},
		map[string]providers.Client{providers.ProviderAIAND: &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
			// The one wired upstream speaks chat/completions, so the
			// non-streaming turn needs a body in that wire shape.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}}},
		nil, false, nil, nil, false,
		providers.ProviderAIAND, bypassScorerPickMdl, telem,
	).
		WithPolicyStrategy(policy.StrategySpec{Strategy: shadowStrategy, Router: shadowRouter})

	ctx := bypassCtx()
	ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, installID)
	ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "org-bypass")
	ctx = context.WithValue(ctx, proxy.PolicyTrainingAllowedContextKey{}, true)
	ctx = context.WithValue(ctx, proxy.PolicyShadowStrategyContextKey{}, shadowStrategy)
	recorder, request, body := bypassRequest(t)

	require.NoError(t, svc.ProxyMessages(ctx, body, recorder, request))

	row := telem.firstShadowRow(t)
	assert.Equal(t, installID, row.InstallationID)
	assert.Equal(t, bypassScorerPickMdl, row.ServingModel, "the shadow compares against the model that served")
	assert.Equal(t, "gpt-5.5", row.ShadowModel)
	assert.False(t, row.ModelsAgree)
}

// TestNormalizeRolloutID covers the bound: oversized ids are rejected (not
// truncated) so a stored id always joins back to a real rollout.
func TestNormalizeRolloutID(t *testing.T) {
	assert.Equal(t, "run/cond/0/iid", proxy.NormalizeRolloutID("  run/cond/0/iid "))
	assert.Equal(t, "", proxy.NormalizeRolloutID(strings.Repeat("x", 257)))
	assert.Equal(t, strings.Repeat("x", 256), proxy.NormalizeRolloutID(strings.Repeat("x", 256)))
}

// TestProxyOpenAIChatCompletion_NativeChatResponseSignals covers the
// chat/completions passthrough, where no translator runs and the usage
// extractor's parse of the upstream stream is the only account of the turn.
func TestProxyOpenAIChatCompletion_NativeChatResponseSignals(t *testing.T) {
	const installID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	toolTurn := []string{
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"main.go\"}"}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
	}

	tests := []struct {
		name              string
		enabled           bool
		frames            []string
		wantFinishReason  *string
		wantToolUseBlocks *int32
	}{
		{
			name:              "observed tool turn",
			enabled:           true,
			frames:            toolTurn,
			wantFinishReason:  ptrTo("tool_calls"),
			wantToolUseBlocks: ptrTo(int32(1)),
		},
		{
			name:              "observed text turn is a measured zero",
			enabled:           true,
			frames:            []string{`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`},
			wantFinishReason:  ptrTo("stop"),
			wantToolUseBlocks: ptrTo(int32(0)),
		},
		{
			name:    "stream cut before finish_reason stays unknown",
			enabled: true,
			frames:  toolTurn[:3],
		},
		{
			name:    "flag off keeps the row as it is today",
			enabled: false,
			frames:  toolTurn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				for _, frame := range tt.frames {
					_, _ = w.Write([]byte("data: " + frame + "\n\n"))
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}}
			telem := newCaptureTelemetry()
			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4.1"}},
				map[string]providers.Client{providers.ProviderAIAND: provider},
				nil, false, nil, nil, false,
				providers.ProviderAIAND, "gpt-4.1", telem,
			).WithNativeOpenAIResponseSignals(tt.enabled)

			ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installID)
			rec := httptest.NewRecorder()
			// A stop sequence has no Responses equivalent, so the turn stays on
			// chat/completions and is served without a translator.
			body := []byte(`{"model":"auto","stream":true,"stop":["END"],"messages":[{"role":"user","content":"read main.go"}]}`)
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
			_ = svc.ProxyOpenAIChatCompletion(ctx, body, rec, httpReq)

			require.Len(t, provider.proxyEndpoints, 1)
			require.Equal(t, providers.EndpointChatCompletions, provider.proxyEndpoints[0])

			row := telem.firstRow(t)
			assert.Equal(t, tt.wantFinishReason, row.StopReason)
			assert.Equal(t, tt.wantFinishReason, row.UpstreamFinishReason)
			assert.Equal(t, tt.wantToolUseBlocks, row.ToolUseBlocks)
			assert.Nil(t, row.InvalidToolArgsBlocks)
		})
	}
}

// TestProxyOpenAIResponses_NativeResponseSignals covers the /v1/responses
// passthrough, whose terminal event is the only statement of how the turn
// ended.
func TestProxyOpenAIResponses_NativeResponseSignals(t *testing.T) {
	const installID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	toolTurn := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"},{"type":"function_call","call_id":"call_2","name":"grep","arguments":"{}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
	}

	tests := []struct {
		name              string
		enabled           bool
		frames            []string
		wantFinishReason  *string
		wantStopReason    *string
		wantToolUseBlocks *int32
	}{
		{
			name:              "observed tool turn",
			enabled:           true,
			frames:            toolTurn,
			wantFinishReason:  ptrTo("tool_calls"),
			wantStopReason:    ptrTo("tool_calls"),
			wantToolUseBlocks: ptrTo(int32(2)),
		},
		{
			name:              "observed text turn is a measured zero",
			enabled:           true,
			frames:            []string{`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`},
			wantFinishReason:  ptrTo("stop"),
			wantStopReason:    ptrTo("stop"),
			wantToolUseBlocks: ptrTo(int32(0)),
		},
		{
			name:    "stream cut before the terminal event stays unknown",
			enabled: true,
			frames:  toolTurn[:1],
		},
		{
			// The upstream finish reason predates this flag and is unaffected.
			name:             "flag off keeps the row as it is today",
			enabled:          false,
			frames:           toolTurn,
			wantFinishReason: ptrTo("tool_calls"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				for _, frame := range tt.frames {
					_, _ = w.Write([]byte("data: " + frame + "\n\n"))
				}
			}}
			telem := newCaptureTelemetry()
			svc := proxy.NewService(
				&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.6-luna"}},
				map[string]providers.Client{providers.ProviderAIAND: provider},
				nil, false, nil, nil, false,
				providers.ProviderAIAND, "gpt-5.6-sol", telem,
			).WithNativeOpenAIResponseSignals(tt.enabled)

			ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, installID)
			rec := httptest.NewRecorder()
			body := []byte(`{"model":"auto","stream":true,"input":"read main.go","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`)
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
			_ = svc.ProxyOpenAIResponses(ctx, body, rec, httpReq)

			require.Len(t, provider.proxyEndpoints, 1)
			require.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])

			row := telem.firstRow(t)
			assert.Equal(t, tt.wantFinishReason, row.UpstreamFinishReason)
			assert.Equal(t, tt.wantStopReason, row.StopReason)
			assert.Equal(t, tt.wantToolUseBlocks, row.ToolUseBlocks)
			assert.Nil(t, row.InvalidToolArgsBlocks)
		})
	}
}

func ptrTo[T any](v T) *T { return &v }
