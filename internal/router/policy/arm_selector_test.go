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
		set("claude-opus-4-8", "claude-sonnet-5"),
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
		RosterArms:   []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
		EligibleArms: []string{"anthropic/claude-opus-4-8", "anthropic/claude-sonnet-5"},
	}}
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

func TestArmSelectorNegotiatesV4(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
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
		return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-opus-4-8"}, nil
	})
	assert.Equal(t, policy.SchemaVersionV4, resolver.SchemaVersion())
}
