package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiBindingModel is served by Bedrock (primary) and OpenRouter (fallback),
// so excluding one provider must not be enough to refuse a force of it.
const multiBindingModel = "qwen/qwen3-235b-a22b-2507"

func excludedProvidersCtx(names ...string) context.Context {
	return context.WithValue(context.Background(),
		InstallationExcludedProvidersContextKey{}, names)
}

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
		"model":"z-ai/glm-5.2",
		"messages":[{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	return env
}

// TestForceModelCommand_RejectsSoleProviderExcluded: core case — pinning
// would have reported success, then served a different model every turn.
func TestForceModelCommand_RejectsSoleProviderExcluded(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderAIAND), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "a refused force must not write a pin")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
	assert.Contains(t, rec.Body.String(), providers.ProviderAIAND,
		"the caller must be told which provider is excluded")
}

// TestForceModelCommand_RejectsExcludedModel covers the model-level list, which
// is enforced independently of the provider list.
func TestForceModelCommand_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.2"})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestForceModelCommand_SessionStrikeOutDoesNotReject keeps transient 529
// evidence from masquerading as operator policy.
func TestForceModelCommand_SessionStrikeOutDoesNotReject(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		SessionDisabledProvidersContextKey{}, []string{providers.ProviderAIAND})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1,
		"a provider struck out for overload is not an exclusion")
}

// TestForceModelHeader_RejectsExcludedModel: the headless path has no synthetic
// response to warn through, so it fails the request instead.
func TestForceModelHeader_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus")

	_, model, forceErr := svc.applyForceModelHeader(
		excludedProvidersCtx(providers.ProviderAIAND), req,
		uuid.New(), DeriveSessionKey(env, "key-1"))

	require.Error(t, forceErr)
	assert.True(t, errors.Is(forceErr, ErrForcedModelExcluded))
	assert.Empty(t, model)
	assert.Empty(t, store.upserts, "a refused header force must not write a pin")
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
	assert.Equal(t, "z-ai/glm-5.2", model)
	require.Len(t, store.upserts, 1)
}

// TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects: policy can change
// mid-session; the pre-exclusion pin would otherwise silently re-route.
func TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.2",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	_, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAIAND),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model})

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForcedModelExcluded))
	assert.Empty(t, fr.captured,
		"the request must fail rather than quietly re-route through the scorer")
}

// TestTurnLoop_AutomaticPinToExcludedProviderStillFallsThrough: only an
// explicit user force fails the request; automatic pins degrade gracefully.
func TestTurnLoop_AutomaticPinToExcludedProviderStillFallsThrough(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.2",
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	_, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAIAND),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model})

	require.NoError(t, err, "an automatic pin must not fail the request")
}

// TestForceModelCommand_RejectsDeploymentExcludedModel: ROUTER_EXCLUDED_MODELS
// is as authoritative as the per-installation list.
func TestForceModelCommand_RejectsDeploymentExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithExcludedModelsOverride([]string{"z-ai/glm-5.2"})

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
		InstallationAllowedModelsContextKey{}, []string{"z-ai/glm-5.2"})

	binding, reason := svc.forcedModelBinding(ctx, "z-ai/glm-5.2", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

// No allowlist configured = no restriction, so passthrough forcing is unchanged.
func TestForcedModelBinding_NoAllowlistLeavesPassthroughForcingUnchanged(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(context.Background(), "z-ai/glm-5.2", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

func TestForcedModelBinding_RefreshesRetiredProviderOnSavedPin(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(
		context.Background(), "deepseek/deepseek-v4-flash", providers.ProviderAIAND)

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
		gatewayKeyCtx("z-ai/glm-5.2"), "z-ai/glm-5.2", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)
}

// Gateway-exclusive forcing skips only the positive allowlist; an explicit
// exclusion still refuses the model.
func TestForcedModelBinding_GatewayExclusiveKeepsExplicitExclusion(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)
	ctx := context.WithValue(gatewayKeyCtx("z-ai/glm-5.2"),
		InstallationAllowedModelsContextKey{}, []string{"z-ai/glm-5.2"})

	binding, reason := svc.forcedModelBinding(ctx, "z-ai/glm-5.2", providers.ProviderAIAND)
	assert.Empty(t, reason, "the gateway, not the allowlist, bounds a force")
	assert.Equal(t, providers.ProviderAIAND, binding)

	ctx = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.2"})
	binding, reason = svc.forcedModelBinding(ctx, "z-ai/glm-5.2", providers.ProviderAIAND)
	assert.Empty(t, binding)
	assert.Contains(t, reason, "excluded on this installation")
}
