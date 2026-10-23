package proxy

import (
	"context"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSol   = "gpt-5.6-sol"
	testTerra = "gpt-5.6-terra"
	testOpus  = "z-ai/glm-5.2"
)

func ctxWithRequestSubset(ctx context.Context, models ...string) context.Context {
	return context.WithValue(ctx, RequestAllowedModelsContextKey{}, RequestAllowedModels{Requested: models, Effective: models})
}

func TestParseAllowedModelsHeader_BlankRejected(t *testing.T) {
	_, err := ParseAllowedModelsHeader(" , ", nil)
	var headerErr *AllowedModelsHeaderError
	require.ErrorAs(t, err, &headerErr)
}

func TestAllowedModelsForRequest_SubsetNarrowsPolicyAllowlist(t *testing.T) {
	ctx := ctxWithRequestSubset(ctxWithAllowedModels("a", "b"), "b", "c")
	assert.Equal(t, map[string]struct{}{"b": {}}, allowedModelsForRequest(ctx))
	assert.Equal(t, map[string]struct{}{"a": {}, "b": {}}, installationAllowedModelSet(ctx))
}

func TestAllowedModelsForRequest_SubsetAloneRestricts(t *testing.T) {
	ctx := ctxWithRequestSubset(context.Background(), "b")
	assert.Equal(t, map[string]struct{}{"b": {}}, allowedModelsForRequest(ctx))
	assert.Nil(t, installationAllowedModelSet(ctx))
}

func TestExcludedModelsForRequest_SubsetExcludesComplementButPolicySetDoesNot(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"a": {}, "b": {}, "c": {}}}
	ctx := ctxWithRequestSubset(ctxWithAllowedModels("a", "b"), "b")

	got := s.excludedModelsForRequest(ctx)
	assert.NotContains(t, got, "b")
	assert.Contains(t, got, "a")
	assert.Contains(t, got, "c")

	policy := s.policyExcludedModels(ctx)
	assert.NotContains(t, policy, "a")
	assert.NotContains(t, policy, "b")
	assert.Contains(t, policy, "c")
}

func TestModelPermittedByAllowlist_IgnoresRequestSubset(t *testing.T) {
	ctx := ctxWithRequestSubset(ctxWithAllowedModels("a", "b"), "b")
	assert.True(t, modelPermittedByAllowlist(ctx, "a"))
	assert.False(t, modelPermittedByAllowlist(ctx, "c"))
}

func TestTelemetryDecisionReason_PrefixesOnlyWithSubset(t *testing.T) {
	assert.Equal(t, "cluster_argmax", telemetryDecisionReason(context.Background(), "cluster_argmax"))
	ctx := ctxWithRequestSubset(context.Background(), "b")
	assert.Equal(t, AllowlistOverrideReasonPrefix+"cluster_argmax", telemetryDecisionReason(ctx, "cluster_argmax"))
	assert.Equal(t, translate.ReasonUserForceModel, telemetryDecisionReason(ctx, translate.ReasonUserForceModel))
}

func TestRequestedAllowedModelsForTelemetry(t *testing.T) {
	assert.Nil(t, requestedAllowedModelsForTelemetry(context.Background()))
	ctx := context.WithValue(context.Background(), RequestAllowedModelsContextKey{}, RequestAllowedModels{
		Requested: []string{testTerra, testSol},
		Effective: []string{testSol},
	})
	assert.Equal(t, []string{testSol, testTerra}, requestedAllowedModelsForTelemetry(ctx))
}

func TestReadmitForcedModel_LiftsSubsetOnlyExclusion(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{testSol: {}, testTerra: {}, testOpus: {}}}
	ctx := ctxWithRequestSubset(context.Background(), testSol)
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	req := router.Request{ExcludedModels: s.excludedModelsForRequest(ctx)}
	require.Contains(t, req.ExcludedModels, testTerra)

	pin := sessionpin.Pin{Model: testTerra, Provider: providers.ProviderAIAND}
	got := s.readmitForcedModel(ctx, req, env, translate.RoutingFeatures{MaxTokens: 16}, pin)
	assert.NotContains(t, got, testTerra)
	assert.Contains(t, got, testOpus)
	assert.Contains(t, req.ExcludedModels, testTerra, "input map must not be mutated")
}

