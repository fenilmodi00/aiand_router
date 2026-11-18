package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/planner"
	"aiand-os/router/internal/router/sessionpin"
)

func newHMMStayPinRegressionService() *Service {
	return NewService(
		nil,
		map[string]providers.Client{providers.ProviderAIAND: nil},
		nil,
		false,
		nil,
		nil,
		false,
		"anthropic", catalog.ModelIDGLM53Flash.String(),
		nil,
	)
}

func hmmStayPinRegressionFresh(reason string) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    catalog.ModelIDDeepSeekV41Flash.String(),
		Reason:   reason,
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.70,
		},
	}
}

func TestHMMCostGate_EffortQualifiedHistoryStaysOnWarmCache(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		LastServedModel: catalog.ModelIDDeepSeekV4Pro.String() + ":high",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		hmmStayPinRegressionFresh("hmm_policy(classifier 'fast')"),
		100,
		false,
	)

	assert.True(t, sticky, "an effort-qualified served identity must stay usable as the HMM stay pin")
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), decision.Model)
	assert.Equal(t, "high", decision.Effort, "staying must keep the effort the session was served at")
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String()+":high", decision.ServedIdentity())
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
}

func TestHMMCostGate_EffortQualifiedHistoryIsPricedByEVGate(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		LastServedModel: catalog.ModelIDDeepSeekV4Pro.String() + ":high",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		hmmStayPinRegressionFresh("hmm_policy(classifier 'fast')"),
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, catalog.ModelIDDeepSeekV41Flash.String(), decision.Model)
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), stayModel)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, plan.Reason, "the switch must come from the EV gate, not a missing pin")
}

func TestHMMCostGate_PhaseChangeStaysWhenEVDoesNotCoverWarmCache(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		LastServedModel: catalog.ModelIDDeepSeekV4Pro.String(),
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	decision, plan, sticky, stayModel := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		hmmStayPinRegressionFresh("hmm_policy(classifier 'balanced')"),
		100,
		false,
	)

	assert.True(t, sticky, "a phase change must not evict a warm cache the EV gate says is worth keeping")
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), decision.Model)
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), stayModel)
	assert.Equal(t, planner.OutcomeStay, plan.Outcome)
	assert.Equal(t, planner.ReasonEVNegative, plan.Reason)
	assert.True(t, plan.ShadowComputed, "phase changes must be priced by the planner")
}

func TestHMMCostGate_PhaseChangeSwitchRecordsEvictionCost(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		LastServedModel: catalog.ModelIDDeepSeekV4Pro.String(),
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}

	decision, plan, sticky, _ := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		hmmStayPinRegressionFresh("hmm_policy(classifier 'balanced')"),
		10_000,
		false,
	)

	assert.False(t, sticky)
	assert.Equal(t, catalog.ModelIDDeepSeekV41Flash.String(), decision.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonPhaseChange, plan.Reason)
	assert.Positive(t, plan.EvictionCostUSD, "a warm-cache phase-change switch must record the eviction it pays")
}

func TestHMMCostGate_PhaseChangeUpgradeFollowsFreshAtLowConfidence(t *testing.T) {
	svc := newHMMStayPinRegressionService()
	history := sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		LastServedModel: catalog.ModelIDGLM53Flash.String(),
		Reason:          "hmm_policy:tool_execution(label=explore)",
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
		PinnedUntil:     time.Now().Add(time.Hour),
	}
	fresh := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    catalog.ModelIDDeepSeekV4Pro.String(),
		Reason:   "hmm_policy(classifier 'high')",
		Metadata: &router.RoutingMetadata{
			Strategy:    string(router.StrategyHMM),
			RouteID:     "route-1",
			ChosenScore: 0.30,
		},
	}

	decision, plan, sticky, _ := svc.hmmCostGatedDecision(
		router.Request{},
		sessionpin.Pin{},
		history,
		fresh,
		10_000,
		false,
	)

	assert.False(t, sticky, "leaving tool execution must not trap the main loop on the cheaper explore model")
	assert.Equal(t, catalog.ModelIDDeepSeekV4Pro.String(), decision.Model)
	assert.Equal(t, planner.OutcomeSwitch, plan.Outcome)
	assert.Equal(t, hmmReasonPhaseChange, plan.Reason)
	assert.True(t, plan.ShadowComputed, "the switch still records the planner's cost evidence")
}
