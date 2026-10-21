package rl

import (
	"weave-os/router/internal/router/catalog"
)

// rosterAliases maps a catalog model ID to the policy-artifact roster ID when
// the slash-form rule below would produce the wrong slug. Every surviving
// catalog model is slash-form except the Kimi K2.7 row, which the policy
// knows under its "-code" build name.
var rosterAliases = map[string]string{
	"moonshotai/kimi-k2.7": "moonshotai/kimi-k2.7-code",
}

// rosterIDFor returns the policy-artifact roster ID for a catalog model. The
// sidecar canonicalizes and intersects candidates against its own roster, so a
// best-effort slug is safe: a model the policy doesn't know is simply dropped.
func rosterIDFor(m catalog.Model) string {
	if alias, ok := rosterAliases[m.ID]; ok {
		return alias
	}
	return m.ID
}
