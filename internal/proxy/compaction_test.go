package proxy

import (
	"context"
	"testing"
	"time"
	"weave-os/router/internal/dispatch"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrecompactionPolicyReviewsEveryCascadeCandidate(t *testing.T) {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	require.True(t, ok)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionDefaultModel)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionLargeWindowModel)
	// Every reviewed summarizer must be servable on the AIand-only roster at a
	// non-low tier: a warm session pin resolves onto one of these.
	for _, m := range spec.FixedCatalogModels {
		model, known := catalog.ByID(m)
		require.True(t, known, "summarizer %s must be a catalog model", m)
		assert.NotEqual(t, catalog.TierLow, model.Tier, "summarizer %s must not be low-tier", m)
		aiand := false
		for _, b := range model.Providers {
			aiand = aiand || b.Provider == providers.ProviderAIAND
		}
		assert.True(t, aiand, "summarizer %s must carry an AIand binding", m)
	}
}

func TestCompactionHardPin(t *testing.T) {
	s := &Service{compactionHardPinEnabled: true}
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()

	p, m, source, ok := s.compactionHardPin(ctx, key, "", router.Request{})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, p)
	assert.Equal(t, policy.PrecompactionDefaultModel, m, "no pin → the deployment's mid/high-tier summarizer")
	assert.Equal(t, policy.OverrideSourceDeployment, source, "the deployment's compaction model fixed the turn")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}})
	assert.False(t, ok, "AIand disabled for the tenant → fall back to generic hard-pin")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{GatewayProviders: map[string]struct{}{providers.ProviderAIAND: {}}})
	assert.False(t, ok, "gateway-exclusive tenant → fall back to generic hard-pin")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{ExcludedModels: map[string]struct{}{policy.PrecompactionDefaultModel: {}}})
	assert.False(t, ok, "excluded default with no pin → fall back to generic hard-pin")

	unavailable := &Service{compactionHardPinEnabled: true, availableModels: map[string]struct{}{"zai-org/glm-5.3-flash": {}}}
	_, _, _, ok = unavailable.compactionHardPin(ctx, key, "", router.Request{})
	assert.False(t, ok, "default not routable in this deployment → fall back to generic hard-pin")
}

// rolePinStore serves a distinct pin per role so the thread pin and the
// _hmm_history row can disagree.
type rolePinStore struct {
	stubPinStore
	byRole map[string]sessionpin.Pin
}

func (s *rolePinStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.byRole[role]
	return pin, found, nil
}

func TestCompactionHardPin_FamilyUpgradeHonorsRestrictions(t *testing.T) {
	// deepseek-v4-flash (tier Low, never a summarizer) and deepseek-v4.1-flash
	// (tier Mid) are one versioned family: a session pinned to the older member
	// upgrades onto the newer eligible one.
	const olderModel = "deepseek-ai/deepseek-v4-flash"
	const newerModel = "deepseek-ai/deepseek-v4.1-flash"
	ctx := context.Background()
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {
			Provider: providers.ProviderAIAND, LastServedModel: olderModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := &Service{pinStore: store, clients: dispatch.NewClients(map[string]providers.Client{providers.ProviderAIAND: nil})}
	for _, test := range []struct {
		name string
		req  router.Request
		want string
	}{
		{"upgrade", router.Request{}, newerModel},
		// The pinned member is tier-low, so it is never eligible itself: a
		// blocked upgrade leaves no session summarizer at all.
		{"org exclusion", router.Request{ExcludedModels: map[string]struct{}{newerModel: {}}}, ""},
		{"automatic exclusion", router.Request{AutomaticExcludedModels: map[string]struct{}{newerModel: {}}}, ""},
		{"provider disabled", router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, model, ok := s.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, test.req)
			assert.Equal(t, test.want != "", ok)
			assert.Equal(t, test.want, model)
		})
	}
	s.availableModels = map[string]struct{}{newerModel: {}}
	_, model, ok := s.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, router.Request{})
	require.True(t, ok)
	assert.Equal(t, newerModel, model)
}

func TestTurnLoop_CompactionReadsClientIdentityBeforeHardPin(t *testing.T) {
	const sessionModel = "deepseek-ai/deepseek-v4-pro"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierMid)): {
			Provider: providers.ProviderAIAND, LastServedModel: sessionModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil, store, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil)
	s.compactionHardPinEnabled = true
	env, err := translate.ParseOpenAI([]byte(`{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a summary."}],"max_tokens":4096}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	features := env.RoutingFeatures(false)
	turn, err := s.runTurnLoop(ctx, env, features, "test-key", uuid.Nil, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, providers.ProviderAIAND, turn.Decision.Provider)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", turn.Decision.Model)
}
