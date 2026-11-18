package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"
)

func TestSwitchedAttemptOptionsResolveNewTargetEffortWithoutChangingOriginal(t *testing.T) {
	base := translate.EmitOptions{
		TargetModel:          "synthetic-primary",
		TargetProvider:       providers.ProviderAIAND,
		Capabilities:         router.Lookup("synthetic-primary"),
		ForceEffort:          "low",
		ForceReasoningEffort: "low",
		AppendAutonomySystem: true,
	}
	target := router.Decision{Provider: providers.ProviderAIAND, Model: "synthetic-target", Effort: "xhigh"}
	var resolved router.Decision
	var resolvedCaps router.ModelSpec
	var resolvedEscalate bool
	opts, effort := switchedAttemptOptions(context.Background(), base, target, true,
		func(_ context.Context, d router.Decision, caps router.ModelSpec, escalate bool) effortResolution {
			resolved, resolvedCaps, resolvedEscalate = d, caps, escalate
			return effortResolution{Arm: "xhigh", Selected: "xhigh", Sent: "high", Source: effortSourceArm}
		})
	assert.Equal(t, target.Model, opts.TargetModel)
	assert.Equal(t, target.Provider, opts.TargetProvider)
	assert.Equal(t, router.Lookup(target.Model), opts.Capabilities)
	assert.True(t, opts.ModelSwitched, "signed reasoning from the old target must not replay verbatim")
	assert.Equal(t, target, resolved, "the new target resolves its own effort")
	assert.Equal(t, router.Lookup(target.Model), resolvedCaps)
	assert.True(t, resolvedEscalate)
	assert.Equal(t, "high", effort.Sent)
	assert.Equal(t, "xhigh", opts.ForceEffort, "emit options retain the pre-cap level")
	assert.Equal(t, "max", opts.ForceReasoningEffort, "the uncapped target resolves its own wire level")
	assert.Equal(t, effortSourceArm, effort.Source)
	assert.True(t, opts.AppendAutonomySystem)
	assert.Equal(t, "synthetic-primary", base.TargetModel)
	assert.Equal(t, "low", base.ForceReasoningEffort)
	assert.False(t, base.ModelSwitched)
}
