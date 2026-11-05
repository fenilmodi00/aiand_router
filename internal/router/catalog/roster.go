package catalog

// ApprovedAutoRoutedModels is the operator-approved automatic-routing pool:
// the exact roster auto mode may ever serve. It is deliberately an explicit
// list, not derived from Tier — a model gains automatic eligibility only by
// being listed here AND carrying a tier AND binding a registered provider.
//
// Every catalog row absent from this list is pin-only by construction:
// /force-model and session pins can still dispatch it, but the scorer, the
// served bundle's registry, and every routing-target set exclude it. The
// roster tests in this package and the cluster bundle tests enforce that
// coupling: an unapproved model gaining a tier or entering a served
// model_registry.json without being added here fails the build.
var ApprovedAutoRoutedModels = []ModelID{
	ModelIDDeepSeekV41Flash,
	ModelIDDeepSeekV4Pro,
	ModelIDKimiK3,
	ModelIDGLM53,
	ModelIDGLM53Flash,
	ModelIDMotif3,
	ModelIDQwen3827B,
}

// ApprovedAutoRoutedSet returns the operator-approved automatic-routing pool
// as a set keyed by model ID.
func ApprovedAutoRoutedSet() map[string]struct{} {
	out := make(map[string]struct{}, len(ApprovedAutoRoutedModels))
	for _, id := range ApprovedAutoRoutedModels {
		out[id.String()] = struct{}{}
	}
	return out
}

// IsApprovedAutoRouted reports whether the model ID is in the
// operator-approved automatic-routing pool.
func IsApprovedAutoRouted(id string) bool {
	_, ok := ApprovedAutoRoutedSet()[id]
	return ok
}
