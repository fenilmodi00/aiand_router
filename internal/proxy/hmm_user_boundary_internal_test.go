package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/hmm"
	"aiand-os/router/internal/router/hmm/rosterdata"
	"aiand-os/router/internal/router/hmm/selection"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"
)

type unavailableHMMDecider struct{ err error }

func (d unavailableHMMDecider) Decide(context.Context, policy.Query) (policy.Result, error) {
	return policy.Result{}, d.err
}

func unscorableHMMService(store sessionpin.Store, sidecarFailure error) *Service {
	roster := &rosterdata.Roster{
		ClassOrder: []string{"low", "medium", "high", "maximum"},
		Clusters: map[string]rosterdata.Cluster{
			"low":     {Arms: []string{"anthropic/claude-haiku-4.5"}},
			"medium":  {Arms: []string{"anthropic/claude-sonnet-4.6"}},
			"maximum": {Arms: []string{"anthropic/claude-opus-5.5"}},
		},
	}
	policyRouter := hmm.NewForStrategy(router.StrategyHMMEmbedding, unavailableHMMDecider{sidecarFailure},
		map[string]struct{}{providers.ProviderAIAND: {}})
	policyRouter.WithArmSelector(selection.Selector(roster))
	return NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil,
		store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: policyRouter})
}

func TestHMMCommandOnlyTurnKeepsEligibleSessionPin(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":[{"type":"text","text":"<command-name>local command</command-name>"}]}]}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "deepseek/deepseek-v4.1-flash",
		Reason:      "hmm_policy",
		PinnedUntil: time.Now().Add(time.Hour),
	}
	svc := unscorableHMMService(store, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	request := router.Request{RequestedModel: features.Model, ConversationMessages: conversationMessagesForRouting(env)}
	turn, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, request)
	require.NoError(t, err)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", turn.Decision.Model)
	assert.Equal(t, unscorableHMMStickyReason, turn.Decision.Reason)
	assert.True(t, turn.StickyHit)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "hmm_policy", store.upserts[len(store.upserts)-1].Reason)
}
