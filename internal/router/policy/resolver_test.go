package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func catalogRosterID(model catalog.Model) string { return model.ID }

// The single-provider cut left one binding per catalog model, so the resolver
// must resolve the model to that binding and report the catalog's upstream ID
// for it (ai& names models vendor-prefixed).
func TestManagedResolverResolvesTheOnlyProviderBinding(t *testing.T) {
	resolver := policy.NewResolver(
		set(catalog.ModelIDDeepSeekV41Flash.String()),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, catalog.ModelIDDeepSeekV41Flash.String(), resolved.Candidates[0].CatalogID)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
	assert.Equal(t, "deepseek-ai/deepseek-v4.1-flash", resolved.Candidates[0].UpstreamID)
}

// The managed policy denies nothing, so every catalog row resolves through
// ai&; the only provider that must never be offered is one the deployment
// has no key for.
func TestManagedResolverUsesTheOnlyRemainingProvider(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek/deepseek-v4-pro", "z-ai/glm-5.3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 2)
	byID := map[string]policy.Candidate{}
	for _, candidate := range resolved.Candidates {
		byID[candidate.CatalogID] = candidate
	}
	require.Contains(t, byID, "deepseek/deepseek-v4-pro")
	assert.Equal(t, providers.ProviderAIAND, byID["deepseek/deepseek-v4-pro"].Provider)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", byID["deepseek/deepseek-v4-pro"].UpstreamID)
	require.Contains(t, byID, "z-ai/glm-5.3")
	assert.Equal(t, providers.ProviderAIAND, byID["z-ai/glm-5.3"].Provider)
	assert.Equal(t, "zai-org/glm-5.3", byID["z-ai/glm-5.3"].UpstreamID)
}


func TestResolverDefaultsUpstreamIDToCatalogID(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "moonshotai/kimi-k3", resolved.Candidates[0].UpstreamID)
	assert.Equal(t, "moonshotai/kimi-k3", resolved.Candidates[0].ModelRevision)
	assert.Equal(t, resolved.Candidates[0].RosterID, resolved.Candidates[0].ArmID)
}

func TestArmResolverEnumeratesTheAllowedProviderBinding(t *testing.T) {
	// ai& is the only upstream and every catalog model carries exactly one
	// binding, so the arm resolver offers exactly one arm per model.
	resolver := policy.NewArmResolver(
		set("z-ai/glm-5.2"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "z-ai/glm-5.2", resolved.Candidates[0].RosterID)
	// The roster ID is unambiguous (one model maps to it), so the resolver
	// can resolve a selection by roster ID.
	require.Contains(t, resolved.ByRosterID, "z-ai/glm-5.2")
	assert.Equal(t, []string{"z-ai/glm-5.2"}, resolved.CandidateModels())
	assert.Equal(t, map[string]string{
		resolved.Candidates[0].ArmID: resolved.Candidates[0].Provider,
	}, resolved.CandidateArmProviders())
	armScores := map[string]float32{
		resolved.Candidates[0].ArmID: 0.1,
	}
	assert.Equal(t, armScores, resolved.ArmCandidateScores(armScores))
	assert.Equal(t, map[string]string{
		resolved.Candidates[0].CatalogID: resolved.Candidates[0].Provider,
	}, resolved.CandidateProviders())
	assert.Equal(t, map[string]float32{
		resolved.Candidates[0].CatalogID: 0.1,
	}, resolved.CatalogCandidateScores(armScores))
	for _, candidate := range resolved.Candidates {
		binding, ok := resolved.BindingForSelection(candidate.ArmID, "")
		require.True(t, ok)
		assert.Equal(t, candidate.Provider, binding.Provider)
		assert.Equal(t, candidate.UpstreamID, binding.UpstreamID)
		assert.Equal(t, candidate.UpstreamID, candidate.ModelRevision)
	}
}

func TestArmResolverRejectsRosterOnlySelection(t *testing.T) {
	// A roster ID that several models share is ambiguous, so the resolver
	// must not resolve a selection by roster ID alone.
	resolver := policy.NewArmResolver(
		set("z-ai/glm-5.2", "moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		func(catalog.Model) string { return "shared/arm" },
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 2)
	assert.Empty(t, resolved.ByRosterID, "an ambiguous roster ID must not be resolvable")
	_, ok := resolved.BindingForSelection("", "shared/arm")
	assert.False(t, ok)
}

func TestResolverAppliesHardFiltersAndPreferenceRanks(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.2"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	// With no enabled provider, no model resolves; the preference order is
	// what the resolver would have ranked them by.
	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set("other-provider"),
		PreferredModels:  []string{"z-ai/glm-5.2", "moonshotai/kimi-k3"},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "z-ai/glm-5.2",
		RosterID:  "z-ai/glm-5.2",
		Reason:    policy.ExclusionNoProvider,
	})
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		RosterID:  "moonshotai/kimi-k3",
		Reason:    policy.ExclusionNoProvider,
	})

	// With ai& wired, both resolve and the preference list ranks them.
	resolved = resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		PreferredModels:  []string{"z-ai/glm-5.2", "moonshotai/kimi-k3"},
	})

	require.Len(t, resolved.Candidates, 2)
	ranks := map[string]int{}
	for _, candidate := range resolved.Candidates {
		require.NotNil(t, candidate.PreferenceRank)
		ranks[candidate.CatalogID] = *candidate.PreferenceRank
	}
	assert.Equal(t, 0, ranks["z-ai/glm-5.2"], "the first preferred model ranks first")
	assert.Equal(t, 1, ranks["moonshotai/kimi-k3"], "the second preferred model ranks second")
}

