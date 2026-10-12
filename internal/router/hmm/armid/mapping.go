// Package armid owns pure roster/catalog identity mapping shared by runtimes and release tooling.
// It must not import the HMM router or its ONNX-backed deployment discovery.
package armid

import (
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// ForModel maps a compiled catalog model to its canonical roster identity.
func ForModel(m catalog.Model) string {
	if strings.Contains(m.ID, "/") {
		return m.ID
	}
	switch m.PrimaryProvider() {
	case providers.ProviderAnthropic:
		return "anthropic/" + m.ID
	}
	return ""
}

// SplitEffort splits a roster arm ID of the form "model:effort" into its base model and canonical effort level; bare arms return (armID, "").
func SplitEffort(armID string) (baseID string, effort string) {
	if idx := strings.LastIndex(armID, ":"); idx > 0 {
		suffix := armID[idx+1:]
		if router.CanonicalizeEffort(suffix) != suffix {
			return armID, ""
		}
		if router.IsValidEffort(suffix) {
			return armID[:idx], suffix
		}
	}
	return armID, ""
}

// EffortArm composes a roster arm ID from a base model ID and effort level.
func EffortArm(baseID, effort string) string {
	if effort == "" {
		return baseID
	}
	return baseID + ":" + effort
}

// CatalogIDForRoster maps a roster arm ID back to its catalog model ID via the
// same forward mapping the resolver uses. Returns the arm ID unchanged when no
// catalog model maps to it.
func CatalogIDForRoster(rosterID string) string {
	baseID, _ := SplitEffort(rosterID)
	for _, m := range catalog.Models {
		if ForModel(m) == baseID {
			return m.ID
		}
	}
	return rosterID
}
