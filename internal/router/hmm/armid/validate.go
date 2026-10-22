package armid

import (
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

// Diagnostic re-exports policy.Diagnostic for roster-validation callers.
type Diagnostic = policy.Diagnostic

// ValidateRosterIDs reports roster arms that cannot be dispatched — unknown,
// ambiguous, or provider-policy-blocked. Observation only: DeployedModelsForRosterIDs's
// silent drop is unchanged.
func ValidateRosterIDs(rosterIDs []string) []Diagnostic {
	return ValidateWithCatalog(rosterIDs, catalog.Models)
}

// ValidateWithCatalog validates against explicit catalog/provider contracts without runtime dependencies.
func ValidateWithCatalog(rosterIDs []string, models []catalog.Model) []Diagnostic {
	inverse := make(map[string][]catalog.Model, len(models))
	for _, m := range models {
		rosterID := ForModel(m)
		if rosterID == "" {
			continue
		}
		inverse[rosterID] = append(inverse[rosterID], m)
	}

	var diagnostics []Diagnostic
	for _, rosterID := range rosterIDs {
		baseID, _ := SplitEffort(rosterID)
		matches := inverse[baseID]
		switch {
		case len(matches) == 0:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, Reason: policy.ExclusionUnknownCatalogModel})
		case len(matches) > 1:
			diagnostics = append(diagnostics, Diagnostic{RosterID: rosterID, CatalogID: matches[0].ID, Reason: policy.ExclusionAmbiguousRoster})
		}
	}
	return diagnostics
}

