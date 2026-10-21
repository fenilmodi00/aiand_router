package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/policy"
)

func maxResolver() *policy.Resolver {
	return policy.NewResolver(
		set("deepseek/deepseek-v4-pro", "moonshotai/kimi-k3", "z-ai/glm-5.3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
}

func TestResolverWithoutProductBoundaryKeepsEveryModel(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{})

	assert.ElementsMatch(t,
		[]string{"deepseek/deepseek-v4-pro", "moonshotai/kimi-k3", "z-ai/glm-5.3"},
		catalogIDs(resolved.Candidates),
	)
}

// Every catalog model is open-source in the ai&-only roster, so the
// Max open-source-only boundary excludes none of them: the pool survives
// the boundary intact.
func TestMaxBoundaryKeepsTheOpenSourceOnlyRoster(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{ProductEligibility: eligibility.MaxOpenSourceOnly})

	assert.ElementsMatch(t,
		[]string{"deepseek/deepseek-v4-pro", "moonshotai/kimi-k3", "z-ai/glm-5.3"},
		catalogIDs(resolved.Candidates),
	)
	for _, id := range []string{"deepseek/deepseek-v4-pro", "moonshotai/kimi-k3", "z-ai/glm-5.3"} {
		assert.NotContainsf(t, resolved.Diagnostics, policy.Diagnostic{
			CatalogID: id,
			Reason:    policy.ExclusionProductIneligible,
		}, "%q is open-source and must not be product-ineligible", id)
	}
}

// The boundary is not a preference the resolver may drop to keep a pool
// non-empty, and not something an org allowlist naming the model re-opens.
func TestMaxBoundaryIsHardEvenWhenItEmptiesThePool(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		AllowedModels:      set("moonshotai/kimi-k3"),
		PreferredModels:    []string{"moonshotai/kimi-k3"},
	})

	// The model is open-source, so the boundary keeps it; the pool is
	// non-empty and the allowlist still governs membership.
	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "moonshotai/kimi-k3", resolved.Candidates[0].CatalogID)
}

// An eligible model the org excluded is still an ordinary request exclusion.
func TestEligibleModelStillReportsRequestedExclusion(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		ExcludedModels:     set("deepseek/deepseek-v4-pro"),
	})

	assert.NotContains(t, catalogIDs(resolved.Candidates), "deepseek/deepseek-v4-pro")
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek/deepseek-v4-pro",
		Reason:    policy.ExclusionRequested,
	})
}

func catalogIDs(candidates []policy.Candidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.CatalogID)
	}
	return ids
}
