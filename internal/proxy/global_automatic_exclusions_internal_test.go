package proxy

import (
	"context"
	"errors"
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

// stubGlobalExclusionStore serves a fixed list and counts reads so the cache's
// TTL behavior is observable.
type stubGlobalExclusionStore struct {
	byModel map[string]string
	err     error
	calls   int
}

func (s *stubGlobalExclusionStore) ListGlobalAutomaticRoutingExclusions(context.Context) (map[string]string, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.byModel, nil
}

func TestGlobalAutomaticExclusions_ServesCachedSnapshotWithinTTL(t *testing.T) {
	store := &stubGlobalExclusionStore{byModel: map[string]string{"z-ai/glm-5.3": "too expensive"}}
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithGlobalAutomaticExclusions(store)

	first := svc.globalAutomaticExcludedModels(context.Background())
	second := svc.globalAutomaticExcludedModels(context.Background())

	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3": {}}, first)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, store.calls, "a second read inside the TTL must not re-query")
}

// A control-plane outage must not disable routing, so a cold cache that cannot
// be loaded reports nothing disabled and retries on the next turn.
func TestGlobalAutomaticExclusions_ColdReadFailureFailsOpen(t *testing.T) {
	store := &stubGlobalExclusionStore{err: errors.New("router database unreachable")}
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithGlobalAutomaticExclusions(store)

	assert.Empty(t, svc.globalAutomaticExcludedModels(context.Background()))
	assert.Empty(t, svc.globalAutomaticExcludedModels(context.Background()))
	assert.Equal(t, 2, store.calls, "a failed cold read must retry rather than cache emptiness for a full TTL")
}

func TestGlobalAutomaticExclusions_RefreshFailureKeepsLastSnapshot(t *testing.T) {
	store := &stubGlobalExclusionStore{byModel: map[string]string{"z-ai/glm-5.3": ""}}
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithGlobalAutomaticExclusions(store)

	require.NotEmpty(t, svc.globalAutomaticExcludedModels(context.Background()))
	store.err = errors.New("router database unreachable")
	svc.globalAutomaticExclusions.refreshedAt = time.Now().Add(-2 * globalAutomaticExclusionTTL)

	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3": {}},
		svc.globalAutomaticExcludedModels(context.Background()))
}

// The soft set must reach the router as its own field: folding it into
// ExcludedModels would also reject explicit force-model pins.
func TestWithPolicyRequestContext_CarriesAutomaticExclusionsSeparately(t *testing.T) {
	store := &stubGlobalExclusionStore{byModel: map[string]string{"z-ai/glm-5.3": ""}}
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithGlobalAutomaticExclusions(store)

	req := svc.withPolicyRequestContext(context.Background(), router.Request{})

	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3": {}}, req.AutomaticExcludedModels)
	assert.Empty(t, req.ExcludedModels)
}

func automaticallyDisabledRequest(model string) router.Request {
	return router.Request{AutomaticExcludedModels: map[string]struct{}{model: {}}}
}

func TestAutomaticPinEligible_RejectsDisabledModelButForcedPinSurvives(t *testing.T) {
	pin := sessionpin.Pin{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND}
	req := automaticallyDisabledRequest("z-ai/glm-5.3")

	assert.False(t, automaticPinEligible(pin, req))
	assert.True(t, forcedPinEligible(pin, req),
		"a user-forced pin is the escape hatch that keeps a disabled model reachable")
}

// A session already pinned to a model must move off it once the model is
// disabled — otherwise the setting only affects sessions that route fresh.
func TestTurnLoop_DropsAutomaticPinOnDisabledModel(t *testing.T) {
	fakeRouter := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.3",
		Reason:      "postgres",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fakeRouter, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{
			byModel: map[string]string{"z-ai/glm-5.3": "quality regression"},
		})

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:          feats.Model,
			EnabledProviders:        keyed(providers.ProviderAIAND),
			AutomaticExcludedModels: map[string]struct{}{"z-ai/glm-5.3": {}},
		})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", res.Decision.Model)
	assert.False(t, res.StickyHit, "the disabled pin must not serve this turn")
}

// The same disable must leave an explicit /force-model pin serving.
func TestTurnLoop_KeepsForcedPinOnDisabledModel(t *testing.T) {
	fakeRouter := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.3",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: pinNeverExpires,
	}, found: true}
	svc := NewService(fakeRouter, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{
			byModel: map[string]string{"z-ai/glm-5.3": "quality regression"},
		})

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:          feats.Model,
			EnabledProviders:        keyed(providers.ProviderAIAND),
			AutomaticExcludedModels: map[string]struct{}{"z-ai/glm-5.3": {}},
		})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3", res.Decision.Model)
	assert.True(t, res.StickyHit)
}

// Loop escalation is a router-chosen rescue, so it must not resurrect a model
// the deployment disabled.
func TestTurnLoop_DropsEscalationPinOnDisabledModel(t *testing.T) {
	fakeRouter := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.3",
		Reason:      translate.ReasonLoopEscalation,
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	svc := NewService(fakeRouter, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAIAND)).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{
			byModel: map[string]string{"z-ai/glm-5.3": ""},
		})

	env := forceCommandEnv(t)
	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil,
		router.Request{
			RequestedModel:          feats.Model,
			EnabledProviders:        keyed(providers.ProviderAIAND),
			AutomaticExcludedModels: map[string]struct{}{"z-ai/glm-5.3": {}},
		})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", res.Decision.Model)
}
