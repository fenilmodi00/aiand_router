package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	feedbacktoken "aiand-os/router/internal/feedback"
	"aiand-os/router/internal/observability/otel"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/telemetry"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingTelemetryEmitter struct {
	recorder *capturingTelemetryRecorder
}

func (e *capturingTelemetryEmitter) NewRecorder() telemetry.Recorder {
	return e.recorder
}

func (e *capturingTelemetryEmitter) NewUsageExtractor(inner http.ResponseWriter, provider string) (*telemetry.UsageExtractor, http.ResponseWriter) {
	wrapper := otel.NewUsageExtractor(inner, provider)
	return wrapper.Extractor(), wrapper
}

type capturingTelemetryRecorder struct {
	spans            []telemetry.Span
	logs             []telemetry.LogRecord
	flushCount       int
	spansBeforeFlush []bool
}

func (r *capturingTelemetryRecorder) Record(span telemetry.Span) {
	r.spans = append(r.spans, span)
	r.spansBeforeFlush = append(r.spansBeforeFlush, r.flushCount == 0)
}

func (r *capturingTelemetryRecorder) RecordLog(record telemetry.LogRecord) {
	r.logs = append(r.logs, record)
}

func (r *capturingTelemetryRecorder) Flush() {
	r.flushCount++
}

func TestTelemetryPort_LinkFeedbackRecordsAndFlushes(t *testing.T) {
	recorder := &capturingTelemetryRecorder{}
	emitter := &capturingTelemetryEmitter{recorder: recorder}
	comment := "The response was helpful."
	service := proxy.NewService(nil, nil, emitter, false, nil, nil, false, "", "", nil).
		WithFeedback(&fakeFeedbackRepoForSpanTest{}, feedbacktoken.NewSigner("secret", time.Hour), "https://router.example.com")

	err := service.SubmitFeedback(context.Background(), proxy.SubmitFeedbackParams{
		InstallationID: "inst-1",
		ExternalID:     "org-1",
		RequestID:      "req-1",
		RouterUserID:   "user-1",
		Rating:         "down",
		Comment:        &comment,
	})

	require.NoError(t, err)
	require.Len(t, recorder.spans, 1)
	assert.Equal(t, "router.feedback", recorder.spans[0].Name)
	require.Len(t, recorder.spansBeforeFlush, 1)
	assert.True(t, recorder.spansBeforeFlush[0])
	assert.Equal(t, 1, recorder.flushCount)
	attrs := telemetrySpanStrings(recorder.spans[0])
	assert.Equal(t, map[string]string{
		"external_id":      "org-1",
		"request_id":       "req-1",
		"feedback.rating":  "down",
		"feedback.source":  "link",
		"feedback.comment": comment,
		"router_user_id":   "user-1",
	}, attrs)
}

func TestTelemetryPort_CommandFeedbackRecordsAndFlushes(t *testing.T) {
	recorder := &capturingTelemetryRecorder{}
	emitter := &capturingTelemetryEmitter{recorder: recorder}
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		Model:           "claude-haiku-4-5",
		LastServedModel: "claude-haiku-4-5",
	}
	feedbackStore := &fakeFeedbackStore{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3", Reason: "cluster"}}
	service := proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAIAND: &fakeProvider{}},
		emitter,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"claude-haiku-4-5",
		nil,
	).WithRouterFeedbackStore(feedbackStore)
	const body = `{
		"model":"z-ai/glm-5.3",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"/router-feedback got stuck on Haiku for too long"}]
	}`
	ctx := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "key-1")
	ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, uuid.New().String())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := service.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), req)

	require.NoError(t, err)
	require.Len(t, recorder.spans, 1)
	assert.Equal(t, "router.feedback.command", recorder.spans[0].Name)
	require.Len(t, recorder.spansBeforeFlush, 1)
	assert.True(t, recorder.spansBeforeFlush[0])
	assert.Equal(t, 1, recorder.flushCount)
	attrs := telemetrySpanStrings(recorder.spans[0])
	assert.Equal(t, "claude-haiku-4-5", attrs["feedback.served_model"])
	assert.Equal(t, "z-ai/glm-5.3", attrs["requested.model"])
	assert.Equal(t, "got stuck on Haiku for too long", attrs["feedback.text"])
	assert.Equal(t, proxy.RouterFeedbackSourceUser, attrs["feedback.source"])
	assert.Contains(t, attrs, "client.app")
}

func telemetrySpanStrings(span telemetry.Span) map[string]string {
	values := make(map[string]string, len(span.Attrs))
	for _, attr := range span.Attrs {
		if attr.Value.Kind() == "string" {
			values[attr.Key] = attr.Value.GetStringValue()
		}
	}
	return values
}
