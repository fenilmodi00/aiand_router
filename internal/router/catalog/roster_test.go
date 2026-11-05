package catalog

import (
	"testing"

	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The approved auto-roster is the operator-approved list of models auto mode
// may ever serve. These tests make its coupling to the rest of the catalog
// structural: approved ⊆ tiered ⊆ routing targets, non-approved ⇒ untiered,
// and no duplicate entries. Adding a tier to an unapproved model, or an
// approved model losing its tier or provider binding, fails here.

// TestApprovedRoster_MembersAreDistinct rejects a roster that lists the same
// model twice — a copy-paste edit would silently hide the second entry.
func TestApprovedRoster_MembersAreDistinct(t *testing.T) {
	seen := make(map[ModelID]struct{}, len(ApprovedAutoRoutedModels))
	for _, id := range ApprovedAutoRoutedModels {
		require.NotContains(t, seen, id, "approved roster lists %q twice", id)
		seen[id] = struct{}{}
	}
	require.NotEmpty(t, ApprovedAutoRoutedModels, "approved roster must not be empty")
}

// TestApprovedRoster_MembersExistInCatalog keeps the roster honest: a renamed
// or retired model ID must not linger here.
func TestApprovedRoster_MembersExistInCatalog(t *testing.T) {
	for _, id := range ApprovedAutoRoutedModels {
		_, ok := ByID(id.String())
		assert.Truef(t, ok, "approved roster member %q is not a catalog row", id)
	}
}

// TestApprovedRoster_MembersAreTieredAndRegistered: every approved model
// carries a tier and binds a registered provider, so it lands in
// RoutingTargetSet — the set the scorer and policy sidecars derive their
// candidates from.
func TestApprovedRoster_MembersAreTieredAndRegistered(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAIAND: {}}
	targets := RoutingTargetSet(available)
	for _, id := range ApprovedAutoRoutedModels {
		m, ok := ByID(id.String())
		require.Truef(t, ok, "approved roster member %q missing from catalog", id)
		assert.NotEqualf(t, TierUnknown, m.Tier,
			"approved roster member %q must carry a tier", id)
		assert.Containsf(t, targets, id.String(),
			"approved roster member %q must be a routing target (tiered + registered provider)", id)
	}
}

// TestNonApprovedRowsArePinOnly: every catalog row outside the approved
// roster is untiered — auto mode structurally cannot serve it because
// RoutingTargetSet, AllowedAtOrBelow and ValidateDeployed all skip
// TierUnknown rows.
func TestNonApprovedRowsArePinOnly(t *testing.T) {
	approved := ApprovedAutoRoutedSet()
	for _, m := range Models {
		if _, ok := approved[m.ID]; ok {
			continue
		}
		assert.Equalf(t, TierUnknown, m.Tier,
			"model %q has a tier but is not in ApprovedAutoRoutedModels — add it to the roster or drop the tier", m.ID)
	}
}

// TestApprovedRosterMatchesTieredRows pins the roster against the tier table
// in the other direction: a tiered catalog row that is missing from the
// approved roster fails, so the two lists cannot drift apart.
func TestApprovedRosterMatchesTieredRows(t *testing.T) {
	approved := ApprovedAutoRoutedSet()
	for _, m := range Models {
		if m.Tier == TierUnknown {
			continue
		}
		assert.Containsf(t, approved, m.ID,
			"tiered model %q is not in ApprovedAutoRoutedModels — it would auto-route without operator approval", m.ID)
	}
	assert.Len(t, ApprovedAutoRoutedModels, 7, "approved roster size")
}
