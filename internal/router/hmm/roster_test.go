package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/hmm"
)

func TestDeployedModelsForRosterIDs_MapsRosterSlugsToCatalogEntries(t *testing.T) {
	// Roster slugs are the catalog IDs themselves (every surviving model is
	// slash-form); entries carry the catalog ID and the primary provider.
	got := hmm.DeployedModelsForRosterIDs([]string{
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4.1-flash",
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-pro-0813",
		"moonshotai/kimi-k2.7-code",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.2",
		"z-ai/glm-5.3",
		"z-ai/glm-5.3-flash",
	})

	byModel := make(map[string]string, len(got))
	for _, e := range got {
		byModel[e.Model] = e.Provider
	}

	assert.Equal(t, providers.ProviderAIAND, byModel["deepseek/deepseek-v4-flash"])
	assert.Equal(t, providers.ProviderAIAND, byModel["deepseek/deepseek-v4.1-flash"])
	assert.Equal(t, providers.ProviderAIAND, byModel["deepseek/deepseek-v4-pro"])
	assert.Equal(t, providers.ProviderAIAND, byModel["deepseek/deepseek-v4-pro-0813"])
	// The aliased Kimi arm maps back to the catalog ID the dispatch path
	// consumes, not the roster slug.
	assert.Equal(t, providers.ProviderAIAND, byModel["moonshotai/kimi-k2.7"])
	assert.Equal(t, providers.ProviderAIAND, byModel["moonshotai/kimi-k3"])
	assert.Equal(t, providers.ProviderAIAND, byModel["z-ai/glm-5.2"])
	assert.Equal(t, providers.ProviderAIAND, byModel["z-ai/glm-5.3"])
	assert.Equal(t, providers.ProviderAIAND, byModel["z-ai/glm-5.3-flash"])
}

func TestDeployedModelsForRosterIDs_PreservesOrderAndDropsUnknown(t *testing.T) {
	got := hmm.DeployedModelsForRosterIDs([]string{
		"z-ai/glm-5.3",
		"not/a-real-roster-id",
		"z-ai/glm-5.3", // duplicate: only the first survives
	})

	require.Len(t, got, 1)
	assert.Equal(t, "z-ai/glm-5.3", got[0].Model)
}

func TestDeployedModelsForRosterIDs_EmptyInput(t *testing.T) {
	assert.Empty(t, hmm.DeployedModelsForRosterIDs(nil))
}
