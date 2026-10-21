package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exhaustedSubToken = "sk-ant-oat01-subscription-token"

// observerWithSnapshot builds an observer pre-seeded with one snapshot for the
// given token, using a fixed clock so the reading stays fresh for the test.
func observerWithSnapshot(token string, snap usage.Snapshot) *usage.Observer {
	now := time.Unix(1_000_000, 0)
	o := usage.NewObserver([]byte("salt"), 10*time.Minute, func() time.Time { return now })
	o.Record(o.Key([]byte(token)), snap)
	return o
}

func exhaustedSnapshot() usage.Snapshot {
	return usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}}
}

func TestResolveAndInjectCredentials_SuppressedSubscriptionFallsThroughToBYOK(t *testing.T) {
	// With the subscription suppressed (its plan window is spent), a present
	// inbound subscription bearer must be skipped so resolution falls through to
	// the BYOK Anthropic key — the turn then serves on that key, not the dead
	// token, and bills at full cost (servedOnSubscription reads non-OAuth).
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-api-byok")},
	})
	ctx = withSuppressedClaudeSubscription(ctx)
	headers := http.Header{"Authorization": []string{"Bearer " + exhaustedSubToken}}

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", headers)
	creds := CredentialsFromContext(out)
	require.NotNil(t, creds)
	assert.False(t, creds.OAuth, "the spent subscription token must be skipped")
	assert.Equal(t, []byte("sk-ant-api-byok"), creds.APIKey,
		"resolution must fall through to the BYOK Anthropic key")
	assert.False(t, servedOnSubscription(out),
		"a suppressed-subscription turn must bill at full cost, not the subscription rate")
}

func TestResolveAndInjectCredentials_SuppressedSubscriptionFallsThroughToDeployment(t *testing.T) {
	// No BYOK, subscription credential present, subscription suppressed:
	// resolution resolves to NO credential, so the Anthropic provider client uses
	// its own deployment key (the Weave key).
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, exhaustedSubToken)
	ctx = withSuppressedClaudeSubscription(ctx)

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", http.Header{})
	assert.Nil(t, CredentialsFromContext(out),
		"with the subscription suppressed and no BYOK, no credential is set so the deployment key serves the turn")
}

func TestResolveAndInjectCredentials_SuppressedInboundBearerNotReResolved(t *testing.T) {
	// Self-hosted (no router key, no BYOK): the spent subscription arrives as an
	// inbound Authorization bearer. With suppression on, neither the
	// subscription-first block NOR the later ExtractClientCredentials fallback may
	// re-resolve it — resolution must end with no credential so the deployment
	// Anthropic key serves the turn.
	ctx := withSuppressedClaudeSubscription(context.Background())
	headers := http.Header{"Authorization": []string{"Bearer " + exhaustedSubToken}}

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", headers)
	assert.Nil(t, CredentialsFromContext(out),
		"a suppressed inbound subscription bearer must not be re-resolved via client extraction")
}

func TestResolveAndInjectCredentials_UnsuppressedSubscriptionStillWins(t *testing.T) {
	// Control: without suppression, the subscription token still wins (the steady
	// state for a plan with headroom) — the suppression carve-out must not leak
	// into the normal path.
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, exhaustedSubToken)

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", http.Header{})
	creds := CredentialsFromContext(out)
	require.NotNil(t, creds)
	assert.True(t, creds.OAuth, "a non-suppressed subscription must still be resolved")
	assert.Equal(t, credSourceSubscription, creds.Source)
}
