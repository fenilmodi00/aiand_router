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

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
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

func TestHMMCommandOnlyTurnUsesEligibleRosterFallback(t *testing.T) {
	const request = `{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>injected context</system-reminder>"},{"type":"text","text":"<command-name>local command</command-name>"},{"type":"text","text":"<local-command-stdout>local output</local-command-stdout>"}]}]}`
	env, err := translate.ParseAnthropic([]byte(request))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	require.False(t, hasTextUserBoundary(conversationMessagesForRouting(env)))

	sidecarFailure := errors.New("policy sidecar must not receive a command-only request")
	svc := unscorableHMMService(nil, sidecarFailure)
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	requestForRouting := router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		ConversationMessages: conversationMessagesForRouting(env),
	}
	turn, err := svc.runTurnLoop(ctx, env, features, "api-key", uuid.New(), "", http.Header{}, requestForRouting)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", turn.Decision.Model)
	assert.Equal(t, providers.ProviderAIAND, turn.Decision.Provider)
	assert.Equal(t, policy.UnscorableHMMDecisionReason, turn.Decision.Reason)
	assert.Equal(t, turn.Decision, turn.Fresh)

	// A real user message remains policy-scored, including a message carrying
	// both injected context and user-authored text.
	userRequest := router.Request{RequestedModel: features.Model, ConversationMessages: []router.ConversationMessage{{Role: "user", Text: "Please investigate"}}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, userRequest)
	require.ErrorIs(t, err, sidecarFailure)
}

func TestHMMCommandOnlyTurnUsesAdmittedRuntimeFallback(t *testing.T) {
	const requestBody = `{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"<command-name>local command</command-name>"}]}]}`
	env, err := translate.ParseAnthropic([]byte(requestBody))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	require.False(t, hasTextUserBoundary(conversationMessagesForRouting(env)))

	innerService := unscorableHMMService(nil, errors.New("policy sidecar must not receive a command-only request"))
	innerRouter := innerService.strategies[router.StrategyHMMEmbedding].router
	snapshot := &policyregistry.Snapshot{Routers: map[router.Strategy]router.Router{
		router.StrategyHMMEmbedding: innerRouter,
	}}
	admittedRouter := policyregistry.NewAdmittedRouter(router.StrategyHMMEmbedding, snapshot)
	service := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil,
		nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: admittedRouter})
	ctx := policyregistry.WithServingSnapshot(
		router.WithStrategy(context.Background(), router.StrategyHMMEmbedding),
		snapshot,
	)
	requestForRouting := router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		ConversationMessages: conversationMessagesForRouting(env),
	}

	decision, err := service.routeWithStrategy(ctx, router.StrategyHMMEmbedding, requestForRouting)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", decision.Model)
	assert.Equal(t, policy.UnscorableHMMDecisionReason, decision.Reason)
}

func TestHMMCommandOnlyTurnHonorsEligibilityAndPolicyPin(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("sidecar unavailable"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	request := router.Request{RequestedModel: "z-ai/glm-5.2", ConversationMessages: []router.ConversationMessage{}}

	request.ExcludedModels = map[string]struct{}{"z-ai/glm-5.2": {}}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", decision.Model)

	request.ExcludedModels = nil
	request.EnabledProviders = map[string]struct{}{providers.ProviderAIAND: {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.ErrorIs(t, err, policy.ErrNoRoutableModels)

	request.EnabledProviders = nil
	pin := router.PolicyPin{ArtifactSHA256: "artifact", RosterSHA256: "roster"}
	ctx = router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Authorized: true, Pin: pin})
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, request)
	require.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}

func TestHMMCommandOnlyTurnUsesPromotedRosterAndAutomaticExclusions(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{RequestedModel: "z-ai/glm-5.2", ConversationMessages: []router.ConversationMessage{}}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", decision.Model, "a catalog price does not admit a retired model to the serving roster")

	req.RequestedModel = "z-ai/glm-5.2"
	req.AutomaticExcludedModels = map[string]struct{}{"z-ai/glm-5.2": {}}
	decision, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.NotEqual(t, "z-ai/glm-5.2", decision.Model)
}

func TestHMMCommandOnlyEscalationRespectsSessionFloor(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{
		RequestedModel:       "z-ai/glm-5.3-flash",
		ConversationMessages: []router.ConversationMessage{},
		PreviousPolicyGroup:  escalation.High,
		Escalation:           &escalation.Constraint{Floor: escalation.Medium},
	}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)

	req.ExcludedModels = map[string]struct{}{"z-ai/glm-5.2": {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrNoEligibleArm)
	require.ErrorIs(t, err, hmm.ErrHMMUnavailable)
	classified, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classified.Status)

	req.ExcludedModels = nil
	req.Escalation = &escalation.Constraint{Escalate: true}
	decision, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)
}

func TestHMMCommandOnlyTurnHonorsForcedClusterAndKeyList(t *testing.T) {
	svc := unscorableHMMService(nil, errors.New("must not call sidecar"))
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	req := router.Request{
		RequestedModel:       "z-ai/glm-5.3-flash",
		ConversationMessages: []router.ConversationMessage{},
		ForceCluster:         "maximum",
		ClusterArmOverrides:  map[string][]string{"maximum": {"z-ai/glm-5.2"}},
	}
	decision, err := svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)

	req.ClusterArmOverrides["maximum"] = []string{"z-ai/glm-5.2"}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched := ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusBadRequest, classified.Status)

	req.ClusterArmOverrides = nil
	req.ForceCluster = "high"
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched = ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusBadRequest, classified.Status)

	req.ForceCluster = "maximum"
	req.ClusterArmOverrides = map[string][]string{"maximum": {"z-ai/glm-5.2"}}
	req.ExcludedModels = map[string]struct{}{"z-ai/glm-5.3-flash": {}, "deepseek/deepseek-v4.1-flash": {}, "z-ai/glm-5.2": {}}
	_, err = svc.routeWithStrategy(ctx, router.StrategyHMMEmbedding, req)
	require.ErrorIs(t, err, hmm.ErrHMMUnavailable)
	require.NotErrorIs(t, err, policy.ErrForcedClusterUnservable)
	classified, matched = ClassifyDispatchError(err)
	require.True(t, matched)
	assert.Equal(t, http.StatusServiceUnavailable, classified.Status)
}

func TestHMMCommandOnlyTurnKeepsEligibleSessionPin(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"<command-name>local command</command-name>"}]}]}`))
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
