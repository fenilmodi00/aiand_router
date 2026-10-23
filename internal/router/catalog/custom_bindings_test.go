package catalog_test

import (
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// customModel is catalog-bound to ai&, so a configuration-declared endpoint is
// the only way it reaches a customer's own gateway.
const customModel = "z-ai/glm-5.3"

func customFor(provider string) map[string][]string {
	return map[string][]string{customModel: {provider}}
}

func TestResolveBindingWithCustom_ServesModelWithNoCatalogBinding(t *testing.T) {
	const custom = "custom_endpoint"
	available := map[string]struct{}{custom: {}}

	_, ok := catalog.ResolveBinding(customModel, available)
	require.False(t, ok, "precondition: the catalog does not bind this model to the custom endpoint")

	binding, ok := catalog.ResolveBindingWithCustom(
		customModel, available, customFor(custom))

	require.True(t, ok)
	assert.Equal(t, custom, binding.Provider)

	// Pricing falls back to list price: a custom endpoint bills on its own
	// contract and the primary binding's rate is the only one we have.
	primary, ok := catalog.PrimaryPriceFor(customModel)
	require.True(t, ok)
	assert.Equal(t, primary, binding.Price)
}

func TestResolveBindingWithCustom_DirectVendorWins(t *testing.T) {
	const custom = "custom_endpoint"
	binding, ok := catalog.ResolveBindingWithCustom(
		customModel,
		map[string]struct{}{
			providers.ProviderAIAND: {},
			custom:                  {},
		},
		customFor(custom),
	)

	require.True(t, ok)
	assert.Equal(t, providers.ProviderAIAND, binding.Provider)
}

func TestResolveBindingWithCustom_IgnoresUnavailableProvider(t *testing.T) {
	_, ok := catalog.ResolveBindingWithCustom(
		customModel,
		map[string]struct{}{},
		customFor("custom_endpoint"),
	)

	assert.False(t, ok)
}

func TestEnumerateBindingsWithCustom_CustomRanksAfterCatalog(t *testing.T) {
	const custom = "custom_endpoint"
	got := catalog.EnumerateBindingsWithCustom(
		customModel,
		map[string]struct{}{
			providers.ProviderAIAND: {},
			custom:                  {},
		},
		customFor(custom),
	)

	require.Len(t, got, 2)
	assert.Equal(t, providers.ProviderAIAND, got[0].Provider)
	assert.Equal(t, custom, got[1].Provider)
	assert.Greater(t, got[1].Index, got[0].Index, "failover order must stay strictly increasing")
}

// TestEnumerateBindingsWithCustom_NoDuplicateProvider: a key may declare a
// model the catalog already binds to that same provider; dispatch must not
// retry the identical upstream as its own fallback.
func TestEnumerateBindingsWithCustom_NoDuplicateProvider(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAIAND: {}}

	got := catalog.EnumerateBindingsWithCustom(
		customModel,
		available,
		map[string][]string{customModel: {providers.ProviderAIAND}},
	)

	assert.Equal(t, catalog.EnumerateBindings(customModel, available), got)
}
