package proxy

import (
	"context"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrecompactionPolicyReviewsEveryCascadeCandidate(t *testing.T) {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	require.True(t, ok)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionDefaultModel)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionLargeWindowModel)
	for _, id := range spec.FixedCatalogModels {
		m, known := catalog.ByID(id)
		assert.True(t, known, "reviewed summarizer %s must be a catalog row", id)
		if !known {
			continue
		}
		assert.NotEqual(t, catalog.TierLow, m.Tier, "a low-tier model can never be selected by the cascade")
		served := false
		for _, b := range m.Providers {
			served = served || b.Provider == providers.ProviderAIAND
		}
		assert.True(t, served, "reviewed summarizer %s must have an aiand binding", id)
	}
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

func TestCompactionSessionDecision(t *testing.T) {
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	openAIAndAnthropic := map[string]struct{}{providers.ProviderAIAND: {}}
	history := func(pin sessionpin.Pin) *rolePinStore {
		return &rolePinStore{byRole: map[string]sessionpin.Pin{hmmHistoryRole(sessionpin.DefaultRole): pin}}
	}
	solHistory := history(sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), PinnedUntil: live})
	switched := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 {Provider: providers.ProviderAIAND, Model: "moonshotai/kimi-k3", LastServedModel: "moonshotai/kimi-k3", LastTurnEndedAt: time.Now().Add(-time.Minute), PinnedUntil: live},
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderAIAND, LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	expired := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3", LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(-time.Hour)},
	}}
	anthropicPinned := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3", LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	solFamily := map[string]struct{}{"z-ai/glm-5.3": {}}
	for _, test := range []struct {
		name         string
		store        *rolePinStore
		available    map[string]struct{}
		req          router.Request
		wantModel    string
		wantEffort   string
		wantProvider string
	}{
		{name: "provider struck out this session", store: history(sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), DisabledProviders: []string{providers.ProviderAIAND}})},
		{name: "model demoted this session", store: history(sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: "z-ai/glm-5.3", LastTurnEndedAt: time.Now(), DemotedModels: []string{"z-ai/glm-5.3"}})},
		{name: "served model, not a newer family member", store: solHistory, wantModel: "z-ai/glm-5.3", wantProvider: providers.ProviderAIAND},
		{name: "latest served wins", store: switched, wantModel: "z-ai/glm-5.3", wantProvider: providers.ProviderAIAND},
		{name: "expired thread pin", store: expired},
		{name: "anthropic session", store: anthropicPinned, wantModel: "z-ai/glm-5.3", wantProvider: providers.ProviderAIAND},
		{name: "low-tier session", store: history(sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: "deepseek/deepseek-v4.1-flash-1017", LastTurnEndedAt: time.Now()}), wantModel: "deepseek/deepseek-v4.1-flash-1017", wantProvider: providers.ProviderAIAND},
		{name: "effort qualified", store: history(sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: "z-ai/glm-5.3:xhigh", LastTurnEndedAt: time.Now()}), wantModel: "z-ai/glm-5.3", wantEffort: "xhigh", wantProvider: providers.ProviderAIAND},
		{name: "provider disabled", store: solHistory, req: router.Request{EnabledProviders: map[string]struct{}{"other-upstream": {}}}},
		{name: "org exclusion", store: solHistory, req: router.Request{Exclusions: router.RequestedExclusions(solFamily)}},
		{name: "automatic exclusion", store: solHistory, req: router.Request{AutomaticExcludedModels: solFamily}},
		{name: "unavailable in deployment", store: solHistory, available: map[string]struct{}{"moonshotai/kimi-k3": {}}},
		{name: "transcript past the window", store: solHistory, req: router.Request{EstimatedInputTokens: catalog.ContextWindowForBinding("z-ai/glm-5.3", providers.ProviderAIAND)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := compactionSession{pins: test.store, providers: openAIAndAnthropic, available: test.available, now: time.Now()}
			decision, _, ok := session.decision(ctx, key, sessionpin.DefaultRole, test.req)
			assert.Equal(t, test.wantModel != "", ok)
			assert.Equal(t, test.wantModel, decision.Model)
			assert.Equal(t, test.wantEffort, decision.Effort)
			assert.Equal(t, test.wantProvider, decision.Provider)
		})
	}
}

func TestTurnLoop_CompactionReadsClientIdentityBeforeSessionPin(t *testing.T) {
	const sessionModel = "z-ai/glm-5.3"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierHigh)): {
			Provider: providers.ProviderAIAND, LastServedModel: sessionModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil, store, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil)
	env, err := translate.ParseOpenAI([]byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a summary."}],"max_tokens":4096}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	features := env.RoutingFeatures(false)
	turn, err := s.runTurnLoop(ctx, env, features, "test-key", uuid.Nil, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, providers.ProviderAIAND, turn.Decision.Provider)
	assert.Equal(t, sessionModel, turn.Decision.Model)
}

func TestSessionStrikesWithdrawStruckArmsFromScoring(t *testing.T) {
	strikes := sessionStrikes{disabledProviders: []string{"other-upstream"}, demotedModels: []string{"z-ai/glm-5.3"}}
	req := strikes.apply(router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}, "other-upstream": {}}})
	assert.Equal(t, map[string]struct{}{providers.ProviderAIAND: {}}, req.EnabledProviders)
	assert.Contains(t, req.AutomaticExcludedModels, "z-ai/glm-5.3")
}
