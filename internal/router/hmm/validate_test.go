package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router/hmm"
)

func TestValidateRosterIDs_AliasMappedArmIsValid(t *testing.T) {
	// "moonshotai/kimi-k3" only maps back to the catalog ID
	// "moonshotai/kimi-k3" through the roster alias; it must not be
	// reported as unknown.
	assert.Empty(t, hmm.ValidateRosterIDs([]string{"moonshotai/kimi-k3"}))
}

func TestValidateRosterIDs_EffortSuffixedArmIsValid(t *testing.T) {
	assert.Empty(t, hmm.ValidateRosterIDs([]string{"z-ai/glm-5.3:high"}))
}

func TestValidateRosterIDs_MixedRosterReportsOnlyBadArms(t *testing.T) {
	diags := hmm.ValidateRosterIDs([]string{
		"z-ai/glm-5.3",
		"newvendor/model-x",
		"anthropic/claude-opus-4.8",
	})

	require.Len(t, diags, 2)
	assert.Equal(t, "newvendor/model-x", diags[0].RosterID)
	assert.Equal(t, "anthropic/claude-opus-4.8", diags[1].RosterID)
}
