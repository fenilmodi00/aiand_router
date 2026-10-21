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
		policy.ManagedProviderPolicy(),
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
		policy.ManagedProviderPolicy(),
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

// A model whose only binding is policy-denied is reported as a provider
// policy exclusion, not as a missing provider.
func TestResolverExcludesModelsWhoseOnlyProviderIsPolicyDenied(t *testing.T) {
	resolver := policy.NewResolver(
		set("z-ai/glm-5.3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ProviderPolicy{Denied: map[string]struct{}{providers.ProviderAIAND: {}}},
	)

	resolved := resolver.Resolve(router.Request{})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "z-ai/glm-5.3",
		RosterID:  "z-ai/glm-5.3",
		Reason:    policy.ExclusionProviderPolicy,
	})
}

func TestResolverDefaultsUpstreamIDToCatalogID(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "claude-opus-4-8", resolved.Candidates[0].UpstreamID)
	assert.Equal(t, "claude-opus-4-8", resolved.Candidates[0].ModelRevision)
	assert.Equal(t, resolved.Candidates[0].RosterID, resolved.Candidates[0].ArmID)
}

func TestArmResolverEnumeratesEachAllowedProviderBinding(t *testing.T) {
	resolver := policy.NewArmResolver(
		set("minimax/minimax-m2.7"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 2)
	assert.Equal(t, "minimax/minimax-m2.7", resolved.Candidates[0].RosterID)
	assert.Equal(t, "minimax/minimax-m2.7", resolved.Candidates[1].RosterID)
	assert.NotEqual(t, resolved.Candidates[0].ArmID, resolved.Candidates[1].ArmID)
	assert.Empty(t, resolved.ByRosterID)
	assert.Equal(t, []string{"minimax/minimax-m2.7"}, resolved.CandidateModels())
	assert.Equal(t, map[string]string{
		resolved.Candidates[0].ArmID: resolved.Candidates[0].Provider,
		resolved.Candidates[1].ArmID: resolved.Candidates[1].Provider,
	}, resolved.CandidateArmProviders())
	armScores := map[string]float32{
		resolved.Candidates[0].ArmID: 0.1,
		resolved.Candidates[1].ArmID: 0.2,
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

func TestArmResolverRejectsRosterOnlySelectionForThreeBindings(t *testing.T) {
	resolver := policy.NewArmResolver(
		set("minimax/minimax-m2.7"),
		set(
			providers.ProviderAIAND,
			providers.ProviderAIAND,
			providers.ProviderAIAND,
		),
		func(catalog.Model) string { return "shared/arm" },
		policy.ProviderPolicy{},
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 3)
	assert.Empty(t, resolved.ByRosterID)
	_, ok := resolved.BindingForSelection("", "shared/arm")
	assert.False(t, ok)
}

func TestResolverAppliesHardFiltersAndPreferenceRanks(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "gpt-5.5"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		PreferredModels:  []string{"gpt-5.5", "claude-opus-4-8"},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "claude-opus-4-8", resolved.Candidates[0].CatalogID)
	require.NotNil(t, resolved.Candidates[0].PreferenceRank)
	assert.Equal(t, 1, *resolved.Candidates[0].PreferenceRank)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "gpt-5.5",
		RosterID:  "gpt-5.5",
		Reason:    policy.ExclusionNoProvider,
	})
}

func TestResolverBuildsMappingOnlyFromFinalSoftFilteredPool(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "deepseek/deepseek-v4-flash"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{HasImages: true})

	assert.Equal(t, []string{"claude-opus-4-8"}, resolved.CandidateModels())
	_, leaked := resolved.ByRosterID["deepseek/deepseek-v4-flash"]
	assert.False(t, leaked)
}

func TestResolverRejectsAmbiguousRosterMappings(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "gpt-5.5"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		func(catalog.Model) string { return "shared/arm" },
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	assert.Empty(t, resolved.Candidates)
	assert.Empty(t, resolved.ByRosterID)
	assert.Len(t, resolved.Diagnostics, 2)
}

func TestResolverRejectsCandidatesThatCannotFitEstimatedInput(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("claude-opus-4-8") + 1})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-opus-4-8",
		RosterID:  "claude-opus-4-8",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverAllowsExactContextFit(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("claude-opus-4-8")})

	assert.Equal(t, []string{"claude-opus-4-8"}, resolved.CandidateModels())
	assert.Empty(t, resolved.Diagnostics)
}

