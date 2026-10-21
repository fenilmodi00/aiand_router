package proxy_test

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCredentialsMap_NilOnEmptySlice(t *testing.T) {
	m := proxy.BuildCredentialsMap(nil)
	assert.Nil(t, m,
		"BuildCredentialsMap must return nil when given no keys so callers can "+
			"cheaply distinguish 'no BYOK configured' from 'BYOK configured but empty'")
}

func TestBuildCredentialsMap_IndexesByProvider(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{Provider: "anthropic", Plaintext: []byte("sk-ant-byok")},
		{Provider: "openai", Plaintext: []byte("sk-oai-byok")},
	}
	m := proxy.BuildCredentialsMap(keys)
	require.NotNil(t, m)
	require.Contains(t, m, "anthropic")
	assert.Equal(t, []byte("sk-ant-byok"), m["anthropic"].APIKey)
	assert.Equal(t, "byok", m["anthropic"].Source,
		"BYOK credentials must carry Source='byok' for observability")
	require.Contains(t, m, "openai")
	assert.Equal(t, []byte("sk-oai-byok"), m["openai"].APIKey)
}

func TestBuildCredentialsMap_DropsEmptyPlaintext(t *testing.T) {
	// An empty Plaintext means a stale/malformed BYOK row; it must not enroll
	// the provider, or argmax would pick it and the upstream call would 401.
	keys := []*auth.ExternalAPIKey{
		{Provider: "openrouter", Plaintext: []byte{}},
		{Provider: "anthropic", Plaintext: []byte("sk-ant-byok")},
	}
	m := proxy.BuildCredentialsMap(keys)
	require.NotNil(t, m)
	assert.NotContains(t, m, "openrouter",
		"BuildCredentialsMap must drop entries with empty Plaintext so the routing layer cannot enroll a provider that would 401 on dispatch")
	assert.Contains(t, m, "anthropic")
}

func TestBuildCredentialsMap_NilWhenAllEmpty(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{Provider: "openrouter", Plaintext: []byte{}},
		{Provider: "fireworks", Plaintext: nil},
	}
	m := proxy.BuildCredentialsMap(keys)
	assert.Nil(t, m,
		"when every BYOK entry is empty the map must be nil so callers see 'no BYOK configured' rather than 'BYOK present but unusable'")
}

func TestExtractClientCredentials_MissingHeader(t *testing.T) {
	creds := proxy.ExtractClientCredentials("anthropic", http.Header{})
	assert.Nil(t, creds,
		"ExtractClientCredentials must return nil when the required header is absent")
}

func TestExtractClientCredentials_RejectsRouterBearerForOpenAI(t *testing.T) {
	headers := http.Header{"Authorization": []string{"Bearer rk_should_not_leak_upstream"}}
	creds := proxy.ExtractClientCredentials("openai", headers)
	assert.Nil(t, creds,
		"router-issued bearer tokens (rk_...) must never be forwarded as upstream OpenAI credentials")
}

func TestExtractClientCredentials_RejectsRouterBearerForGoogle(t *testing.T) {
	headers := http.Header{"Authorization": []string{"Bearer rk_should_not_leak_upstream"}}
	creds := proxy.ExtractClientCredentials("google", headers)
	assert.Nil(t, creds,
		"router-issued bearer tokens (rk_...) must never be forwarded as upstream Google credentials")
}

func TestExtractClientCredentials_RejectsRouterBearerForOpenRouter(t *testing.T) {
	headers := http.Header{"Authorization": []string{"Bearer rk_should_not_leak_upstream"}}
	creds := proxy.ExtractClientCredentials("openrouter", headers)
	assert.Nil(t, creds,
		"router-issued bearer tokens (rk_...) must never be forwarded as upstream OpenRouter credentials")
}

func TestExtractClientCredentials_RejectsRouterBearerForFireworks(t *testing.T) {
	headers := http.Header{"Authorization": []string{"Bearer rk_should_not_leak_upstream"}}
	creds := proxy.ExtractClientCredentials("fireworks", headers)
	assert.Nil(t, creds,
		"router-issued bearer tokens (rk_...) must never be forwarded as upstream Fireworks credentials")
}

