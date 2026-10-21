package armid

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/catalog"
)

func TestKimiK27RosterIdentityUsesAlias(t *testing.T) {
	model, ok := catalog.ByID("moonshotai/kimi-k2.7")
	require.True(t, ok)
	assert.Equal(t, "moonshotai/kimi-k2.7-code", ForModel(model))
	assert.Equal(t, "moonshotai/kimi-k2.7", CatalogIDForRoster("moonshotai/kimi-k2.7-code"))
	assert.Empty(t, ValidateRosterIDs([]string{"moonshotai/kimi-k2.7-code"}))
}

func TestSlashFormModelIsItsOwnRosterIdentity(t *testing.T) {
	model, ok := catalog.ByID("z-ai/glm-5.3")
	require.True(t, ok)
	assert.Equal(t, "z-ai/glm-5.3", ForModel(model))
	assert.Equal(t, "z-ai/glm-5.3", CatalogIDForRoster("z-ai/glm-5.3"))
	assert.Empty(t, ValidateRosterIDs([]string{"z-ai/glm-5.3"}))
}
