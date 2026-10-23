package router_test

import (
	"testing"

	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

// TestLookup_NoModelDeclaresCapabilities pins the post-cut invariant: the
// wired catalog carries no per-model capability data, so every lookup is the
// zero-value spec and no capability-gated field is forwarded.
func TestLookup_NoModelDeclaresCapabilities(t *testing.T) {
	for _, model := range []string{
		"unknown-model-99",
		router.ModelGemini4Argon,
		"deepseek/deepseek-v4-pro",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.3",
	} {
		spec := router.Lookup(model)
		assert.False(t, spec.Supports(router.CapReasoning), model)
		assert.False(t, spec.Supports(router.CapXhighEffort), model)
		assert.Empty(t, spec.Reasoning().Levels, model)
	}
}
