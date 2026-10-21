package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationID = "11111111-1111-1111-1111-111111111111"

const testCodexLunaModel = "gpt-6-luna"

func TestSubscriptionCredsFromToken(t *testing.T) {
	t.Run("accepts oat token", func(t *testing.T) {
		creds := subscriptionCredsFromToken("sk-ant-oat01-token")
		require.NotNil(t, creds)
		assert.True(t, creds.OAuth)
		assert.Equal(t, credSourceSubscription, creds.Source)
		assert.Equal(t, []byte("sk-ant-oat01-token"), creds.APIKey)
	})
	t.Run("trims whitespace", func(t *testing.T) {
		creds := subscriptionCredsFromToken("  sk-ant-oat01-token  ")
		require.NotNil(t, creds)
		assert.Equal(t, []byte("sk-ant-oat01-token"), creds.APIKey,
			"the token must be canonicalized before use")
	})
	t.Run("rejects api key", func(t *testing.T) {
		assert.Nil(t, subscriptionCredsFromToken("sk-ant-api-real-key"),
			"a real API key is not a subscription token and must not be flagged OAuth")
	})
	t.Run("rejects router key", func(t *testing.T) {
		assert.Nil(t, subscriptionCredsFromToken("rk_router_key"))
	})
	t.Run("rejects empty", func(t *testing.T) {
		assert.Nil(t, subscriptionCredsFromToken(""))
	})
}

func TestResolveAndInjectCredentials_SubscriptionBeatsBYOK(t *testing.T) {
	// Router-keyed request with both a BYOK key and a caller subscription:
	// subscription must win, reading past the router-key guard.
	ctx := context.Background()
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, testInstallationID)
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-api-byok")},
	})
	ctx = context.WithValue(ctx, AnthropicSubscriptionContextKey{}, "sk-ant-oat01-subscription-token")

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", http.Header{})
	creds := CredentialsFromContext(out)
	require.NotNil(t, creds)
	assert.True(t, creds.OAuth)
	assert.Equal(t, credSourceSubscription, creds.Source)
	assert.Equal(t, []byte("sk-ant-oat01-subscription-token"), creds.APIKey)
}

func TestResolveAndInjectCredentials_RouterKeyedInboundApiKeyNotForwarded(t *testing.T) {
	// Router-key path must not forward a general inbound API key, only the
	// sk-ant-oat OAuth subset — otherwise it'd widen the cross-provider-leak guard.
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, testInstallationID)
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-api-real-client-key"}}
	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", headers)
	assert.Nil(t, CredentialsFromContext(out),
		"a non-OAuth inbound API key must not be forwarded on the router-key path; the deployment key is the correct fallback")
}

const codexTestJWT = "eyJhbGciOiJSUzI1NiJ9.codex-access.signature"

func TestResolveAndInjectCredentials_RouterKeyedInboundOpenAIApiKeyNotForwarded(t *testing.T) {
	// Router-key path must not forward a general inbound OpenAI key — only the
	// Codex OAuth subset (JWT + ChatGPT-Account-ID) is honored.
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, testInstallationID)
	headers := http.Header{"Authorization": []string{"Bearer sk-proj-real-client-key"}}
	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "gpt-5.6-sol", headers)
	assert.Nil(t, CredentialsFromContext(out),
		"a non-OAuth inbound OpenAI key must not be forwarded on the router-key path; the deployment key is the correct fallback")
}

func TestResolveAndInjectCredentials_CodexSubscriptionIgnoredForNonOpenAI(t *testing.T) {
	// The Codex token can only pay for OpenAI; a non-OpenAI route must not
	// resolve it.
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, testInstallationID)
	ctx = context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-api-byok")},
	})
	ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, codexTestJWT)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, "acct-999")

	out := resolveAndInjectCredentials(ctx, providers.ProviderAIAND, "z-ai/glm-5.2", http.Header{})
	creds := CredentialsFromContext(out)
	require.NotNil(t, creds)
	assert.False(t, creds.OAuth)
	assert.Equal(t, []byte("sk-ant-api-byok"), creds.APIKey,
		"an Anthropic route must use its own BYOK key, never the Codex subscription token")
}

func TestClearCredentials(t *testing.T) {
	ctx := context.WithValue(context.Background(), CredentialsContextKey{},
		&Credentials{APIKey: []byte("sk-ant-oat01-token"), Source: credSourceSubscription, OAuth: true})
	require.NotNil(t, CredentialsFromContext(ctx))

	cleared := clearCredentials(ctx)
	assert.Nil(t, CredentialsFromContext(cleared),
		"clearCredentials must make CredentialsFromContext report none so the synthetic call falls back to the deployment key")
}

func TestResolveSummarizerCreds_DropsSubscriptionToken(t *testing.T) {
	// The synthetic summarizer body has no Claude Code identity block, which a
	// subscription token requires — so it must never run on one.
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-subscription-token"}}
	creds := resolveSummarizerCreds(context.Background(), providers.ProviderAIAND, headers)
	assert.Nil(t, creds,
		"resolveSummarizerCreds must decline a subscription OAuth token so the synthetic summary call doesn't 401")
}

func TestServedOnSubscription(t *testing.T) {
	t.Run("true for an OAuth credential", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), CredentialsContextKey{},
			&Credentials{APIKey: []byte("sk-ant-oat01-token"), Source: credSourceSubscription, OAuth: true})
		assert.True(t, servedOnSubscription(ctx), "billing must treat an OAuth-credentialed turn as subscription-served")
	})
	t.Run("false for a non-OAuth credential", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), CredentialsContextKey{},
			&Credentials{APIKey: []byte("sk-ant-api-byok"), Source: credSourceBYOK})
		assert.False(t, servedOnSubscription(ctx))
	})
	t.Run("false when no credential resolved (deployment key)", func(t *testing.T) {
		assert.False(t, servedOnSubscription(context.Background()),
			"a deployment-key turn is Weave-fronted and must bill at full cost")
	})
}

func TestResolveSummarizerCreds_ReturnsBYOK(t *testing.T) {
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-api-byok")},
	})
	creds := resolveSummarizerCreds(ctx, providers.ProviderAIAND, http.Header{})
	require.NotNil(t, creds)
	assert.Equal(t, []byte("sk-ant-api-byok"), creds.APIKey,
		"a real BYOK key is a valid summarizer credential and must be used")
}