func TestResolverBuildsMappingOnlyFromFinalSoftFilteredPool(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "deepseek/deepseek-v4-flash"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{HasImages: true})

	assert.Equal(t, []string{"moonshotai/kimi-k3"}, resolved.CandidateModels())
	_, leaked := resolved.ByRosterID["deepseek/deepseek-v4-flash"]
	assert.False(t, leaked)
}

func TestResolverRejectsAmbiguousRosterMappings(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.2"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		func(catalog.Model) string { return "shared/arm" },
	)

	resolved := resolver.Resolve(router.Request{})

	assert.Empty(t, resolved.Candidates)
	assert.Empty(t, resolved.ByRosterID)
	assert.Len(t, resolved.Diagnostics, 2)
}

func TestResolverRejectsCandidatesThatCannotFitEstimatedInput(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("moonshotai/kimi-k3") + 1})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		RosterID:  "moonshotai/kimi-k3",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverAllowsExactContextFit(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("moonshotai/kimi-k3")})

	assert.Equal(t, []string{"moonshotai/kimi-k3"}, resolved.CandidateModels())
	assert.Empty(t, resolved.Diagnostics)
}

func TestResolverIncludesExpectedOutputInContextBudget(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)
	expectedOutputTokens := 2_000

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: catalog.ContextWindowFor("moonshotai/kimi-k3") - 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		RosterID:  "moonshotai/kimi-k3",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverKeepsOverflowAdmittedModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.2"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens:   catalog.ContextWindowFor("moonshotai/kimi-k3") + 1,
		OverflowAdmittedModels: set("moonshotai/kimi-k3"),
	})

	assert.Equal(t, []string{"moonshotai/kimi-k3"}, resolved.CandidateModels(),
		"the provider's exact count decides for a model the proxy admitted on total overflow")
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: "z-ai/glm-5.2",
		RosterID:  "z-ai/glm-5.2",
		Reason:    policy.ExclusionContextWindow,
	}}, resolved.Diagnostics)
}

func TestResolverPreservesUnsignedHistoryExclusionAfterOverflowAdmission(t *testing.T) {
	const model = "z-ai/glm-5.3"
	resolver := policy.NewResolver(
		set(model),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		ExcludedModels:                set(model),
		OverflowAdmittedModels:        set(model),
		UnsignedHistoryExcludedModels: set(model),
	})

	assert.Empty(t, resolved.Candidates)
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: model,
		RosterID:  model,
		Reason:    policy.ExclusionUnsignedHistory,
	}}, resolved.Diagnostics)
}

