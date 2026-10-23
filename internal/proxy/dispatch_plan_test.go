package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/inference"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/policy"
)

const plannedTestModel = "deepseek/deepseek-v4-pro"

func plannedInputs(rec *httptest.ResponseRecorder, buf *preludeBuffer, bindings []catalog.ProviderBinding, body []byte) failoverInputs {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return failoverInputs{
		w:               rec,
		buf:             buf,
		initialDecision: router.Decision{Model: plannedTestModel, Provider: bindings[0].Provider},
		bindings:        bindings,
		attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
			buf.Seal()
			return p.Proxy(ctx, d, providers.PreparedRequest{Body: body}, buf, r)
		},
		flushErr: flushBufferedIfPresent,
		purpose:  inference.PurposeAnthropicMessages,
	}
}

func TestDispatchPlanned_PrimarySucceeds(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: providers.ProviderAIAND}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: providers.ProviderAIAND}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 0, winnerIdx)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 0, fallback.calls)
	assert.Equal(t, "ok", rec.Body.String())
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(HeaderRouterProvider))
	assert.Equal(t, plannedTestModel, rec.Header().Get(HeaderRouterModel))
}

func TestDispatchPlanned_NoFailoverAfterCommit(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{
		writeBytes: []byte("event: message_start\n\npartial"),
		err:        &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`mid-stream`)},
	}}}
	fallback := &fakeClient{name: providers.ProviderAIAND}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: providers.ProviderAIAND}}, nil))

	require.Error(t, err)
	assert.Equal(t, 0, fallback.calls, "bytes reached the client; no retry")
	assert.True(t, buf.Committed())
}

func TestDispatchPlanned_WireModelMismatchNeverReachesProvider(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: providers.ProviderAIAND}},
		[]byte(`{"model":"some-other-model"}`)))

	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Equal(t, 0, primary.calls, "mismatch fails closed before I/O")
	assert.Equal(t, 0, fallback.calls, "mismatch is not a failover trigger")
	assert.Empty(t, rec.Body.String())
}

func TestDispatchPlanned_WireModelMayNameUpstreamID(t *testing.T) {
	entry, ok := catalog.ByID(plannedTestModel)
	require.True(t, ok)
	binding := entry.Providers[0]
	require.NotEmpty(t, binding.UpstreamID)
	client := &fakeClient{name: binding.Provider, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{binding.Provider: client})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: binding.Provider}}, []byte(`{"model":"`+binding.UpstreamID+`"}`)))

	require.NoError(t, err, "a primary binding without an explicit upstream id inherits the catalog's")
	assert.Equal(t, 1, client.calls)
}

func TestDispatchPlanned_SameBindingRetryUsesInjectedSleep(t *testing.T) {
	only := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`blip`)}},
		{writeBytes: []byte("ok")},
	}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: only})
	slept := 0
	s.retrySleep = func(context.Context, time.Duration) error { slept++; return nil }

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 0, winnerIdx)
	assert.Equal(t, 2, only.calls)
	assert.Equal(t, 1, slept)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestDispatchPlanned_ExhaustionFlushesUpstreamEnvelope(t *testing.T) {
	only := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 404, Body: []byte(`nope`)}}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: only})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}, nil))

	require.Error(t, err)
	assert.Equal(t, 1, only.calls, "404 must not same-binding-retry")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDispatchPlanned_DeferredFlushLeavesWriterUntouched(t *testing.T) {
	only := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 500, Body: []byte(`boom`)}}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: only})
	s.retrySleep = noopSleep

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}, nil)
	in.deferFlushOnExhaustion = true
	_, err := s.dispatchWithFallback(context.Background(), in)

	require.Error(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String(), "caller owns the deferred error")
}

func TestDispatchPlanned_UnregisteredPurposeFailsClosed(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}, nil)
	in.purpose = inference.Purpose("not_a_purpose")
	_, err := s.dispatchWithFallback(context.Background(), in)

	var resolution *policy.ResolutionError
	require.True(t, errors.As(err, &resolution))
	assert.Equal(t, policy.ResolutionErrorUnknownPurpose, resolution.Code)
	assert.Equal(t, 0, primary.calls)
}

type recordingAttemptSink struct{ events []inference.AttemptEvent }

func (r *recordingAttemptSink) RecordAttempt(_ context.Context, event inference.AttemptEvent) {
	r.events = append(r.events, event)
}
