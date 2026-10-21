package rl

import (
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRosterIDForCoversEveryProvider guards against a new Provider* constant
// going unreviewed by rosterIDFor. With one upstream there is no per-provider
// vendor prefix to apply, so every provider takes the bare model ID.
func TestRosterIDForCoversEveryProvider(t *testing.T) {
	for _, p := range providers.AllProviders() {
		m := catalog.Model{ID: "some-model"}
		assert.Equalf(t, m.ID, rosterIDFor(m), "provider %q must map to the bare model ID", p)
	}
}

// TestRosterIDForUsesSlashFormModelID asserts a slash-form catalog ID is
// returned unchanged — it already is the roster slug.
func TestRosterIDForUsesSlashFormModelID(t *testing.T) {
	m := catalog.Model{ID: "z-ai/glm-5.3"}
	assert.Equal(t, "z-ai/glm-5.3", rosterIDFor(m))
}

// TestRosterIDForHonorsAlias asserts the alias table still wins over the
// slash-form short-circuit.
func TestRosterIDForHonorsAlias(t *testing.T) {
	m := catalog.Model{ID: "moonshotai/kimi-k2.7"}
	assert.Equal(t, "moonshotai/kimi-k2.7-code", rosterIDFor(m))
}

// TestRosterIDForCoversCatalogModels asserts every catalog model resolves to a
// non-empty roster ID.
func TestRosterIDForCoversCatalogModels(t *testing.T) {
	for _, m := range catalog.Models {
		require.NotEmptyf(t, rosterIDFor(m), "catalog model %q has no roster ID", m.ID)
	}
}