func TestResolverDoesNotCallUnmappedOverflowCandidateAContextExclusion(t *testing.T) {
	const model = "z-ai/glm-5.3"
	resolver := policy.NewResolver(
		set(model),
		set(providers.ProviderAIAND),
		func(catalog.Model) string { return "" },
	)

	resolved := resolver.Resolve(router.Request{
		ExcludedModels:              set(model),
		ContextWindowExcludedModels: set(model),
	})

	assert.Empty(t, resolved.Candidates)
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: model,
		Reason:    policy.ExclusionUnmappedRoster,
	}}, resolved.Diagnostics)
}

func TestResolverIncludesLiveCandidateEconomics(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)
	expectedOutputTokens := 500

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	require.Len(t, resolved.Candidates, 1)
	candidate := resolved.Candidates[0]
	// kimi-k3: $3.00 input / $12.50 output per 1M, cache read at 0.50/3.00.
	assert.Equal(t, 0.50/3.00, candidate.CacheReadMultiplier)
	assert.Equal(t, 1.0, candidate.MarginalCostFactor)
	assert.Equal(t, 3.0, candidate.EffectiveInputUSDPer1M)
	assert.Equal(t, 12.5, candidate.EffectiveOutputUSDPer1M)
	assert.InDelta(t, candidate.EstimatedCostUSD, candidate.EffectiveEstimatedCostUSD, 1e-12)
}

func TestResolverUsesLongContextPricing(t *testing.T) {
	// No surviving ai& model publishes a long-context tier, so the resolver
	// must report the standard rates for a long prompt.
	resolver := policy.NewResolver(
		set("z-ai/glm-5.2"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)
	expectedOutputTokens := 1_000

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: 300_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	require.Len(t, resolved.Candidates, 1)
	candidate := resolved.Candidates[0]
	assert.Equal(t, 1.00, candidate.InputUSDPer1M)
	assert.Equal(t, 4.00, candidate.OutputUSDPer1M)
}

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// The resolver enforces a positive allowlist before explicit exclusions, so
// diagnostics still distinguish not-allowlisted from admin-excluded models.
func TestResolverReportsNotAllowlistedSeparatelyFromRequestedExclusion(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.3-flash"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		// Both are excluded on the wire; only one is an explicit exclusion.
		ExcludedModels: map[string]struct{}{
			"moonshotai/kimi-k3":  {},
			"z-ai/glm-5.3-flash": {},
		},
		AllowedModels: map[string]struct{}{"z-ai/glm-5.3-flash": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		Reason:    policy.ExclusionNotAllowlisted,
	}, "a model absent from the allowlist must be reported as not-allowlisted")
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "z-ai/glm-5.3-flash",
		Reason:    policy.ExclusionRequested,
	}, "an allowlisted model excluded explicitly stays a requested exclusion")
}

// Without an allowlist configured, exclusion diagnostics must be unchanged.
func TestResolverKeepsRequestedExclusionWhenNoAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		ExcludedModels: map[string]struct{}{"moonshotai/kimi-k3": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		Reason:    policy.ExclusionRequested,
	})
}

func TestResolverDropsAutomaticallyDisabledModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.3-flash"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"moonshotai/kimi-k3": {}},
	})

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, resolved.CandidateModels())
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		RosterID:  "moonshotai/kimi-k3",
		Reason:    policy.ExclusionAutomaticDisabled,
	})
}

// Soft filter: disabling every candidate leaves the pool intact rather than
// failing the turn, because the models remain reachable through a user pin.
func TestResolverKeepsPoolWhenAutomaticDisablesWouldEmptyIt(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"moonshotai/kimi-k3": {}},
	})

	assert.Equal(t, []string{"moonshotai/kimi-k3"}, resolved.CandidateModels())
}

