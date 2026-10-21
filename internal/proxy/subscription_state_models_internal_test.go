package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const (
	stateClaudeModel = "z-ai/glm-5.2"
	stateCodexModel  = "gpt-5.6-sol"
	statePaidModel   = "deepseek/deepseek-v4-flash"
)

type stateModelRouter struct{}

func (stateModelRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	resolver := policy.NewResolver(modelSet([]string{stateClaudeModel, stateCodexModel, statePaidModel}), nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{})
	eligible := modelSet(resolver.Resolve(req).CandidateModels())
	for _, model := range append(slices.Clone(req.PreferredModels), stateClaudeModel, stateCodexModel, statePaidModel) {
		if _, admitted := eligible[model]; admitted {
			entry, _ := catalog.ByID(model)
			for _, binding := range entry.Providers {
				if _, enabled := req.EnabledProviders[binding.Provider]; enabled {
					return router.Decision{Model: model, Provider: binding.Provider}, nil
				}
			}
		}
	}
	return router.Decision{}, cluster.ErrAllowlistEmptiesPool
}

type stateModelLeaser struct {
	scriptedSubscriptionLeaser
	activeProviders map[subscriptions.Provider]bool
}

func (l *stateModelLeaser) Lease(_ context.Context, owner auth.SubscriptionOwner, provider subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	account := "synthetic-" + string(provider)
	for _, excluded := range owner.ExcludedAccountIDs {
		if excluded == account {
			return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
		}
	}
	if !l.activeProviders[provider] {
		return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
	}
	return subscriptions.Lease{AccountID: account, AccessToken: "synthetic-token", ProviderAccount: "synthetic-workspace", OwnerID: "synthetic-owner"}, true, nil
}

func TestSubscriptionStateModelsIntersectGlobalAllowlist(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{stateCodexModel})
	require.Equal(t, map[string]struct{}{stateCodexModel: {}}, allowedModelsForRequest(ctx))
	svc := &Service{}
	_, paid := svc.subscriptionStateRequest(ctx, router.Request{AllowedModels: allowedModelsForRequest(ctx)}, []string{statePaidModel})
	require.Empty(t, paid.AllowedModels, "a state must never widen the global allowlist")
}

func TestSubscriptionStateFailedTargetReachesRecoveryCallback(t *testing.T) {
	paidClient := &fakeClient{name: providers.ProviderAIAND}
	svc := NewService(stateModelRouter{}, map[string]providers.Client{providers.ProviderAIAND: paidClient}, nil, false, nil, nil, false, "", "", nil)
	ctx := context.WithValue(context.Background(), InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	request := router.Request{
		AllowedModels:    allowedModelsForRequest(ctx),
		EnabledProviders: modelSet([]string{providers.ProviderAIAND, providers.ProviderAIAND, providers.ProviderAIAND}),
	}
	var recoveryDecision router.Decision
	var attemptedModel string
	winnerProvider := new(string)
	_, err := svc.dispatchWithFallback(ctx, failoverInputs{
		w:                               httptest.NewRecorder(),
		subscriptionStateRequest:        &request,
		subscriptionStateWinnerProvider: winnerProvider,
		initialDecision:                 router.Decision{Model: stateClaudeModel, Provider: providers.ProviderAIAND},
		purpose:                         inference.PurposeAnthropicMessages,
		buildAlternative: func(router.Decision) (dispatchAttempt, error) {
			return func(_ context.Context, attemptedTarget router.Decision, _ providers.Client) error {
				attemptedModel = attemptedTarget.Model
				return &providers.UpstreamStatusError{Status: http.StatusBadRequest}
			}, nil
		},
		onSubscriptionStateTarget: func(target router.Decision, _ []catalog.ProviderBinding) {
			recoveryDecision = target
		},
	})
	require.Error(t, err)
	require.Empty(t, *winnerProvider, "a failed target must not be recorded as the provider that served the request")
	require.Equal(t, statePaidModel, recoveryDecision.Model)
	require.Equal(t, statePaidModel, attemptedModel)
}

func TestSubscriptionStateModelsProtectPaidSummaries(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)
	fake := &fakeHandoverProvider{respBody: canonicalAnthropicResponse, respStatus: http.StatusOK}
	summarizer := newTestSummarizer(t, fake, "", time.Second)
	ctx := conditionalModelsContext([]string{policy.HandoverSummaryDefaultModel}, []string{statePaidModel})
	_, _, err = summarizer.Summarize(ctx, env, router.Request{})
	require.Error(t, err)
	require.Zero(t, fake.calls)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{policy.HandoverSummaryDefaultModel})
	text, _, err := summarizer.Summarize(ctx, env, router.Request{})
	require.NoError(t, err)
	require.Equal(t, "Refactor in progress: step 1 done, step 2 pending.", text)
	require.Equal(t, 1, fake.calls)
}

func TestSubscriptionStateModelsEmptySelectedStateFailsClosed(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel}, nil)
	ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(nil))
	allowed := allowedModelsForRequest(ctx)
	require.NotNil(t, allowed, "nil would mean unrestricted routing")
	require.Empty(t, allowed)
	require.False(t, modelPermittedByAllowlist(ctx, stateClaudeModel))
}

func TestSubscriptionStatePaidRescueRequiresExhaustedSetTarget(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	require.True(t, paidFallbackForbidden(ctx))
	require.False(t, paidFallbackForbiddenForModel(ctx, statePaidModel))
	require.True(t, paidFallbackForbiddenForModel(ctx, stateClaudeModel))
	require.Equal(t, []router.Decision{{Model: statePaidModel}}, subscriptionStatePaidRescueDecisions(ctx, []router.Decision{
		{Model: stateClaudeModel}, {Model: statePaidModel}, {Model: stateCodexModel},
	}))

	allowedRescueCtx := subscriptionStatePaidRescueContext(ctx, statePaidModel)
	require.False(t, paidFallbackForbidden(allowedRescueCtx))
	require.True(t, paidFallbackForbidden(subscriptionStatePaidRescueContext(ctx, stateClaudeModel)))
}

func TestSubscriptionStateModelsKeepSafetyExclusions(t *testing.T) {
	ctx := conditionalModelsContext([]string{stateClaudeModel, stateCodexModel}, []string{statePaidModel})
	request := router.Request{
		AllowedModels:                 allowedModelsForRequest(ctx),
		SafetyExcludedModels:          map[string]struct{}{stateClaudeModel: {}},
		UnsignedHistoryExcludedModels: map[string]struct{}{stateCodexModel: {}},
	}
	_, narrowed := (&Service{}).subscriptionStateRequest(ctx, request, []string{stateClaudeModel, stateCodexModel})
	require.Empty(t, narrowed.AllowedModels)
}

func TestSubscriptionStateModelsRespectManagedBoostAllowlist(t *testing.T) {
	ctx := boostPlanOwnedContext()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{stateClaudeModel, stateCodexModel})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	require.Equal(t, map[string]struct{}{stateCodexModel: {}}, allowedModelsForRequest(ctx))
}

func TestSubscriptionStateModelsEnabledByFlagWithEmptySets(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationSubscriptionStateModelsEnabledContextKey{}, true)

	assert.True(t, subscriptionStateModelsEnabled(ctx))
	assert.Empty(t, installationAllowedModelSet(context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(nil))))
}
