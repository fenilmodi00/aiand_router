package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/router"
)

// Ported from upstream 49be5bd2 (#1903); the streaming end-to-end test was
// dropped because v0 lacks planClient/stateModelLeaser/WithManagedSubscriptions.
func TestForcedSubscriptionContextEstimatePreservesHardExclusions(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		forced          bool
		estimate        bool
		unsignedHistory bool
		wantServed      bool
	}{
		{name: "forced estimate delegates to provider", forced: true, estimate: true, wantServed: true},
		{name: "automatic estimate still excludes", estimate: true},
		{name: "explicit policy exclusion still excludes", forced: true},
		{name: "unsigned history still excludes", forced: true, estimate: true, unsignedHistory: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			request := router.Request{
				Exclusions: router.Exclusions{}.
					ExcludeModel(testOpus, router.ExclusionRequested).
					ExcludeModel(testOpus, router.ExclusionSafety),
			}
			if scenario.forced {
				request.ForceModel = testOpus
			}
			if scenario.estimate {
				request.Exclusions = request.Exclusions.ExcludeModel(testOpus, router.ExclusionContextWindow)
			}
			if scenario.unsignedHistory {
				request.Exclusions = request.Exclusions.ExcludeModel(testOpus, router.ExclusionUnsignedHistory)
			}
			_, narrowed := (&Service{}).subscriptionStateRequest(context.Background(), request, []string{testOpus})
			_, allowed := narrowed.AllowedModels[testOpus]
			assert.Equal(t, scenario.wantServed, allowed)
			assert.True(t, request.Exclusions.Excludes(testOpus), "state routing must not mutate the original request")
			assert.True(t, request.Exclusions.SafetyExcludes(testOpus))
		})
	}
}

func TestForcedContextEstimateReadmitsForcedPin(t *testing.T) {
	request := router.Exclusions{}.
		ExcludeModel(testOpus, router.ExclusionRequested).
		ExcludeModel(testOpus, router.ExclusionSafety).
		ExcludeModel(testOpus, router.ExclusionContextWindow).
		ExcludeModel(testOpus, router.ExclusionUnsignedHistory)
	req := router.Request{
		ForceModel: testOpus,
		Exclusions: request,
	}
	readmitted := readmitForcedContextEstimate(req, req.ForceModel)
	assert.False(t, readmitted.Exclusions.Excludes(testOpus))
	assert.False(t, readmitted.Exclusions.SafetyExcludes(testOpus))
	assert.True(t, readmitted.Exclusions.ContextWindowExcludes(testOpus), "the estimate marker itself is preserved")
	assert.True(t, readmitted.Exclusions.UnsignedHistoryExcludes(testOpus), "unsigned-history exclusion still holds")
	// An automatic estimate for a model the request does not force stays excluded.
	assert.True(t, readmitForcedContextEstimate(req, "other-model").Exclusions.Excludes(testOpus))
	// An explicit (non-estimate) exclusion is never lifted.
	explicit := router.Request{ForceModel: testOpus, Exclusions: router.Exclusions{}.ExcludeModel(testOpus, router.ExclusionRequested)}
	assert.True(t, readmitForcedContextEstimate(explicit, testOpus).Exclusions.Excludes(testOpus))
}
