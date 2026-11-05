package providers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
)

// TestValidateDispatchableAcceptsAiand asserts the boot guard accepts the one
// wired provider and still rejects a registered-but-unknown provider name,
// surfacing it in the error.
func TestValidateDispatchable(t *testing.T) {
	require.NoError(t, providers.ValidateDispatchable([]string{providers.ProviderAIAND}))

	err := providers.ValidateDispatchable([]string{providers.ProviderAIAND, "brand-new-provider"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "brand-new-provider")
}

// TestFamilyForKnownProviders pins the expected family of the provider so a
// mis-assignment (e.g. flipping it off OpenAI-compat) fails.
func TestFamilyForKnownProviders(t *testing.T) {
	assert.Equal(t, providers.FamilyOpenAICompat, providers.FamilyFor(providers.ProviderAIAND))
	assert.True(t, providers.IsOpenAICompat(providers.ProviderAIAND))
}

// TestAllProvidersSorted asserts AllProviders returns the single known
// provider (deterministic display order for the dashboard).
func TestAllProvidersReturnsAiandOnly(t *testing.T) {
	assert.Equal(t, []string{providers.ProviderAIAND}, providers.AllProviders())
}

// TestFamilyForUnknownIsZero asserts an unmapped provider name reports
// FamilyUnknown (the request-path backstop and boot-guard both rely on this).
func TestFamilyForUnknownIsZero(t *testing.T) {
	assert.Equal(t, providers.FamilyUnknown, providers.FamilyFor("nope-not-a-provider"))
}
