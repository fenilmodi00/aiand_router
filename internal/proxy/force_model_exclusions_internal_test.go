package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The AIand-only cut left every catalog row with a single provider binding, so
// the multi-binding force-model cases (exclude one of two bindings, pin follows
// the survivor) have no fixture left and were removed with the cut. The remap
// itself is still exercised through a stored pin naming a provider that no
// longer serves the model.

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
		"model":"zai-org/glm-5.3",
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
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		excludedProvidersCtx(providers.ProviderAIAND), rec, env,
		translate.ForceModelResult{Model: "glm"},
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
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		InstallationExcludedModelsContextKey{}, []string{"zai-org/glm-5.3"})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "glm"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestForceModelCommand_SessionStrikeOutDoesNotReject keeps transient 529
// evidence from masquerading as operator policy.
func TestForceModelCommand_SessionStrikeOutDoesNotReject(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	ctx := context.WithValue(context.Background(),
		SessionDisabledProvidersContextKey{}, []string{providers.ProviderAIAND})
	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "glm"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	require.Len(t, store.upserts, 1,
		"a provider struck out for overload is not an exclusion")
}

// TestForceModelHeader_RejectsExcludedModel: the headless path has no synthetic
// response to warn through, so it fails the request instead.
func TestForceModelHeader_RejectsExcludedModel(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "glm")

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
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "glm")

	_, model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "zai-org/glm-5.3", model)
	require.Len(t, store.upserts, 1)
}

// The `:level` suffix must reach the caller's context and returned model spec:
// plan-owned dispatch omits routing knobs, so the returned spec carries the
// effort into req.ForceModel for the same turn.
func TestForceModelHeader_EffortSuffixLandsOnReturnedContext(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "glm:xhigh")

	ctx, model, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))

	require.NoError(t, forceErr)
	assert.Equal(t, "zai-org/glm-5.3:xhigh", model)
	knobs := routingKnobsForRequest(ctx)
	require.NotNil(t, knobs)
	assert.Equal(t, "xhigh", knobs.ForceEffort)
}

// TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects: policy can change
// mid-session; the pre-exclusion pin would otherwise silently re-route.
func TestTurnLoop_ForcedPinToNewlyExcludedProviderRejects(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "zai-org/glm-5.3",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
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
	fr := &tierProbeRouter{available: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "zai-org/glm-5.3",
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
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
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithExcludedModelsOverride([]string{"zai-org/glm-5.3"})

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env,
		translate.ForceModelResult{Model: "glm"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an env-excluded model must not be pinned")
	assert.Contains(t, rec.Body.String(), "force-model rejected")
}

// TestTurnLoop_ForcedPinFollowsSurvivingBinding: a stored pin naming an
// excluded provider must move to the model's permitted binding, not silently
// drop it to the scorer.
func TestTurnLoop_ForcedPinFollowsSurvivingBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "zai-org/glm-5.3",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAIAND))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAIAND),
		})

	require.NoError(t, err)
	assert.True(t, res.StickyHit, "the force must survive on the permitted binding")
	assert.Equal(t, providers.ProviderAIAND, res.Decision.Provider)
	assert.Equal(t, "zai-org/glm-5.3", res.Decision.Model)
	assert.Empty(t, fr.captured, "the pin must not fall through to the scorer")
}

// TestTurnLoop_StrikeExemptionCoversRemappedBinding: strike exemption must
// cover the remapped binding, or a 529 strike on it vetoes the force.
func TestTurnLoop_StrikeExemptionCoversRemappedBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:          providers.ProviderAnthropic,
		Model:             "zai-org/glm-5.3",
		Reason:            translate.ReasonUserForceModel,
		PinnedUntil:       pinNeverExpires,
		DisabledProviders: []string{providers.ProviderAIAND},
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAIAND))

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAIAND),
		})

	require.NoError(t, err)
	assert.True(t, res.StickyHit, "a session strike must not veto an explicit force")
	assert.Equal(t, providers.ProviderAIAND, res.Decision.Provider)
}