// A session demotion (AutomaticExcludedModels) is bounded by the
// installation's allowlist in both directions: it can only move the pick to a
// model the allowlist already admits, and when the allowlist admits nothing
// else the demoted model is kept rather than the pool widened.
func TestResolverSessionDemotionStaysInsideAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3", "z-ai/glm-5.2", "z-ai/glm-5.2"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
	)

	t.Run("allowlist admits an Anthropic sibling", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("moonshotai/kimi-k3", "z-ai/glm-5.2"),
			AutomaticExcludedModels: set("moonshotai/kimi-k3"),
		})

		assert.Equal(t, []string{"z-ai/glm-5.2"}, resolved.CandidateModels())
	})

	t.Run("allowlist admits only the demoted model", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("moonshotai/kimi-k3"),
			AutomaticExcludedModels: set("moonshotai/kimi-k3"),
		})

		assert.Equal(t, []string{"moonshotai/kimi-k3"}, resolved.CandidateModels(),
			"the demotion must not admit a cross-vendor model the allowlist excludes")
		assert.NotContains(t, resolved.CandidateModels(), "z-ai/glm-5.2")
	})
}

func TestResolverDirectlyEnforcesAllowlistForStrategySpecificCandidates(t *testing.T) {
	resolver := policy.NewResolver(
		set("z-ai/glm-5.3-flash"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{
		AllowedModels: map[string]struct{}{"z-ai/glm-5.3-flash": {}},
	})

	// The allowlisted model resolves; the resolver enforces the allowlist
	// directly rather than letting a strategy candidate slip past it.
	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "z-ai/glm-5.3-flash", resolved.Candidates[0].CatalogID)

	resolved = resolver.Resolve(router.Request{
		AllowedModels: map[string]struct{}{"z-ai/glm-5.2": {}},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "z-ai/glm-5.3-flash",
		Reason:    policy.ExclusionNotAllowlisted,
	})
}

// Effort-qualified arm/roster selections must resolve via base ID and propagate the effort level.
func TestBindingForSelectionResolvesEffortQualifiedArmID(t *testing.T) {
	resolved := policy.ResolvedCandidates{
		ByArmID: map[string]policy.Binding{
			"moonshotai/kimi-k3": {ArmID: "moonshotai/kimi-k3", CatalogID: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"moonshotai/kimi-k3": {ArmID: "moonshotai/kimi-k3", CatalogID: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND},
		},
	}

	for _, tc := range []struct {
		name       string
		armID      string
		rosterID   string
		wantFound  bool
		wantEffort string
	}{
		{name: "effort-qualified arm id", armID: "moonshotai/kimi-k3:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "effort-qualified roster id", rosterID: "moonshotai/kimi-k3:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "bare arm id", armID: "moonshotai/kimi-k3", wantFound: true, wantEffort: ""},
		{name: "unknown arm id", armID: "unknown/model", wantFound: false, wantEffort: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, ok := resolved.BindingForSelection(tc.armID, tc.rosterID)
			assert.Equal(t, tc.wantFound, ok)
			if tc.wantFound {
				assert.Equal(t, "moonshotai/kimi-k3", binding.CatalogID)
				assert.Equal(t, tc.wantEffort, binding.Effort)
			}
		})
	}
}

// splitEffort drops only recognized effort suffixes, so a non-effort ":"
// suffix misses the base-keyed maps rather than misrouting to the base.
func TestBindingForSelectionDoesNotResolveNonEffortColonSuffix(t *testing.T) {
	resolved := policy.ResolvedCandidates{
		ByArmID: map[string]policy.Binding{
			"moonshotai/kimi-k3": {CatalogID: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"moonshotai/kimi-k3": {CatalogID: "moonshotai/kimi-k3", Provider: providers.ProviderAIAND},
		},
	}

	_, ok := resolved.BindingForSelection("anthropic/claude-opus-5:custom", "anthropic/claude-opus-5:custom")
	assert.False(t, ok, "a non-effort colon suffix must not be stripped to reach the base-keyed binding")
}

func TestResolverKeepsVendorRoutingWhenNoGatewayConfigured(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
}