func TestExtractClientCredentials_OpenRouterNoAuthHeader(t *testing.T) {
	// Anthropic-format clients have no Authorization header; must return nil
	// so the caller falls back to the deployment-level env key.
	headers := http.Header{"X-Api-Key": []string{"rk_router_key"}}
	creds := proxy.ExtractClientCredentials("openrouter", headers)
	assert.Nil(t, creds,
		"when no Authorization header is present, ExtractClientCredentials must return nil for openrouter "+
			"so setAuth falls back to the deployment-level OPENROUTER_API_KEY env key")
}

func TestExtractClientCredentials_RejectsRouterKeyForAnthropic(t *testing.T) {
	headers := http.Header{"X-Api-Key": []string{"rk_should_not_leak_upstream"}}
	creds := proxy.ExtractClientCredentials("anthropic", headers)
	assert.Nil(t, creds,
		"router-issued tokens (rk_...) supplied via x-api-key must never be forwarded as upstream Anthropic credentials")
}

func TestExtractClientCredentials_RejectsRouterBearerWithLeadingWhitespace(t *testing.T) {
	headers := http.Header{"Authorization": []string{"Bearer  rk_whitespace_bypass"}}
	creds := proxy.ExtractClientCredentials("openai", headers)
	assert.Nil(t, creds,
		"the router-key guard must canonicalize whitespace; the auth middleware accepts 'Bearer  rk_...' as a router credential, so this path must not forward it upstream")
}

func TestExtractClientCredentials_RejectsRouterKeyWithLeadingWhitespaceForAnthropic(t *testing.T) {
	headers := http.Header{"X-Api-Key": []string{"  rk_whitespace_bypass"}}
	creds := proxy.ExtractClientCredentials("anthropic", headers)
	assert.Nil(t, creds,
		"x-api-key values must be TrimSpace'd before the prefix check to match the auth middleware's normalization")
}

func TestCredentialsFromContext_ReturnsNilWhenAbsent(t *testing.T) {
	creds := proxy.CredentialsFromContext(context.Background())
	assert.Nil(t, creds,
		"CredentialsFromContext must return nil when no credentials are on the context")
}

func TestCredentialsFromContext_ReturnsStashedCredentials(t *testing.T) {
	want := &proxy.Credentials{APIKey: []byte("sk-test"), Source: "byok"}
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, want)
	got := proxy.CredentialsFromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, want.Source, got.Source)
	assert.Equal(t, want.APIKey, got.APIKey)
}

func TestExtractClientCredentials_RejectsSubscriptionForNonAnthropic(t *testing.T) {
	// A subscription token can only pay for Claude models; it must never be
	// forwarded to another vendor's upstream.
	for _, provider := range []string{"openai", "google", "openrouter", "fireworks"} {
		headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-subscription-token"}}
		creds := proxy.ExtractClientCredentials(provider, headers)
		assert.Nilf(t, creds, "a Claude subscription bearer must never be forwarded to %s", provider)
	}
}

const codexJWT = "eyJhbGciOiJSUzI1NiJ9.codex-access-token.signature"

func TestExtractClientCredentials_CodexSubscriptionIsOpenAIOnly(t *testing.T) {
	// The Codex JWT is OpenAI-only; an account-id header on another vendor's
	// surface must not produce an OAuth credential there.
	headers := http.Header{
		"Authorization":      []string{"Bearer " + codexJWT},
		"Chatgpt-Account-Id": []string{"acct-12345"},
	}
	for _, provider := range []string{"google", "openrouter", "fireworks"} {
		creds := proxy.ExtractClientCredentials(provider, headers)
		if creds != nil {
			assert.Falsef(t, creds.OAuth, "a Codex subscription must never be resolved for %s", provider)
			assert.Emptyf(t, creds.AccountID, "no Codex account-id must attach to %s creds", provider)
		}
	}
}

