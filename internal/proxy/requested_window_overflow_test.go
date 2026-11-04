package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from upstream 8f426ad1 (#1787) + b09e5f8d (#1788), with v0's aiand
// catalog. The past-window classification itself is unit-tested in
// requested_window_overflow_internal_test.go: v0's catalog has no
// larger-window second model, so an end-to-end served-on-another-model
// scenario cannot be built without synthetic catalog entries.

const requestedWindowModel = "z-ai/glm-5.3"

// pastRequestedWindowPrompt estimates past the requested model's window.
var pastRequestedWindowPrompt = strings.Repeat("overflow ", 500_000)


func proxyRequestedWindowOverflow(t *testing.T, prompt string, stream bool, upstreamErr error) (*httptest.ResponseRecorder, error, *fakeProvider) {
	t.Helper()
	upstream := &fakeProvider{proxyErr: upstreamErr}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: requestedWindowModel, Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAIAND: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAIAND, requestedWindowModel, nil,
	).WithAvailableModels(map[string]struct{}{requestedWindowModel: {}})
	streamField := "false"
	if stream {
		streamField = "true"
	}
	body := []byte(`{"model":"` + requestedWindowModel + `","max_tokens":1024,"stream":` + streamField + `,"messages":[{"role":"user","content":"` + prompt + `"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(authedCtx(overflowInstallationID), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	return rec, err, upstream
}

func requireDispatchKind(t *testing.T, err error) proxy.DispatchErrorKind {
	t.Helper()
	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok, "unclassified: %v", err)
	return cls.Kind
}

// Rate limits are a throttle, not a size signal: compaction would not fix them.
func TestProxyMessages_RateLimitPastRequestedWindowIsNotOverflow(t *testing.T) {
	_, err, _ := proxyRequestedWindowOverflow(t, pastRequestedWindowPrompt, false, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests})

	assert.NotEqual(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err))
}

// While the history still fits the requested model, the client's own budget is
// accurate and a 5xx is transient; compacting would discard context for nothing.
func TestProxyMessages_PreOutputFailureWithinRequestedWindowIsNotOverflow(t *testing.T) {
	_, err, _ := proxyRequestedWindowOverflow(t, "hi", false, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway})

	assert.NotEqual(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err))
}

// The client's own compaction turn is its recovery from overflow: answering it
// with prompt-too-long fails the compaction ("Automatic compaction failed").
func TestProxyMessages_CompactionTurnPastRequestedWindowIsNotOverflow(t *testing.T) {
	upstream := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: requestedWindowModel, Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAIAND: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAIAND, requestedWindowModel, nil,
	).WithAvailableModels(map[string]struct{}{requestedWindowModel: {}})
	body := []byte(`{"model":"` + requestedWindowModel + `","max_tokens":1024,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"` + pastRequestedWindowPrompt + `"}]}`)

	err := svc.ProxyMessages(authedCtx(overflowInstallationID), body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.NotEmpty(t, upstream.proxyBodies, "the compaction turn reaches the served model")
	assert.NotEqual(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err))
}
