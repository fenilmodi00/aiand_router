package cluster

import (
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The served bundle's roster and the operator-approved auto pool
// (catalog.ApprovedAutoRoutedModels) must stay coupled: registry ⊆ approved
// and approved ⊆ registry (for the served version). A new model entering
// model_registry.json without being approved — or an approved model dropped
// from the registry — fails these tests, so auto mode can only ever serve an
// approved model.

// TestLatestBundle_RegistryWithinApprovedRoster: every model the served
// bundle's model_registry.json deploys is operator-approved for automatic
// routing. A training run that adds a model to deployed_models must first add
// it to catalog.ApprovedAutoRoutedModels (and give it a tier), or this fails.
func TestLatestBundle_RegistryWithinApprovedRoster(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)

	approved := catalog.ApprovedAutoRoutedSet()
	for _, m := range bundle.Registry.Models() {
		assert.Containsf(t, approved, m,
			"bundle %s deploys %q, which is not in catalog.ApprovedAutoRoutedModels — approve it in internal/router/catalog/roster.go or remove it from model_registry.json", version, m)
	}
}

// TestLatestBundle_ApprovedRosterFullyDeployed: every approved model is on
// the served bundle's roster. An approved model missing from model_registry.json
// can never be served by auto mode, so the approval is dead weight — the
// roster and the registry must agree.
func TestLatestBundle_ApprovedRosterFullyDeployed(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)

	deployed := make(map[string]struct{}, len(bundle.Registry.DeployedModels))
	for _, m := range bundle.Registry.Models() {
		deployed[m] = struct{}{}
	}
	for _, id := range catalog.ApprovedAutoRoutedModels {
		assert.Containsf(t, deployed, id.String(),
			"approved auto-routed model %q is missing from bundle %s's model_registry.json — it can never be served by auto mode", id, version)
	}
}

// TestLatestBundle_ScorerCandidatesWithinApprovedRoster runs the REAL scorer
// over the served bundle and asserts its boot candidate set (what a policy
// may ever select) is exactly the approved pool filtered to registered
// providers — no pin-only model can sneak in through the registry or the
// catalog tier table.
func TestLatestBundle_ScorerCandidatesWithinApprovedRoster(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)

	available := map[string]struct{}{providers.ProviderAIAND: {}}
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{dim: bundle.Centroids.Dim}, available)
	require.NoError(t, err)

	approved := catalog.ApprovedAutoRoutedSet()
	for _, e := range s.DeployedModels() {
		assert.Containsf(t, approved, e.Model,
			"scorer candidate %q on bundle %s is not in the approved auto pool", e.Model, version)
	}
}
