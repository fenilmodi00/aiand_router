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
				ExcludedModels:       modelSet([]string{testOpus}),
				SafetyExcludedModels: modelSet([]string{testOpus}),
			}
			if scenario.forced {
				request.ForceModel = testOpus
			}
			if scenario.estimate {
				request.ContextWindowExcludedModels = modelSet([]string{testOpus})
			}
			if scenario.unsignedHistory {
				request.UnsignedHistoryExcludedModels = modelSet([]string{testOpus})
			}
			_, narrowed := (&Service{}).subscriptionStateRequest(context.Background(), request, []string{testOpus})
			_, allowed := narrowed.AllowedModels[testOpus]
			assert.Equal(t, scenario.wantServed, allowed)
			assert.Contains(t, request.ExcludedModels, testOpus, "state routing must not mutate the original request")
			assert.Contains(t, request.SafetyExcludedModels, testOpus)
		})
	}
}

func TestForcedContextEstimateReadmitsForcedPin(t *testing.T) {
	req := router.Request{
		ForceModel:                    testOpus,
		ExcludedModels:                modelSet([]string{testOpus}),
		SafetyExcludedModels:          modelSet([]string{testOpus}),
		ContextWindowExcludedModels:   modelSet([]string{testOpus}),
		UnsignedHistoryExcludedModels: modelSet([]string{testOpus}),
	}
	readmitted := readmitForcedContextEstimate(req, req.ForceModel)
	assert.NotContains(t, readmitted.ExcludedModels, testOpus)
	assert.NotContains(t, readmitted.SafetyExcludedModels, testOpus)
	assert.Contains(t, readmitted.ContextWindowExcludedModels, testOpus, "the estimate marker itself is preserved")
	assert.Contains(t, readmitted.UnsignedHistoryExcludedModels, testOpus, "unsigned-history exclusion still holds")
	// An automatic estimate for a model the request does not force stays excluded.
	assert.Contains(t, readmitForcedContextEstimate(req, "other-model").ExcludedModels, testOpus)
	// An explicit (non-estimate) exclusion is never lifted.
	explicit := router.Request{ForceModel: testOpus, ExcludedModels: modelSet([]string{testOpus})}
	assert.Contains(t, readmitForcedContextEstimate(explicit, testOpus).ExcludedModels, testOpus)
}
