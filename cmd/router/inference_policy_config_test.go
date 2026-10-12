package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"
)

func TestResolveCompactionModelFailsClosed(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "")
		model, err := resolveCompactionModel(providers.ProviderAIAND)
		require.NoError(t, err)
		assert.Equal(t, policy.PrecompactionDefaultModel, model)
	})

	t.Run("valid AIand binding", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "zai-org/glm-5.3")
		model, err := resolveCompactionModel(providers.ProviderAIAND)
		require.NoError(t, err)
		assert.Equal(t, "zai-org/glm-5.3", model)
	})

	t.Run("invalid binding", func(t *testing.T) {
		t.Setenv("ROUTER_COMPACTION_MODEL", "claude-fable-5")
		_, err := resolveCompactionModel(providers.ProviderAIAND)
		assert.ErrorContains(t, err, "has no aiand catalog binding")
	})
}

func TestResolveHardPinModelRejectsProviderWithoutModel(t *testing.T) {
	t.Setenv("ROUTER_HARD_PIN_MODEL", "")
	t.Setenv("ROUTER_HARD_PIN_PROVIDER", providers.ProviderAIAND)

	_, _, err := resolveHardPinModel(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.ErrorContains(t, err, "requires ROUTER_HARD_PIN_MODEL")
}

func TestResolveHardPinModelUsesDefaultProviderForExplicitModel(t *testing.T) {
	t.Setenv("ROUTER_HARD_PIN_MODEL", "zai-org/glm-5.3-flash")
	t.Setenv("ROUTER_HARD_PIN_PROVIDER", "")

	provider, model, err := resolveHardPinModel(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAIAND, provider)
	assert.Equal(t, "zai-org/glm-5.3-flash", model)
}