// TestTurnLoop_HardPinnedTurnForcedPinFollowsSurvivingBinding: the hard-pin
// fast path needs the same remap or the force loses probe/compaction turns.
func TestTurnLoop_HardPinnedTurnForcedPinFollowsSurvivingBinding(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAnthropic,
		Model:       "zai-org/glm-5.3",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(
			providers.ProviderAnthropic, providers.ProviderAIAND))

	env, err := translate.ParseAnthropic([]byte(
		`{"model":"zai-org/glm-5.3-flash","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(
		excludedProvidersCtx(providers.ProviderAnthropic),
		env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:   feats.Model,
			EnabledProviders: keyed(providers.ProviderAIAND),
		})

	require.NoError(t, err)
	require.Equal(t, turntype.Probe, res.TurnType, "fixture must exercise the hard-pinned path")
	assert.False(t, res.HardPinned, "the force outranks the hard pin")
	assert.Equal(t, "zai-org/glm-5.3", res.Decision.Model)
	assert.Equal(t, providers.ProviderAIAND, res.Decision.Provider)
}

func TestClassifyDispatchError_ForcedModelExcluded(t *testing.T) {
	cls, ok := ClassifyDispatchError(&ForcedModelExcludedError{
		Model:  "zai-org/glm-5.3",
		Reason: "zai-org/glm-5.3 is only served by aiand, which is excluded on this installation",
	})

	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError(),
		"an excluded force is a client-input problem, not an upstream failure")
	assert.Contains(t, cls.Message, "zai-org/glm-5.3")
	// Sigil-free: this classifier has no request context to tell a Codex
	// caller (which needs "$") from a Claude Code one (which needs "/").
	assert.Contains(t, cls.Message, "unforce-model")
	assert.NotContains(t, cls.Message, "/unforce-model")
}

// Desugaring only covers routableUniverse; kimi-k3 is outside both the
// routing-targets-only universe and the allowlist, so the allowlist check in
// forcedModelBinding must be direct.
func TestForcedModelBinding_RejectsPassthroughModelOutsideAllowlist(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		// nil availableModels enumerates the full catalog and masks the passthrough-
		// only bypass; must be a routing-targets-only universe.
		WithAvailableModels(map[string]struct{}{"zai-org/glm-5.3": {}})

	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"zai-org/glm-5.3"})

	binding, reason := svc.forcedModelBinding(ctx, "moonshotai/kimi-k3", providers.ProviderAIAND)

	assert.Empty(t, binding, "a passthrough model outside the allowlist must not resolve a binding")
	assert.Contains(t, reason, "allowed-model list",
		"the refusal must name the allowlist, not a generic exclusion")
}

// The allowlist must only ever narrow: an allowlisted model still forces fine.
func TestForcedModelBinding_AllowsPassthroughModelInsideAllowlist(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	ctx := context.WithValue(context.Background(),
		InstallationAllowedModelsContextKey{}, []string{"zai-org/glm-5.3"})

	binding, reason := svc.forcedModelBinding(ctx, "zai-org/glm-5.3", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

// No allowlist configured = no restriction, so passthrough forcing is unchanged.
func TestForcedModelBinding_NoAllowlistLeavesPassthroughForcingUnchanged(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	binding, reason := svc.forcedModelBinding(context.Background(), "zai-org/glm-5.3", providers.ProviderAnthropic)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAnthropic, binding)
}

// A saved pin can outlive the provider that served it: the binding must
// refresh to the provider the catalog serves the model from now.
func TestForcedModelBinding_RefreshesStaleProviderOnSavedPin(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND))

	binding, reason := svc.forcedModelBinding(
		context.Background(), "deepseek-ai/deepseek-v4-flash", providers.ProviderAIAND)

	assert.Empty(t, reason)
	assert.Equal(t, providers.ProviderAIAND, binding,
		"a saved force pin must move to the provider that serves the model now")
}
