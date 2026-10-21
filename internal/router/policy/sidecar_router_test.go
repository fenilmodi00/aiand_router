package policy_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type recordingPolicy struct {
	query        policy.Query
	previewQuery policy.Query
	result       policy.Result
	preview      policy.PreviewResult
	outcome      map[string]interface{}
	feedback     map[string]interface{}
}

func (p *recordingPolicy) Decide(_ context.Context, query policy.Query) (policy.Result, error) {
	p.query = query
	return p.result, nil
}

func (p *recordingPolicy) Preview(_ context.Context, query policy.Query) (policy.PreviewResult, error) {
	p.previewQuery = query
	return p.preview, nil
}

func (p *recordingPolicy) ReportOutcome(_ context.Context, payload map[string]interface{}) error {
	p.outcome = payload
	return nil
}

func (p *recordingPolicy) ReportFeedback(_ context.Context, payload map[string]interface{}) error {
	p.feedback = payload
	return nil
}

func TestSidecarRouterPreviewRejectsUnknownArm(t *testing.T) {
	decider := &recordingPolicy{preview: policy.PreviewResult{
		SchemaVersion:         policy.SchemaVersionV1,
		PolicyArtifactID:      "hmm-prod",
		PolicyArtifactSHA256:  "sha256:artifact",
		RosterSHA256:          "sha256:roster",
		HMMStateID:            1,
		HMMStatePath:          []int{1},
		HMMStateProbabilities: []float64{0.2, 0.8},
		ClassOrder:            []string{"hard"},
		ClassProbabilities:    map[string]float64{"hard": 1},
		RankedFallback: []policy.PreviewGroup{{
			Group:        "hard",
			Probability:  1,
			RosterArms:   []string{"unoffered-model"},
			EligibleArms: []string{"unoffered-model"},
		}},
		SelectedGroup:     "hard",
		EligibleRosterIDs: []string{"unoffered-model"},
	}}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy: router.StrategyHMM,
	}, decider, policy.NewResolver(
		set("gpt-5.5"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	))

	_, err := adapter.PreviewRoute(context.Background(), router.Request{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown roster id")
}

func TestSidecarRouterCapabilitiesRejectUnsupportedShadow(t *testing.T) {
	unavailable := errors.New("future unavailable")
	decider := &recordingPolicy{}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.Strategy("future-policy"),
		Unavailable: unavailable,
	}, decider, nil).WithCapabilities(policy.Capabilities{})

	_, err := adapter.Route(context.Background(), router.Request{ShadowMode: true})

	require.ErrorIs(t, err, unavailable)
	assert.Empty(t, decider.query.ExecutionMode)
}

func TestSidecarRouterCapabilitiesGateOptionalLifecycleCalls(t *testing.T) {
	decider := &recordingPolicy{}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy: router.Strategy("future-policy"),
	}, decider, nil).WithCapabilities(policy.Capabilities{})

	require.NoError(t, adapter.ReportOutcome(context.Background(), map[string]interface{}{"route_id": "route-1"}))
	require.NoError(t, adapter.ReportFeedback(context.Background(), map[string]interface{}{"feedback": "positive"}))
	assert.Nil(t, decider.outcome)
	assert.Nil(t, decider.feedback)
}

func TestSidecarRouterCapabilitiesCanRefreshAfterStartup(t *testing.T) {
	decider := &recordingPolicy{}
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy: router.Strategy("future-policy"),
	}, decider, nil).WithCapabilities(policy.Capabilities{})

	adapter.WithCapabilities(policy.Capabilities{
		SchemaVersion:   policy.SchemaVersionV1,
		ReportsOutcomes: true,
		ReportsFeedback: true,
		SupportsShadow:  true,
	})

	require.NoError(t, adapter.ReportOutcome(context.Background(), map[string]interface{}{"route_id": "route-2"}))
	require.NoError(t, adapter.ReportFeedback(context.Background(), map[string]interface{}{"feedback": "positive"}))
	assert.Equal(t, "route-2", decider.outcome["route_id"])
	assert.Equal(t, "positive", decider.feedback["feedback"])
	assert.True(t, adapter.CurrentCapabilities().SupportsShadow)
}

// clusterOverrideMapper mirrors the HMM roster aliasing closely enough for the
// override path: anthropic models map to "anthropic/<id>".
func clusterOverrideMapper(model catalog.Model) string { return "anthropic/" + model.ID }

// A force that coincides with the sidecar's own pick must not waive the
// provider-agreement check: nothing was re-selected, so a mismatched provider
// still means the sidecar and resolver disagree.
func TestSidecarRouterForcedClusterKeepsProviderAgreementCheck(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		clusterOverrideMapper,
		policy.ManagedProviderPolicy(),
	)
	decider := &recordingPolicy{result: policy.Result{
		SchemaVersion: policy.SchemaVersionV1,
		Model:         "anthropic/claude-opus-4-8",
		Provider:      providers.ProviderAIAND, // disagrees with the resolved binding
		RankedFallback: []policy.PreviewGroup{{
			Group:        "maximum",
			EligibleArms: []string{"anthropic/claude-opus-4-8"},
		}},
	}}
	unavailable := errors.New("hmm unavailable")
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: unavailable,
	}, decider, resolver)

	_, err := adapter.Route(context.Background(), router.Request{ForceCluster: "maximum"})

	require.ErrorIs(t, err, unavailable,
		"an unchanged selection must still be validated against the sidecar's provider")
}

func TestSidecarRouterCapabilityRefreshIsConcurrentSafe(t *testing.T) {
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy: router.Strategy("future-policy"),
	}, &recordingPolicy{}, nil).WithCapabilities(policy.Capabilities{})
	var workers sync.WaitGroup

	for i := 0; i < 100; i++ {
		workers.Add(2)
		go func(supportsShadow bool) {
			defer workers.Done()
			adapter.WithCapabilities(policy.Capabilities{SupportsShadow: supportsShadow})
		}(i%2 == 0)
		go func() {
			defer workers.Done()
			_ = adapter.CurrentCapabilities()
		}()
	}

	workers.Wait()
}
