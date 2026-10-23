package rl

import (
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
)

// TestRosterIDForCoversEveryProvider guards against a new Provider* constant
// going unreviewed by rosterIDFor. ai& is the only provider left, and it
// serves vendor-prefixed slash-form slugs, so the bare model ID is the right
// roster identity for it.
func TestRosterIDForCoversEveryProvider(t *testing.T) {
	for _, p := range providers.AllProviders() {
		assert.Equalf(t, providers.ProviderAIAND, p,
			"only ai& should remain a known provider")
	}
}

// TestRosterIDForMatchesActualBehavior asserts rosterIDFor returns the model
// ID unchanged for ai& (no vendor prefix is applied).
func TestRosterIDForMatchesActualBehavior(t *testing.T) {
	const bareModelID = "some-bare-model-id"

	model := catalog.Model{
		ID:        bareModelID,
		Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}},
	}
	assert.Equal(t, bareModelID, rosterIDFor(model))
}

// TestRosterIDForKeepsTheKimiCodeAlias pins the one surviving divergence: the
// policy roster knows Kimi K2.7 under its "-code" build name.
func TestRosterIDForKeepsTheKimiCodeAlias(t *testing.T) {
	model := catalog.Model{
		ID:        "moonshotai/kimi-k3",
		Providers: []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}},
	}
	assert.Equal(t, "moonshotai/kimi-k3", rosterIDFor(model))
}

// TestRosterIDForResolvesEveryCatalogModel: no catalog model may map to an
// empty roster ID — an unmapped model is silently dropped from the policy's
// candidate set.
func TestRosterIDForResolvesEveryCatalogModel(t *testing.T) {
	for _, model := range catalog.Models {
		assert.NotEmptyf(t, rosterIDFor(model), "catalog model %q must map to a roster ID", model.ID)
	}
}
