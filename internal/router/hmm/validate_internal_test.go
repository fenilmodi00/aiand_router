package hmm

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRosterIDs_AmbiguousMappingReported(t *testing.T) {
	// Two catalog models with the same slash-form ID map to one roster ID.
	models := []catalog.Model{
		{ID: "acme/model-a", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}},
		{ID: "acme/model-a", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}},
	}

	diags := validateRosterIDs([]string{"acme/model-a"}, models, policy.ManagedProviderPolicy())

	require.Len(t, diags, 1)
	assert.Equal(t, policy.ExclusionAmbiguousRoster, diags[0].Reason)
	assert.Equal(t, "acme/model-a", diags[0].RosterID)
}

func TestValidateRosterIDs_ValidArmIsNotReported(t *testing.T) {
	models := []catalog.Model{
		{ID: "acme/model-b", Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}},
	}

	diags := validateRosterIDs([]string{"acme/model-b"}, models, policy.ManagedProviderPolicy())

	assert.Empty(t, diags)
}
