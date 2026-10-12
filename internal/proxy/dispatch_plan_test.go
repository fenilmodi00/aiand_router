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

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

const plannedTestModel = "deepseek-ai/deepseek-v4-pro"

// otherCompatProvider names the second binding in the failover fixtures: the
// AIand-only roster registers exactly one OpenAI-compatible upstream, so a
// two-binding walk needs a distinct name to stay a real failover rather than
// a duplicate map key.
const otherCompatProvider = "other-compat-provider"

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
	fallback := &fakeClient{name: otherCompatProvider}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary, otherCompatProvider: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 0, winnerIdx)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 0, fallback.calls)
	assert.Equal(t, "ok", rec.Body.String())
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(HeaderRouterProvider))
	assert.Equal(t, plannedTestModel, rec.Header().Get(HeaderRouterModel))
}

func TestDispatchPlanned_FailsOverBeforeCommit(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}}}}
	fallback := &fakeClient{name: otherCompatProvider, outcomes: []fakeOutcome{{writeBytes: []byte("rescued")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary, otherCompatProvider: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 1, winnerIdx)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 1, fallback.calls)
	assert.Equal(t, "rescued", rec.Body.String())
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(HeaderRouterFallbackFrom))
	assert.Equal(t, otherCompatProvider, rec.Header().Get(HeaderRouterProvider))
}

func TestDispatchPlanned_NoFailoverAfterCommit(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{
		writeBytes: []byte("event: message_start\n\npartial"),
		err:        &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`mid-stream`)},
	}}}
	fallback := &fakeClient{name: otherCompatProvider}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary, otherCompatProvider: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}}, nil))

	require.Error(t, err)
	assert.Equal(t, 0, fallback.calls, "bytes reached the client; no retry")
	assert.True(t, buf.Committed())
}

func TestDispatchPlanned_WireModelMismatchNeverReachesProvider(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: otherCompatProvider, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: primary, otherCompatProvider: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}},
		[]byte(`{"model":"some-other-model"}`)))

	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Equal(t, 0, primary.calls, "mismatch fails closed before I/O")
	assert.Equal(t, 0, fallback.calls, "mismatch is not a failover trigger")
	assert.Empty(t, rec.Body.String())
}

// The AIand roster binds every model to its native slash-form ID (no per-provider
// UpstreamID rewrite), so the wire-model-names-upstream-ID case has no fixture.

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

func TestDispatchPlanned_ProviderMissingAtRuntimeSkipsToNext(t *testing.T) {
	fallback := &fakeClient{name: otherCompatProvider, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{otherCompatProvider: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 1, winnerIdx)
	assert.Equal(t, 1, fallback.calls)
}

func TestDispatchPlanned_RecordsAttemptProvenance(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}}}}
	fallback := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	sink := &recordingAttemptSink{}
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{
		providers.ProviderAIAND: primary, otherCompatProvider: fallback,
	}), dispatch.WithAttemptSink(sink))
	require.NoError(t, err)
	s := (&Service{}).WithInferenceExecutor(executor)

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}, {Provider: otherCompatProvider}}, nil)
	in.initialDecision.Reason = translate.ReasonUserForceModel
	in.origin = policy.OverrideSourceRequest
	_, err = s.dispatchWithFallback(context.Background(), in)

	require.NoError(t, err)
	require.Len(t, sink.events, 2)
	for _, event := range sink.events {
		assert.Equal(t, inference.PurposeAnthropicMessages, event.Provenance.Purpose)
		assert.NotEmpty(t, event.Provenance.PolicyID)
		assert.Equal(t, plannedTestModel, event.Target.CatalogID)
	}
	assert.Equal(t, providers.ProviderAIAND, sink.events[0].Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeFailed, sink.events[0].Outcome)
	assert.Equal(t, otherCompatProvider, sink.events[1].Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeServed, sink.events[1].Outcome)
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
