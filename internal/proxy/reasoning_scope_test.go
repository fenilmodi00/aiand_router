package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cortexBaseURL = "https://acct-a.snowflakecomputing.com/api/v2/cortex/v1"

func reasoningScopeService(t *testing.T, clients map[string]providers.Client) *Service {
	t.Helper()
	return &Service{clients: dispatch.NewClients(clients)}
}

// Encrypted reasoning only decrypts under the account and model that minted
// it, so every dimension that can change the upstream identity mid-session
// must change the scope — otherwise the replay 400s and ends the session.
func TestReasoningReplayScope_DistinguishesUpstreams(t *testing.T) {
	svc := reasoningScopeService(t, nil)
	base := &requestcontext.Credentials{
		APIKey:      []byte("WIF.GCP.token-a"),
		Source:      requestcontext.SourceBYOK,
		BaseURL:     cortexBaseURL,
		PrincipalID: "external-key:key-a",
	}
	ctx := requestcontext.WithCredentials(context.Background(), base)
	decision := router.Decision{Provider: "openai_compat", Model: "moonshotai/kimi-k3"}
	want := svc.reasoningReplayScope(ctx, decision)
	require.NotEmpty(t, want)
	assert.Equal(t, want, svc.reasoningReplayScope(ctx, decision), "same upstream keeps the scope stable across turns")

	for name, tc := range map[string]struct {
		creds    requestcontext.Credentials
		decision router.Decision
	}{
		"another model":    {*base, router.Decision{Provider: decision.Provider, Model: "zai-org/glm-5.3-flash"}},
		"another provider": {*base, router.Decision{Provider: "openai", Model: decision.Model}},
		"another principal": {
			requestcontext.Credentials{APIKey: base.APIKey, Source: base.Source, BaseURL: base.BaseURL, PrincipalID: "external-key:key-b"},
			decision,
		},
		"another account host": {
			requestcontext.Credentials{
				APIKey:      base.APIKey,
				Source:      base.Source,
				BaseURL:     "https://acct-b.snowflakecomputing.com/api/v2/cortex/v1",
				PrincipalID: base.PrincipalID,
			},
			decision,
		},
		"another credential source": {
			requestcontext.Credentials{APIKey: base.APIKey, Source: requestcontext.SourceClient, BaseURL: base.BaseURL, PrincipalID: base.PrincipalID},
			decision,
		},
		"aliased to another wire model": {
			requestcontext.Credentials{
				APIKey:       base.APIKey,
				Source:       base.Source,
				BaseURL:      base.BaseURL,
				PrincipalID:  base.PrincipalID,
				ModelAliases: map[string]string{decision.Model: "motif-technologies/motif-3"},
			},
			decision,
		},
	} {
		t.Run(name, func(t *testing.T) {
			creds := tc.creds
			got := svc.reasoningReplayScope(requestcontext.WithCredentials(context.Background(), &creds), tc.decision)
			assert.NotEqual(t, want, got)
		})
	}
}

// Cortex mints a fresh workload-identity bearer per request and still decrypts
// a Grok item minted under the previous one, so the bearer must not narrow the
// scope: keying on it would drop reasoning the upstream would have accepted.
func TestReasoningReplayScope_SurvivesWorkloadIdentityTokenRefresh(t *testing.T) {
	svc := reasoningScopeService(t, nil)
	decision := router.Decision{Provider: "openai_compat", Model: "deepseek-ai/deepseek-v4-pro"}
	scopeFor := func(bearer string) string {
		return svc.reasoningReplayScope(requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
			APIKey:      []byte(bearer),
			Source:      requestcontext.SourceBYOK,
			BaseURL:     cortexBaseURL,
			AuthType:    "workload_identity_federation",
			PrincipalID: "external-key:key-a",
		}), decision)
	}

	assert.Equal(t, scopeFor("WIF.GCP.token-a"), scopeFor("WIF.GCP.token-b"))
}

// Only cosmetic endpoint differences are forgiven; a different host is a
// different Snowflake account.
func TestReasoningReplayScope_NormalizesEndpoint(t *testing.T) {
	svc := reasoningScopeService(t, nil)
	decision := router.Decision{Provider: "openai_compat", Model: "deepseek-ai/deepseek-v4-pro"}
	scopeFor := func(baseURL string) string {
		return svc.reasoningReplayScope(requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
			Source:      requestcontext.SourceBYOK,
			BaseURL:     baseURL,
			PrincipalID: "external-key:key-a",
		}), decision)
	}

	assert.Equal(t, scopeFor(cortexBaseURL),
		scopeFor("https://ACCT-A.snowflakecomputing.com:443/api/v2/cortex/v1/"))
	assert.NotEqual(t, scopeFor(cortexBaseURL),
		scopeFor("https://acct-b.snowflakecomputing.com/api/v2/cortex/v1"))
}

// Without a request credential the turn goes out on the adapter's deployment
// key, which no request-scoped value describes — two deployments of the same
// provider and model must still not share a scope.
func TestReasoningReplayScope_DeploymentKeyedTurnsAreDistinct(t *testing.T) {
	decision := router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek-ai/deepseek-v4-pro"}
	scopeFor := func(apiKey, baseURL string) string {
		svc := reasoningScopeService(t, map[string]providers.Client{
			providers.ProviderAIAND: openaicompat.NewClient(apiKey, baseURL),
		})
		return svc.reasoningReplayScope(requestcontext.WithCredentials(context.Background(), nil), decision)
	}

	deploymentA := scopeFor("key-a", cortexBaseURL)
	require.NotEmpty(t, deploymentA)
	assert.Equal(t, deploymentA, scopeFor("key-a", cortexBaseURL))
	assert.NotEqual(t, deploymentA, scopeFor("key-b", cortexBaseURL),
		"a second deployment's key is a second upstream account")
	assert.NotEqual(t, deploymentA, scopeFor("key-a", "https://acct-b.snowflakecomputing.com/api/v2/cortex/v1"))

	byok := reasoningScopeService(t, nil).reasoningReplayScope(requestcontext.WithCredentials(context.Background(),
		&requestcontext.Credentials{Source: requestcontext.SourceBYOK, BaseURL: cortexBaseURL, PrincipalID: "external-key:key-a"}), decision)
	assert.NotEqual(t, deploymentA, byok)
}

// The scope travels to the client inside the minted signature, so it must not
// leak the key it is derived from.
func TestReasoningReplayScope_DoesNotLeakCredential(t *testing.T) {
	svc := reasoningScopeService(t, nil)
	creds := &requestcontext.Credentials{APIKey: []byte("sk-secret-value"), AccountID: []byte("acct-secret")}
	scope := svc.reasoningReplayScope(requestcontext.WithCredentials(context.Background(), creds),
		router.Decision{Provider: "openai", Model: "deepseek-ai/deepseek-v4-flash"})

	assert.NotContains(t, scope, "sk-secret-value")
	assert.NotContains(t, scope, "acct-secret")
	assert.NotContains(t, openaicompat.NewClient("sk-secret-value", cortexBaseURL).DeploymentPrincipal(), "sk-secret-value")
}
