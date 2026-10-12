package rl

import (
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

// rosterIDFor returns the policy-artifact roster ID for a catalog model. The
// sidecar canonicalizes and intersects candidates against its own roster, so a
// best-effort slug is safe: a model the policy doesn't know is simply dropped.
func rosterIDFor(m catalog.Model) string {
	if strings.Contains(m.ID, "/") {
		return m.ID
	}
	switch m.PrimaryProvider() {
	case providers.ProviderAnthropic:
		return "anthropic/" + m.ID
	}
	return m.ID
}