func TestExtractClientCredentials_RejectsRouterBearerEvenWithAccountID(t *testing.T) {
	headers := http.Header{
		"Authorization":      []string{"Bearer rk_router_key"},
		"Chatgpt-Account-Id": []string{"acct-12345"},
	}
	creds := proxy.ExtractClientCredentials("openai", headers)
	assert.Nil(t, creds,
		"a router key must never be classified as a Codex subscription, account-id present or not")
}

func TestEffectiveUpstreamModel(t *testing.T) {
	ctx := context.Background()

	t.Run("unchanged without credentials", func(t *testing.T) {
		assert.Equal(t, "z-ai/glm-5.2", proxy.EffectiveUpstreamModel(ctx, "z-ai/glm-5.2"))
	})

	t.Run("unchanged for a model the key does not alias", func(t *testing.T) {
		withCreds := context.WithValue(ctx, proxy.CredentialsContextKey{}, &proxy.Credentials{
			ModelAliases: map[string]string{"gpt-5.5": "gw-gpt"},
		})
		assert.Equal(t, "z-ai/glm-5.2", proxy.EffectiveUpstreamModel(withCreds, "z-ai/glm-5.2"),
			"an unaliased model must keep its catalog id rather than fall back to some other key's alias")
	})

	t.Run("rewrites an aliased model", func(t *testing.T) {
		withCreds := context.WithValue(ctx, proxy.CredentialsContextKey{}, &proxy.Credentials{
			ModelAliases: map[string]string{"z-ai/glm-5.2": "gw-fable"},
		})
		assert.Equal(t, "gw-fable", proxy.EffectiveUpstreamModel(withCreds, "z-ai/glm-5.2"))
	})
}

func TestBuildCredentialsMap_CarriesModelAliases(t *testing.T) {
	m := proxy.BuildCredentialsMap([]*auth.ExternalAPIKey{{
		Provider:     "anthropic_gateway",
		Plaintext:    []byte("token"),
		ModelAliases: map[string]string{"z-ai/glm-5.2": "gw-fable"},
	}})
	require.NotNil(t, m["anthropic_gateway"])
	assert.Equal(t, map[string]string{"z-ai/glm-5.2": "gw-fable"}, m["anthropic_gateway"].ModelAliases,
		"aliases must ride on the credential, or the endpoint receives catalog names it doesn't publish")
}

func TestApplyModelAlias(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.2","messages":[]}`)

	t.Run("leaves the body untouched without an alias", func(t *testing.T) {
		got := proxy.ApplyModelAlias(context.Background(), body, "z-ai/glm-5.2")
		assert.Equal(t, string(body), string(got),
			"the envelope owns the body's model on every non-aliased request; rewriting it here would silently override that")
	})

	t.Run("rewrites the model when the key aliases it", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
			ModelAliases: map[string]string{"z-ai/glm-5.2": "gw-fable"},
		})
		got := proxy.ApplyModelAlias(ctx, body, "z-ai/glm-5.2")
		assert.Equal(t, `{"model":"gw-fable","messages":[]}`, string(got))
	})

	t.Run("an alias equal to the catalog id still overwrites an adapter's rewrite", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
			ModelAliases: map[string]string{"z-ai/glm-5.2": "z-ai/glm-5.2"},
		})
		rewritten := []byte(`{"model":"vendor/claude-fable-5-0125","messages":[]}`)
		got := proxy.ApplyModelAlias(ctx, rewritten, "z-ai/glm-5.2")
		assert.Equal(t, `{"model":"z-ai/glm-5.2","messages":[]}`, string(got),
			"a key that explicitly maps a model to the catalog id is opting out of the global binding's upstream id")
	})

	t.Run("leaves a body with no model field alone", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, &proxy.Credentials{
			ModelAliases: map[string]string{"z-ai/glm-5.2": "gw-fable"},
		})
		noModel := []byte(`{"messages":[]}`)
		got := proxy.ApplyModelAlias(ctx, noModel, "z-ai/glm-5.2")
		assert.Equal(t, string(noModel), string(got),
			"surfaces that carry the model outside the body (e.g. in the URL) must not gain a stray field")
	})
}
