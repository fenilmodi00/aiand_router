package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tierProbeRouter reproduces the production collapse: it returns the cheapest
// eligible candidate, so an unconstrained fallback would pick the low-tier
// default. It records every request so the tier constraint can be asserted.
type tierProbeRouter struct {
	available map[string]struct{}
	captured  []router.Request
}

func (r *tierProbeRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.captured = append(r.captured, req)
	best := ""
	bestTier := catalog.TierHigh + 1
	for m := range r.available {
		if _, excluded := req.ExcludedModels[m]; excluded {
			continue
		}
		if t := catalog.TierFor(m); best == "" || t < bestTier {
			best, bestTier = m, t
		}
	}
	if best == "" {
		return router.Decision{}, errors.New("no eligible candidate")
	}
	return router.Decision{Provider: providers.ProviderAIAND, Model: best, Reason: "fake"}, nil
}

// forcedPinStore returns a single user-forced pin for every lookup.
type forcedPinStore struct {
	pin sessionpin.Pin
}

func (s *forcedPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	p := s.pin
	p.SessionKey = key
	p.Role = role
	return p, true, nil
}
func (s *forcedPinStore) Upsert(context.Context, sessionpin.Pin) error { return nil }
func (s *forcedPinStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}
func (s *forcedPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (s *forcedPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (s *forcedPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (s *forcedPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*forcedPinStore) ExpireAndDemoteModel(context.Context, sessionpin.Pin, string, sessionpin.DemotionReason) error {
	return nil
}

func (s *forcedPinStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}
func (s *forcedPinStore) Consume(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}
func (s *forcedPinStore) SweepExpired(context.Context) error { return nil }

type overwritingPinStore struct {
	pin   sessionpin.Pin
	found bool
}

func (s *overwritingPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	if !s.found {
		return sessionpin.Pin{}, false, nil
	}
	p := s.pin
	p.SessionKey = key
	p.Role = role
	return p, true, nil
}

func (s *overwritingPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.pin = p
	s.found = true
	return nil
}
func (s *overwritingPinStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}
func (s *overwritingPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (s *overwritingPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (s *overwritingPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (s *overwritingPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*overwritingPinStore) ExpireAndDemoteModel(context.Context, sessionpin.Pin, string, sessionpin.DemotionReason) error {
	return nil
}

func (s *overwritingPinStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}
func (s *overwritingPinStore) Consume(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}
func (s *overwritingPinStore) SweepExpired(context.Context) error { return nil }

func TestRunTurnLoop_ForcedModelOverridesHardPin(t *testing.T) {
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.2",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	svc := NewService(
		fr,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"z-ai/glm-5.2",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
	})
	require.NoError(t, err)

	assert.Equal(t, "z-ai/glm-5.2", res.Decision.Model,
		"an explicit force-model pin must outrank the automatic compaction hard-pin")
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.True(t, res.StickyHit)
	assert.False(t, res.HardPinned)
	assert.Empty(t, fr.captured, "a forced pin must not invoke the scorer")
}

// Regression: a forced pin targeting a session-struck-out provider must
// serve through the fast path; the breaker skips force-model to avoid
// silently reverting an explicit user choice.
func TestRunTurnLoop_ForcedModelServesDespiteDisabledProvider(t *testing.T) {
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:          providers.ProviderAIAND,
		Model:             "z-ai/glm-5.2",
		Reason:            translate.ReasonUserForceModel,
		PinnedUntil:       time.Now().Add(time.Hour),
		DisabledProviders: []string{providers.ProviderAIAND},
	}}
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	svc := NewService(
		fr,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"z-ai/glm-5.2",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel:   feats.Model,
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
	})
	require.NoError(t, err)

	assert.Equal(t, "z-ai/glm-5.2", res.Decision.Model,
		"force-model must serve through the pinned provider despite the session-level disable")
	assert.Equal(t, providers.ProviderAIAND, res.Decision.Provider)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.True(t, res.StickyHit)
	assert.Empty(t, fr.captured, "a forced pin must not invoke the scorer")
}

func TestForceModelHeader_OverridesHardPin(t *testing.T) {
	store := &overwritingPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.3-flash",
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(time.Hour),
	}, found: true}
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	svc := NewService(
		fr,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"z-ai/glm-5.2",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	key := DeriveSessionKey(env, "key-1")
	headerReq, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	headerReq.Header.Set(ForceModelHeader, "opus")
	_, _, forceErr := svc.applyForceModelHeader(context.Background(), headerReq, uuid.New(), key)
	require.NoError(t, forceErr)

	feats := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", headerReq.Header, router.Request{
		RequestedModel: feats.Model,
	})
	require.NoError(t, err)

	assert.Equal(t, "z-ai/glm-5.2", res.Decision.Model,
		"the x-weave-force-model pin must outrank the automatic compaction hard-pin")
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.False(t, res.HardPinned)
}

// Guards the empty-pool escape hatch: with no in-tier candidate, the helper
// must return ok=false rather than hand the scorer an empty pool.
func TestRestrictToTier_FallsBackWhenNoInTierCandidate(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithAvailableModels(map[string]struct{}{"z-ai/glm-5.3-flash": {}})

	excluded := map[string]struct{}{"deepseek/deepseek-v4-pro": {}}
	out, ok := svc.restrictToTier(excluded, catalog.TierHigh)
	assert.False(t, ok, "no high-tier model is available, so the constraint must not apply")
	assert.Equal(t, excluded, out, "the original denylist is returned unchanged on fallback")
}

// Every available model outside the target tier gets excluded; in-tier
// models stay eligible.
func TestRestrictToTier_ExcludesOtherTiers(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithAvailableModels(map[string]struct{}{
			"moonshotai/kimi-k3":            {}, // high
			"z-ai/glm-5.3-flash":           {}, // low
			"deepseek/deepseek-v4.1-flash": {}, // mid
		})

	out, ok := svc.restrictToTier(nil, catalog.TierHigh)
	require.True(t, ok)
	_, haikuExcluded := out["z-ai/glm-5.3-flash"]
	_, sonnetExcluded := out["deepseek/deepseek-v4.1-flash"]
	_, opusExcluded := out["moonshotai/kimi-k3"]
	assert.True(t, haikuExcluded, "low-tier excluded")
	assert.True(t, sonnetExcluded, "mid-tier excluded")
	assert.False(t, opusExcluded, "high-tier stays eligible")
}
