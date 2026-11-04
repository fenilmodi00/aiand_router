package proxy

import (
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
)

// pastRequestedWindow classification, unit-tested directly: v0's catalog has
// no larger-window second model, so the served-on-another-model scenario is
// exercised here with synthetic windows rather than end to end.

// The z-ai/glm-5.3 catalog window (1,048,576) from the request's own model.
func TestPastRequestedWindowClassification(t *testing.T) {
	const requested = "z-ai/glm-5.3"
	window := contextWindowForRequest(requested)

	assert.False(t, pastRequestedWindow(requested, requested, window, 0),
		"served model equals the requested model: the client's budget is accurate")
	assert.False(t, pastRequestedWindow(requested, "other-model", window-1, 1),
		"estimate plus reserve still fits the requested window")
	assert.True(t, pastRequestedWindow(requested, "other-model", window, 1),
		"estimate past the requested window must classify")
	assert.False(t, pastRequestedWindow(requested, "other-model", 0, 0),
		"no estimate means no classification")
	assert.False(t, pastRequestedWindow("z-ai/unknown-model", "other-model", 1<<30, 0),
		"unknown requested model: no window to compare against")
}

func TestAsRequestedWindowOverflow(t *testing.T) {
	retryable := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}
	rateLimited := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}
	overflow := &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":{"message":"prompt is too long"}}`)}

	// Past the window, pre-output, retryable: rewrite to the client's native
	// prompt-too-long so it compacts instead of resending the same history.
	err := asRequestedWindowOverflow(retryable, true, false)
	assert.ErrorIs(t, err, policy.ErrContextWindowExceeded)

	// Already an overflow, a rate limit, or a committed stream: pass through.
	assert.Equal(t, overflow, asRequestedWindowOverflow(overflow, true, false))
	assert.Equal(t, rateLimited, asRequestedWindowOverflow(rateLimited, true, false))
	assert.Equal(t, retryable, asRequestedWindowOverflow(retryable, true, true), "committed stream")
	assert.Equal(t, retryable, asRequestedWindowOverflow(retryable, false, false), "within window")
	assert.NoError(t, asRequestedWindowOverflow(nil, true, false))
}
