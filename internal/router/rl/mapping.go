package rl

import (
	"aiand-os/router/internal/router/catalog"
)

// rosterIDFor returns the policy-artifact roster ID for a catalog model.
// The sidecar canonicalizes and intersects candidates against its own roster,
// so a model the policy doesn't know is simply dropped.
func rosterIDFor(m catalog.Model) string {
	return m.ID
}