func TestResolverIncludesExpectedOutputInContextBudget(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	expectedOutputTokens := 2_000

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: catalog.ContextWindowFor("claude-opus-4-8") - 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-opus-4-8",
		RosterID:  "claude-opus-4-8",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverKeepsOverflowAdmittedModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-4-6"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens:   catalog.ContextWindowFor("claude-opus-4-8") + 1,
		OverflowAdmittedModels: set("claude-opus-4-8"),
	})

	assert.Equal(t, []string{"claude-opus-4-8"}, resolved.CandidateModels(),
		"the provider's exact count decides for a model the proxy admitted on total overflow")
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: "claude-sonnet-4-6",
		RosterID:  "claude-sonnet-4-6",
		Reason:    policy.ExclusionContextWindow,
	}}, resolved.Diagnostics)
}

func TestResolverPreservesUnsignedHistoryExclusionAfterOverflowAdmission(t *testing.T) {
	const model = "gemini-3.1-pro-preview"
	resolver := policy.NewResolver(
		set(model),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
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
	const model = "gemini-3.1-pro-preview"
	resolver := policy.NewResolver(
		set(model),
		set(providers.ProviderAIAND),
		func(catalog.Model) string { return "" },
		policy.ManagedProviderPolicy(),
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
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	expectedOutputTokens := 500

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	require.Len(t, resolved.Candidates, 1)
	candidate := resolved.Candidates[0]
	assert.Equal(t, 0.1, candidate.CacheReadMultiplier)
	assert.Equal(t, 1.0, candidate.MarginalCostFactor)
	assert.Equal(t, 5.0, candidate.EffectiveInputUSDPer1M)
	assert.Equal(t, 25.0, candidate.EffectiveOutputUSDPer1M)
	assert.InDelta(t, candidate.EstimatedCostUSD, candidate.EffectiveEstimatedCostUSD, 1e-12)
}

func TestResolverUsesLongContextPricing(t *testing.T) {
	resolver := policy.NewResolver(
		set("gpt-5.6-luna"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	expectedOutputTokens := 1_000

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: 300_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	require.Len(t, resolved.Candidates, 1)
	candidate := resolved.Candidates[0]
	assert.Equal(t, 0.40, candidate.InputUSDPer1M)
	assert.Equal(t, 1.80, candidate.OutputUSDPer1M)
	assert.InDelta(t, 0.1218, candidate.EstimatedCostUSD, 1e-12)
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
		set("claude-opus-4-8", "claude-haiku-4-5"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		// Both are excluded on the wire; only one is an explicit exclusion.
		ExcludedModels: map[string]struct{}{
			"claude-opus-4-8":  {},
			"claude-haiku-4-5": {},
		},
		AllowedModels: map[string]struct{}{"claude-haiku-4-5": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-opus-4-8",
		Reason:    policy.ExclusionNotAllowlisted,
	}, "a model absent from the allowlist must be reported as not-allowlisted")
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-haiku-4-5",
		Reason:    policy.ExclusionRequested,
	}, "an allowlisted model excluded explicitly stays a requested exclusion")
}

// Without an allowlist configured, exclusion diagnostics must be unchanged.
func TestResolverKeepsRequestedExclusionWhenNoAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		ExcludedModels: map[string]struct{}{"claude-opus-4-8": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-opus-4-8",
		Reason:    policy.ExclusionRequested,
	})
}

func TestResolverDropsAutomaticallyDisabledModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-haiku-4-5"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"claude-opus-4-8": {}},
	})

	assert.Equal(t, []string{"claude-haiku-4-5"}, resolved.CandidateModels())
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-opus-4-8",
		RosterID:  "claude-opus-4-8",
		Reason:    policy.ExclusionAutomaticDisabled,
	})
}

// Soft filter: disabling every candidate leaves the pool intact rather than
// failing the turn, because the models remain reachable through a user pin.
func TestResolverKeepsPoolWhenAutomaticDisablesWouldEmptyIt(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"claude-opus-4-8": {}},
	})

	assert.Equal(t, []string{"claude-opus-4-8"}, resolved.CandidateModels())
}

// A session demotion (AutomaticExcludedModels) is bounded by the
// installation's allowlist in both directions: it can only move the pick to a
// model the allowlist already admits, and when the allowlist admits nothing
// else the demoted model is kept rather than the pool widened.
func TestResolverSessionDemotionStaysInsideAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-5", "gpt-5.6-luna"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	t.Run("allowlist admits an Anthropic sibling", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("claude-opus-4-8", "claude-sonnet-5"),
			AutomaticExcludedModels: set("claude-opus-4-8"),
		})

		assert.Equal(t, []string{"claude-sonnet-5"}, resolved.CandidateModels())
	})

	t.Run("allowlist admits only the demoted model", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("claude-opus-4-8"),
			AutomaticExcludedModels: set("claude-opus-4-8"),
		})

		assert.Equal(t, []string{"claude-opus-4-8"}, resolved.CandidateModels(),
			"the demotion must not admit a cross-vendor model the allowlist excludes")
		assert.NotContains(t, resolved.CandidateModels(), "gpt-5.6-luna")
	})
}

