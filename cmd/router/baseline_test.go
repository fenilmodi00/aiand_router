package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDefaultBaselineModel(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "")
		require.NoError(t, os.Unsetenv("ROUTER_DEFAULT_BASELINE_MODEL"))
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", resolveDefaultBaselineModel())
	})

	t.Run("explicit empty disables substitution", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "")
		assert.Empty(t, resolveDefaultBaselineModel())
	})

	t.Run("explicit value wins", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "z-ai/glm-5.3")
		assert.Equal(t, "z-ai/glm-5.3", resolveDefaultBaselineModel())
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "  z-ai/glm-5.3-flash  ")
		assert.Equal(t, "z-ai/glm-5.3-flash", resolveDefaultBaselineModel())
	})
}
