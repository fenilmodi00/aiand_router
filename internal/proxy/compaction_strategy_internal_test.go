package proxy

import (
	"context"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
)

// Ported from upstream 918601b9 (#1901). The upstream end-to-end Codex
// compaction tests (TestCodexCompactionAfterAutoReview,
// TestCodexCompactionDispatchAcrossStrategies) were dropped: v0 serves only
// the aiand provider and lacks cluster bundle resolution for the legacy
// OpenAI models they exercise. The eligibility guard below is portable.

const compactionStrategyModel = "z-ai/glm-5.3"

func TestCompactionCrossStrategyHistoryHonorsEligibility(t *testing.T) {
	for _, test := range []struct {
		name      string
		req       router.Request
		available map[string]struct{}
	}{
		{name: "provider disabled", req: router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}}, ExcludedModels: map[string]struct{}{compactionStrategyModel: {}}}},
		{name: "model excluded", req: router.Request{ExcludedModels: map[string]struct{}{compactionStrategyModel: {}}}},
		{name: "model automatically disabled", req: router.Request{AutomaticExcludedModels: map[string]struct{}{compactionStrategyModel: {}}}},
		{name: "model unavailable", available: map[string]struct{}{"z-ai/other-model": {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &rolePinStore{byRole: map[string]sessionpin.Pin{
				hmmHistoryRole(sessionpin.DefaultRole): {
					Provider: providers.ProviderAIAND, LastServedModel: compactionStrategyModel,
					Strategy: router.StrategyHMMEmbedding, LastTurnEndedAt: time.Now(),
				},
			}}
			svc := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil},
				nil, false, nil, store, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil).
				WithAvailableModels(test.available)
			test.req.ClientApp = ClientAppCodex
			ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
			_, _, ok := svc.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, test.req)
			assert.False(t, ok)
		})
	}
}

func TestCompactionSessionModelUsesServedHistory(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {
			Provider: providers.ProviderAIAND, Model: compactionStrategyModel,
			LastServedModel: compactionStrategyModel,
			Strategy:        router.StrategyHMMEmbedding, LastTurnEndedAt: time.Now(),
		},
	}}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil},
		nil, false, nil, store, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil).
		WithAvailableModels(map[string]struct{}{compactionStrategyModel: {}})
	ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
	// An expired thread pin under a different strategy must not block the
	// served-history readmit (the #1901 fix).
	provider, model, ok := svc.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, router.Request{
		ClientApp:        ClientAppCodex,
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
	})
	assert.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, provider)
	assert.Equal(t, compactionStrategyModel, model)
}
