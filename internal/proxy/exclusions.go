package proxy

import "aiand-os/router/internal/router"

// ingressExclusions folds the ingress pre-filter results into the request's
// typed exclusion value. union already carries every hard exclusion
// (installation policy, allowlist desugaring, product boundary, image caps,
// context overflow minus the widest-window re-admissions); the remaining sets
// tag the same models with the kind that excluded them so diagnostics can
// attribute each exclusion and explicit forces can decide which kinds they
// outrank.
func ingressExclusions(union, safetySet, contextWindowSet, imageLimitSet map[string]struct{}) router.Exclusions {
	e := router.RequestedExclusions(union)
	e = e.ExcludeSet(safetySet, router.ExclusionSafety)
	e = e.ExcludeSet(contextWindowSet, router.ExclusionContextWindow)
	e = e.ExcludeSet(imageLimitSet, router.ExclusionImageLimit)
	return e
}
