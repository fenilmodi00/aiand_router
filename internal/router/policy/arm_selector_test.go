package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func newSelectorAdapter(result policy.Result) *policy.SidecarRouter {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.2"),
		set(providers.ProviderAIAND),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	return policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, &recordingPolicy{result: result}, resolver)
}

func classifierOnlyResult() policy.Result {
	return policy.Result{
		SchemaVersion:      policy.SchemaVersionV4,
		RouteID:            "route-classifier",
		Score:              0.8,
		PredictedLabel:     "maximum",
		ClassOrder:         []string{"maximum", "low"},
		ClassProbabilities: map[string]float64{"maximum": 0.8, "low": 0.2},
	}
}

func classifierFallback(group string) []policy.PreviewGroup {
	return []policy.PreviewGroup{{
		Group:        group,
		Probability:  0.8,
		RosterArms:   []string{"anthropic/moonshotai/kimi-k3", "anthropic/z-ai/glm-5.2"},
		EligibleArms: []string{"anthropic/moonshotai/kimi-k3", "anthropic/z-ai/glm-5.2"},
	}}
}

func TestArmSelectorPickIsServed(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	qualityBias := 0.2
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, "maximum", input.PredictedLabel)
		assert.ElementsMatch(t, []string{"anthropic/moonshotai/kimi-k3", "anthropic/z-ai/glm-5.2"}, input.CandidateRosterIDs)
		require.NotNil(t, input.QualityBias)
		assert.Equal(t, qualityBias, *input.QualityBias)
		return policy.SelectionPick{
			Group:          "maximum",
			Arm:            "anthropic/z-ai/glm-5.2",
			RankedFallback: classifierFallback("maximum"),
			Trace: policy.SelectionTrace{
				SelectedGroup: "maximum",
				SelectedArm:   "anthropic/z-ai/glm-5.2",
			},
			ArmScoresByGroup: map[string]map[string]float32{
				"maximum": {"anthropic/z-ai/glm-5.2": 42},
			},
		}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{
		RoutingKnobs: &router.Overrides{QualityBias: &qualityBias},
	})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.NotContains(t, decision.Reason, ":go_selection")
	assert.Contains(t, decision.Reason, "group=maximum,arm=anthropic/z-ai/glm-5.2")
	require.NotNil(t, decision.Metadata)
	assert.Equal(t, "anthropic/z-ai/glm-5.2", decision.Metadata.SelectedRosterArmID)
	assert.Equal(t, "maximum", decision.Metadata.PolicyGroup)
	assert.Equal(t, float32(42), decision.Metadata.ArmScores["anthropic/z-ai/glm-5.2"])
}

func TestArmSelectorCanonicalizesOpenCodeAlias(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, policy.HarnessOpenCode, input.Harness)
		return policy.SelectionPick{
			Group:          "maximum",
			Arm:            "anthropic/z-ai/glm-5.2",
			RankedFallback: classifierFallback("maximum"),
		}, nil
	})

	_, err := adapter.Route(context.Background(), router.Request{ClientApp: "open-code"})

	require.NoError(t, err)
}

func TestArmSelectorPreservesPiSubagentIdentity(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, "pi-subagent", input.Harness)
		return policy.SelectionPick{
			Group:          "maximum",
			Arm:            "anthropic/z-ai/glm-5.2",
			RankedFallback: classifierFallback("maximum"),
		}, nil
	})

	_, err := adapter.Route(context.Background(), router.Request{ClientApp: "pi-subagent"})

	require.NoError(t, err)
}

func TestArmSelectorErrorFailsTheTurn(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{}, errors.New("no eligible arm")
	})

	_, err := adapter.Route(context.Background(), router.Request{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selection unavailable",
		"a failed selection must surface as the strategy's unavailable sentinel, not a sidecar-picked arm")
}

func TestArmSelectorForceClusterExhaustionIsCallerError(t *testing.T) {
	result := classifierOnlyResult()
	adapter := newSelectorAdapter(result)
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, "low", input.ForcedGroup)
		return policy.SelectionPick{}, policy.ErrNoEligibleArm
	})

	_, err := adapter.Route(context.Background(), router.Request{ForceCluster: "low"})

	require.ErrorIs(t, err, policy.ErrForcedClusterUnservable)
	assert.NotContains(t, err.Error(), "selection unavailable")
	assert.Contains(t, err.Error(), "low")
}

