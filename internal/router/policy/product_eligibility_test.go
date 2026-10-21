package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/policy"
)

func maxResolver() *policy.Resolver {
	return policy.NewResolver(
		set("deepseek/deepseek-v4-pro", "claude-opus-4-8", "muse-spark-1.3"),
		set(providers.ProviderAIAND, providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
}

// The boundary is not a preference the resolver may drop to keep a pool
// non-empty, and not something an org allowlist naming the model re-opens.
func TestMaxBoundaryIsHardEvenWhenItEmptiesThePool(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		AllowedModels:      set("claude-opus-4-8"),
		PreferredModels:    []string{"claude-opus-4-8"},
	})

	assert.Empty(t, resolved.Candidates)
}

// An eligible model the org excluded is still an ordinary request exclusion.
func TestEligibleModelStillReportsRequestedExclusion(t *testing.T) {
	resolved := maxResolver().Resolve(router.Request{
		ProductEligibility: eligibility.MaxOpenSourceOnly,
		ExcludedModels:     set("deepseek/deepseek-v4-pro"),
	})

	assert.Empty(t, resolved.Candidates)
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