func TestReadmitForcedModel_KeepsPolicyExclusion(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{testSol: {}, testTerra: {}, testOpus: {}}}
	ctx := ctxWithRequestSubset(ctxWithAllowedModels(testSol, testTerra), testSol)
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	req := router.Request{ExcludedModels: s.excludedModelsForRequest(ctx)}

	pin := sessionpin.Pin{Model: testOpus, Provider: providers.ProviderAIAND}
	got := s.readmitForcedModel(ctx, req, env, translate.RoutingFeatures{MaxTokens: 16}, pin)
	assert.Contains(t, got, testOpus)
}

func TestReadmitForcedModel_NoSubsetIsNoOp(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{testSol: {}, testTerra: {}}}
	req := router.Request{ExcludedModels: map[string]struct{}{testTerra: {}}}
	got := s.readmitForcedModel(context.Background(), req, nil, translate.RoutingFeatures{}, sessionpin.Pin{Model: testTerra})
	assert.Contains(t, got, testTerra)
}

func TestModelInRequestSubset(t *testing.T) {
	assert.True(t, modelInRequestSubset(context.Background(), testOpus))
	ctx := ctxWithRequestSubset(context.Background(), testSol)
	assert.True(t, modelInRequestSubset(ctx, testSol))
	assert.False(t, modelInRequestSubset(ctx, testOpus))
}

func TestForcedModelBinding_IgnoresRequestSubset(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{testSol: {}, testTerra: {}, testOpus: {}}}
	ctx := ctxWithRequestSubset(ctxWithAllowedModels(testSol, testTerra), testSol)

	binding, reason := s.forcedModelBinding(ctx, testTerra, providers.ProviderAIAND)
	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding)

	_, reason = s.forcedModelBinding(ctx, testOpus, providers.ProviderAIAND)
	assert.NotEmpty(t, reason, "installation allowlist still binds a forced model")
}

// A sticky pin outside the request subset must reroute inside it; a pin
// inside the subset still sticks.
func TestTurnLoop_StickyPinOutsideRequestSubsetReroutes(t *testing.T) {
	newSvc := func(fr *tierProbeRouter) *Service {
		store := &overwritingPinStore{pin: sessionpin.Pin{
			Provider:    providers.ProviderAIAND,
			Model:       testOpus,
			Reason:      "cluster:v0.2",
			PinnedUntil: time.Now().Add(time.Hour),
		}, found: true}
		return NewService(fr, nil, nil, false, nil, store, false,
			providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
			WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND, providers.ProviderAIAND))
	}
	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)

	fr := &tierProbeRouter{available: map[string]struct{}{testOpus: {}, "gpt-5.4-mini": {}}}
	svc := newSvc(fr)
	ctx := ctxWithRequestSubset(context.Background(), "gpt-5.4-mini")
	res, err := svc.runTurnLoop(ctx, env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model, ExcludedModels: svc.excludedModelsForRequest(ctx)})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.4-mini", res.Decision.Model)
	assert.False(t, res.StickyHit)

	fr = &tierProbeRouter{available: map[string]struct{}{testOpus: {}, "gpt-5.4-mini": {}}}
	svc = newSvc(fr)
	ctx = ctxWithRequestSubset(context.Background(), testOpus, "gpt-5.4-mini")
	res, err = svc.runTurnLoop(ctx, env, feats, "key-1", uuid.New(), "", nil,
		router.Request{RequestedModel: feats.Model, ExcludedModels: svc.excludedModelsForRequest(ctx)})
	require.NoError(t, err)
	assert.Equal(t, testOpus, res.Decision.Model)
}

func TestRequestAllowedModelsPresent(t *testing.T) {
	assert.False(t, requestAllowedModelsPresent(context.Background()))
	assert.True(t, requestAllowedModelsPresent(
		ctxWithRequestSubset(context.Background(), testSol)))
}