func TestArmSelectorRejectsLegacySchema(t *testing.T) {
	result := classifierOnlyResult()
	result.SchemaVersion = policy.SchemaVersionV1
	result.Model = "moonshotai/kimi-k3"

	adapter := newSelectorAdapter(result)
	called := false
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		called = true
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/moonshotai/kimi-k3"}, nil
	})

	_, err := adapter.Route(context.Background(), router.Request{})

	require.Error(t, err)
	assert.False(t, called, "a legacy response must be rejected before selection runs")
	assert.Contains(t, err.Error(), policy.SchemaVersionV4)
}

func TestArmSelectorNegotiatesV4(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, &recordingPolicy{result: classifierOnlyResult()}, resolver)

	assert.Equal(t, policy.SchemaVersionV1, resolver.SchemaVersion())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/moonshotai/kimi-k3"}, nil
	})
	assert.Equal(t, policy.SchemaVersionV4, resolver.SchemaVersion())
}

func TestArmSelectorYieldsToClusterOverride(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "moonshotai/kimi-k3", RankedFallback: classifierFallback("maximum")}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{
		ClusterArmOverrides: map[string][]string{
			"maximum": {"z-ai/glm-5.2", "moonshotai/kimi-k3"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)
	assert.Contains(t, decision.Reason, ":cluster_override")
}

func TestArmSelectorSurvivesOverridesOmittingWinningGroup(t *testing.T) {
	adapter := newSelectorAdapter(classifierOnlyResult())
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/z-ai/glm-5.2"}, nil
	})

	// A partial per-key map that configures only an unrelated cluster must not
	// suppress Go selection for the served group.
	decision, err := adapter.Route(context.Background(), router.Request{
		ClusterArmOverrides: map[string][]string{
			"minimal": {"z-ai/glm-5.2"},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)
	assert.NotContains(t, decision.Reason, ":go_selection")
}

func TestArmSelectorForceClusterUsesFinalGroupScores(t *testing.T) {
	result := classifierOnlyResult()
	adapter := newSelectorAdapter(result)
	adapter.WithArmSelector(func(_ context.Context, _ policy.SelectionInput) (policy.SelectionPick, error) {
		return policy.SelectionPick{
			Group:          "maximum",
			Arm:            "anthropic/moonshotai/kimi-k3",
			RankedFallback: append(classifierFallback("maximum"), policy.PreviewGroup{Group: "low", Probability: 0.2, RosterArms: []string{"z-ai/glm-5.2"}, EligibleArms: []string{"z-ai/glm-5.2"}}),
			ArmScoresByGroup: map[string]map[string]float32{
				"maximum": {"moonshotai/kimi-k3": 90},
				"low":     {"z-ai/glm-5.2": 20},
			},
		}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{
		ForceCluster: "low",
		ClusterArmOverrides: map[string][]string{
			"low": {"z-ai/glm-5.2"},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	assert.Equal(t, "low", decision.Metadata.PolicyGroup)
	assert.Equal(t, map[string]float32{"z-ai/glm-5.2": 20}, decision.Metadata.ArmScores)
}

func TestArmSelectorForceClusterPreservesPreferenceRanking(t *testing.T) {
	result := classifierOnlyResult()
	adapter := newSelectorAdapter(result)
	adapter.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		assert.Equal(t, "low", input.ForcedGroup)
		return policy.SelectionPick{
			Group:          "low",
			Arm:            "anthropic/z-ai/glm-5.2",
			RankedFallback: []policy.PreviewGroup{{Group: "low", Probability: 0.2, RosterArms: []string{"anthropic/moonshotai/kimi-k3", "anthropic/z-ai/glm-5.2"}, EligibleArms: []string{"anthropic/moonshotai/kimi-k3", "anthropic/z-ai/glm-5.2"}}},
			ArmScoresByGroup: map[string]map[string]float32{
				"low": {"moonshotai/kimi-k3": 10, "z-ai/glm-5.2": 20},
			},
		}, nil
	})

	decision, err := adapter.Route(context.Background(), router.Request{ForceCluster: "low"})

	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.2", decision.Model)
	assert.Contains(t, decision.Reason, ":force_cluster")
	require.NotNil(t, decision.Metadata)
	assert.Equal(t, "low", decision.Metadata.PolicyGroup)
	assert.Equal(t, map[string]float32{
		"moonshotai/kimi-k3": 10,
		"z-ai/glm-5.2": 20,
	}, decision.Metadata.ArmScores)
}
