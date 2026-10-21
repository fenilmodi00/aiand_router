package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveDefaultBaselineModel(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		orig, hadOrig := os.LookupEnv("ROUTER_DEFAULT_BASELINE_MODEL")
		require := func() {
			if hadOrig {
				os.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", orig)
			} else {
				os.Unsetenv("ROUTER_DEFAULT_BASELINE_MODEL")
			}
		}
		os.Unsetenv("ROUTER_DEFAULT_BASELINE_MODEL")
		t.Cleanup(require)
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", resolveDefaultBaselineModel())
	})

	t.Run("explicit empty disables substitution", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "")
		assert.Equal(t, "", resolveDefaultBaselineModel())
	})

	t.Run("explicit value wins", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "z-ai/glm-5.2")
		assert.Equal(t, "z-ai/glm-5.2", resolveDefaultBaselineModel())
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		t.Setenv("ROUTER_DEFAULT_BASELINE_MODEL", "  claude-haiku-4-5  ")
		assert.Equal(t, "z-ai/glm-5.3-flash", resolveDefaultBaselineModel())
	})
}
