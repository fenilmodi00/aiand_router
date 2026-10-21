package proxy

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

// The arm's pre-cap level goes on ForceEffort so the per-model cap still
// applies downstream; the wire level is what the emit paths resolve.
func TestEffortResolution_Apply(t *testing.T) {
	caps := router.Lookup("gpt-5.5")
	opts := translate.EmitOptions{Capabilities: caps}

	effortResolutionFor(caps, "xhigh", "xhigh", effortSourceArm).apply(&opts)
	assert.Equal(t, "xhigh", opts.ForceEffort)
	assert.Equal(t, "max", opts.ForceReasoningEffort)

	effortResolution{}.apply(&opts)
	assert.Empty(t, opts.ForceEffort)
	assert.Empty(t, opts.ForceReasoningEffort)
}

// A rescue candidate never served the failed model's level, so keeping it
// would report an identity the sibling never served.
func TestRescueDecisionFor_DropsEffort(t *testing.T) {
	failed := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "gpt-5.6-luna",
		Effort:   "xhigh",
		Metadata: &router.RoutingMetadata{SelectedArmID: "gpt-5.6-luna:xhigh"},
	}

	out := rescueDecisionFor(failed, "gpt-5.6-sol", providers.ProviderAIAND, ReasonSiblingFailover)

	assert.Empty(t, out.Effort)
	assert.Equal(t, "gpt-5.6-sol", out.ServedIdentity())
}
