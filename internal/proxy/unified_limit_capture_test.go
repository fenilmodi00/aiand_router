package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnifiedLimitHeadersFrom_NoCaptureInstalled(t *testing.T) {
	// No capture holder installed → must return nil, not panic.
	assert.Nil(t, UnifiedLimitHeadersFrom(context.Background()))
}

func TestCaptureUnifiedLimitHeaders_RecordsOnSubscriptionCredential(t *testing.T) {
	ctx := withUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("sk-ant-oat01-live"), Source: credSourceSubscription, OAuth: true,
	})

	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	resp.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	resp.Set("content-type", "application/json") // must NOT leak into the captured set

	captureUnifiedLimitHeaders(ctx, resp)

	got := UnifiedLimitHeadersFrom(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
	assert.Equal(t, "rejected", got["anthropic-ratelimit-unified-overage-status"])
	if _, ok := got["content-type"]; ok {
		t.Errorf("non-unified header leaked into capture: %v", got)
	}
}

func TestCaptureUnifiedLimitHeaders_SkipsNonOAuthCredential(t *testing.T) {
	// BYOK/deployment-key call must not be recorded — headers describe a different account's quota.
	ctx := withUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("sk-ant-api-deployment"), Source: credSourceBYOK, OAuth: false,
	})

	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	captureUnifiedLimitHeaders(ctx, resp)

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx), "non-OAuth credential must not be captured")
}

func TestCaptureUnifiedLimitHeaders_NoCredentialsResolved(t *testing.T) {
	ctx := withUnifiedLimitCapture(context.Background())
	resp := http.Header{}
	resp.Set("anthropic-ratelimit-unified-status", "allowed")
	captureUnifiedLimitHeaders(ctx, resp) // no CredentialsContextKey set at all

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx))
}

func TestCaptureUnifiedLimitHeaders_NoUnifiedHeadersPresent(t *testing.T) {
	// Subscription response with no unified headers must leave capture nil, not empty-but-non-nil.
	ctx := withUnifiedLimitCapture(context.Background())
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("sk-ant-oat01-live"), Source: credSourceSubscription, OAuth: true,
	})
	captureUnifiedLimitHeaders(ctx, http.Header{"Content-Type": {"application/json"}})

	assert.Nil(t, UnifiedLimitHeadersFrom(ctx))
}

func TestUnifiedLimitHeadersJSON(t *testing.T) {
	t.Run("nothing captured -> nil (NULL column)", func(t *testing.T) {
		ctx := withUnifiedLimitCapture(context.Background())
		assert.Nil(t, unifiedLimitHeadersJSON(ctx))
	})

	t.Run("captured set round-trips through JSON", func(t *testing.T) {
		ctx := withUnifiedLimitCapture(context.Background())
		ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
			APIKey: []byte("sk-ant-oat01-live"), Source: credSourceSubscription, OAuth: true,
		})
		resp := http.Header{}
		resp.Set("anthropic-ratelimit-unified-status", "allowed")
		resp.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
		captureUnifiedLimitHeaders(ctx, resp)

		b := unifiedLimitHeadersJSON(ctx)
		require.NotNil(t, b)
		var got map[string]string
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Equal(t, "allowed", got["anthropic-ratelimit-unified-status"])
		assert.Equal(t, "0.42", got["anthropic-ratelimit-unified-5h-utilization"])
	})
}

// No-op contract: nothing captured when usageObserver is nil AND no subscription
// token is present — Phase 0 must not capture on every request.
func TestWithUsageObserver_NoopWhenNothingToObserve(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	got := s.withUsageObserver(ctx, http.Header{}) // no Authorization at all
	assert.Nil(t, UnifiedLimitHeadersFrom(got), "no subscription token present -> capture never installed")
}
