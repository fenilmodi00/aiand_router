package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keyed(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func forceCommandEnv(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"z-ai/glm-5.3",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	return env
}

// TestForceModelCommand_RejectsExcludedModel covers the model-level list,
// enforced loudly so a forced pin is never silently re-routed.
func TestForceModelCommand_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.3"})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestForceModelHeader_UnfencedInstallationUnaffected pins the regression
// boundary: no exclusions means the header behaves exactly as before.
func TestForceModelHeader_UnfencedInstallationUnaffected(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus")

	_, model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "z-ai/glm-5.3", model)
	require.Len(t, store.upserts, 1)
}

// TestForceModelCommand_RejectsDeploymentExcludedModel: ROUTER_EXCLUDED_MODELS
// is as authoritative as the per-installation list.
func TestForceModelCommand_RejectsDeploymentExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithExcludedModelsOverride([]string{"z-ai/glm-5.3"})

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an env-excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// The allowlist must only ever narrow: an allowlisted model still forces fine.
func TestForcedModelBinding_AllowsPassthroughModelInsideAllowlist(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"z-ai/glm-5.3"})

	binding, reason := svc.forcedModelBinding(ctx, "z-ai/glm-5.3", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

// No allowlist configured = no restriction, so passthrough forcing is unchanged.
func TestForcedModelBinding_NoAllowlistLeavesPassthroughForcingUnchanged(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(context.Background(), "z-ai/glm-5.3", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

func TestForcedModelBinding_RefreshesRetiredProviderOnSavedPin(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(
		context.Background(), "deepseek/deepseek-v4.1-flash", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding,
		"a saved force pin must move off Makora after its V4 binding is retired")
}

func gatewayKeyCtx(model string) context.Context {
	return context.WithValue(context.Background(), ExternalAPIKeysContextKey{},
		[]*auth.ExternalAPIKey{{
			Provider:     providers.ProviderAIAND,
			Plaintext:    []byte("pat"),
			ModelAliases: map[string]string{model: "upstream-name"},
		}})
}

// Gateway-exclusive routing drops the vendor primary, so a force resolved to
// it would be rejected by the pin check and route automatically instead.
func TestForcedModelBinding_GatewayExclusivePinsTheGateway(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(
		gatewayKeyCtx("z-ai/glm-5.3"), "z-ai/glm-5.3", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

// Gateway-exclusive forcing skips only the positive allowlist; an explicit
// exclusion still refuses the model.
func TestForcedModelBinding_GatewayExclusiveKeepsExplicitExclusion(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)
	ctx := context.WithValue(gatewayKeyCtx("z-ai/glm-5.3"),
		InstallationAllowedModelsContextKey{}, []string{"z-ai/glm-5.3"})

	binding, reason := svc.forcedModelBinding(ctx, "z-ai/glm-5.3", providers.ProviderAIAND)
	assert.Empty(t, reason, "the gateway, not the allowlist, bounds a force")
	assert.Equal(t, providers.ProviderAIAND, binding)

	ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.3"})
	binding, reason = svc.forcedModelBinding(ctx, "z-ai/glm-5.3", providers.ProviderAIAND)
	assert.Empty(t, binding)
	assert.Contains(t, reason, "excluded on this installation")
}
