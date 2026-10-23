package armid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router/catalog"
)

func TestKimiK27RosterIdentityUsesAlias(t *testing.T) {
	model, ok := catalog.ByID("moonshotai/kimi-k3")
	require.True(t, ok)
	assert.Equal(t, "moonshotai/kimi-k3", ForModel(model))
	assert.Equal(t, "moonshotai/kimi-k3", CatalogIDForRoster("moonshotai/kimi-k3"))
	assert.Empty(t, ValidateRosterIDs([]string{"moonshotai/kimi-k3"}))
}

func TestSlashFormModelIsItsOwnRosterIdentity(t *testing.T) {
	model, ok := catalog.ByID("z-ai/glm-5.3")
	require.True(t, ok)
	assert.Equal(t, "z-ai/glm-5.3", ForModel(model))
	assert.Equal(t, "z-ai/glm-5.3", CatalogIDForRoster("z-ai/glm-5.3"))
	assert.Empty(t, ValidateRosterIDs([]string{"z-ai/glm-5.3"}))
}