func TestResolverDirectlyEnforcesAllowlistForStrategySpecificCandidates(t *testing.T) {
	resolver := policy.NewResolver(
		set("gpt-5.6-luna-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AllowedModels: map[string]struct{}{"claude-haiku-4-5": {}},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "gpt-5.6-luna-pro",
		Reason:    policy.ExclusionNotAllowlisted,
	})
}

// Effort-qualified arm/roster selections must resolve via base ID and propagate the effort level.
func TestBindingForSelectionResolvesEffortQualifiedArmID(t *testing.T) {
	resolved := policy.ResolvedCandidates{
		ByArmID: map[string]policy.Binding{
			"anthropic/claude-opus-5": {ArmID: "anthropic/claude-opus-5", CatalogID: "claude-opus-5", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"anthropic/claude-opus-5": {ArmID: "anthropic/claude-opus-5", CatalogID: "claude-opus-5", Provider: providers.ProviderAIAND},
		},
	}

	for _, tc := range []struct {
		name       string
		armID      string
		rosterID   string
		wantFound  bool
		wantEffort string
	}{
		{name: "effort-qualified arm id", armID: "anthropic/claude-opus-5:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "effort-qualified roster id", rosterID: "anthropic/claude-opus-5:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "bare arm id", armID: "anthropic/claude-opus-5", wantFound: true, wantEffort: ""},
		{name: "unknown arm id", armID: "unknown/model", wantFound: false, wantEffort: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, ok := resolved.BindingForSelection(tc.armID, tc.rosterID)
			assert.Equal(t, tc.wantFound, ok)
			if tc.wantFound {
				assert.Equal(t, "claude-opus-5", binding.CatalogID)
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
			"anthropic/claude-opus-5": {CatalogID: "claude-opus-5", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"anthropic/claude-opus-5": {CatalogID: "claude-opus-5", Provider: providers.ProviderAIAND},
		},
	}

	_, ok := resolved.BindingForSelection("anthropic/claude-opus-5:custom", "anthropic/claude-opus-5:custom")
	assert.False(t, ok, "a non-effort colon suffix must not be stripped to reach the base-keyed binding")
}

func TestResolverRoutesOnlyGatewayAliasedModelsWhenGatewayConfigured(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-5", "claude-sonnet-5", "gpt-5.5"),
		set(providers.ProviderAIAND, providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND),
		CustomBindings:   map[string][]string{"claude-opus-5": {providers.ProviderAIAND}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "claude-opus-5", resolved.Candidates[0].CatalogID)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "claude-sonnet-5",
		RosterID:  "claude-sonnet-5",
		Reason:    policy.ExclusionGatewayNotServed,
	})
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "gpt-5.5",
		RosterID:  "gpt-5.5",
		Reason:    policy.ExclusionGatewayNotServed,
	})
}

func TestResolverIgnoresProviderExclusionsForGatewayRouting(t *testing.T) {
	// The org that broke prod excluded every vendor to force its gateway; with
	// gateway routing those exclusions must not touch the gateway's own models.
	resolver := policy.NewResolver(
		set("claude-opus-5"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND),
		CustomBindings:   map[string][]string{"claude-opus-5": {providers.ProviderAIAND}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
}

func TestResolverYieldsNoCandidatesWhenGatewayKeysHaveNoAliases(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-5", "gpt-5.5"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND),
	})

	assert.Empty(t, resolved.Candidates)
	for _, diagnostic := range resolved.Diagnostics {
		assert.Equal(t, policy.ExclusionGatewayNotServed, diagnostic.Reason)
	}
}

func TestResolverEnumeratesEveryAliasingGatewayForAModel(t *testing.T) {
	resolver := policy.NewArmResolver(
		set("claude-opus-5"),
		set(providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND, providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND, providers.ProviderAIAND),
		CustomBindings: map[string][]string{
			"claude-opus-5": {providers.ProviderAIAND, providers.ProviderAIAND},
		},
	})

	require.Len(t, resolved.Candidates, 2)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[1].Provider)
}

func TestResolverKeepsVendorRoutingWhenNoGatewayConfigured(t *testing.T) {
	resolver := policy.NewResolver(
		set("claude-opus-5"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
}
