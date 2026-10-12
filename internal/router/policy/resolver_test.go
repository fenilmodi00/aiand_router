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

func TestManagedResolverOffersEveryBindingOfAnEnabledProvider(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3"),
		set(providers.ProviderAIAND, providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAnthropic),
		CustomBindings:   map[string][]string{"deepseek-ai/deepseek-v4-pro": {providers.ProviderAnthropic}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].CatalogID)
	assert.Equal(t, providers.ProviderAnthropic, resolved.Candidates[0].Provider)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].UpstreamID)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "zai-org/glm-5.3",
		RosterID:  "zai-org/glm-5.3",
		Reason:    policy.ExclusionNoProvider,
	})
}

// ManagedProviderPolicy denies nothing after the AIand-only cut; an explicit
// denial still removes the only provider that could serve the model.
func TestResolverReportsPolicyDeniedProvider(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ProviderPolicy{Denied: set(providers.ProviderAIAND)},
	)

	resolved := resolver.Resolve(router.Request{})

	assert.Empty(t, resolved.Candidates)
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		RosterID:  "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionProviderPolicy,
	}}, resolved.Diagnostics)
}

func TestResolverDefaultsUpstreamIDToCatalogID(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].UpstreamID)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].ModelRevision)
	assert.Equal(t, resolved.Candidates[0].RosterID, resolved.Candidates[0].ArmID)
}

func TestArmResolverEnumeratesEachAllowedProviderBinding(t *testing.T) {
	resolver := policy.NewArmResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND, providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	// No surviving catalog row carries two providers, so the second binding is
	// installation-declared.
	resolved := resolver.Resolve(router.Request{CustomBindings: map[string][]string{
		"deepseek-ai/deepseek-v4-pro": {providers.ProviderAnthropic},
	}})

	require.Len(t, resolved.Candidates, 2)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].RosterID)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[1].RosterID)
	assert.NotEqual(t, resolved.Candidates[0].ArmID, resolved.Candidates[1].ArmID)
	assert.Empty(t, resolved.ByRosterID)
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, resolved.CandidateModels())
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

func TestArmResolverRejectsRosterOnlySelectionForMultipleBindings(t *testing.T) {
	// The AIand-only catalog binds every row once, so a second arm for the same
	// model can only come from an installation-declared Anthropic binding.
	resolver := policy.NewArmResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND, providers.ProviderAnthropic),
		func(catalog.Model) string { return "shared/arm" },
		policy.ProviderPolicy{},
	)

	resolved := resolver.Resolve(router.Request{CustomBindings: map[string][]string{
		"deepseek-ai/deepseek-v4-pro": {providers.ProviderAnthropic},
	}})

	require.Len(t, resolved.Candidates, 2)
	assert.Empty(t, resolved.ByRosterID)
	_, ok := resolved.BindingForSelection("", "shared/arm")
	assert.False(t, ok)
}

func TestResolverAppliesHardFiltersAndPreferenceRanks(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3"),
		set(providers.ProviderAIAND, providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAnthropic),
		PreferredModels:  []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro"},
		CustomBindings:   map[string][]string{"deepseek-ai/deepseek-v4-pro": {providers.ProviderAnthropic}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].CatalogID)
	require.NotNil(t, resolved.Candidates[0].PreferenceRank)
	assert.Equal(t, 1, *resolved.Candidates[0].PreferenceRank)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "zai-org/glm-5.3",
		RosterID:  "zai-org/glm-5.3",
		Reason:    policy.ExclusionNoProvider,
	})
}

func TestResolverBuildsMappingOnlyFromFinalSoftFilteredPool(t *testing.T) {
	resolver := policy.NewResolver(
		set("qwen/qwen3.8-27b", "deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{HasImages: true})

	assert.Equal(t, []string{"qwen/qwen3.8-27b"}, resolved.CandidateModels())
	_, leaked := resolved.ByRosterID["deepseek-ai/deepseek-v4-pro"]
	assert.False(t, leaked)
}

func TestResolverRejectsAmbiguousRosterMappings(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3"),
		set(providers.ProviderAIAND),
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
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("deepseek-ai/deepseek-v4-pro") + 1})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		RosterID:  "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverAllowsExactContextFit(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{EstimatedInputTokens: catalog.ContextWindowFor("deepseek-ai/deepseek-v4-pro")})

	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, resolved.CandidateModels())
	assert.Empty(t, resolved.Diagnostics)
}

func TestResolverIncludesExpectedOutputInContextBudget(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	expectedOutputTokens := 2_000

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: catalog.ContextWindowFor("deepseek-ai/deepseek-v4-pro") - 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		RosterID:  "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionContextWindow,
	})
}

func TestResolverKeepsOverflowAdmittedModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens:   catalog.ContextWindowFor("deepseek-ai/deepseek-v4-pro") + 1,
		OverflowAdmittedModels: set("deepseek-ai/deepseek-v4-pro"),
	})

	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, resolved.CandidateModels(),
		"the provider's exact count decides for a model the proxy admitted on total overflow")
	assert.Equal(t, []policy.Diagnostic{{
		CatalogID: "moonshotai/kimi-k3",
		RosterID:  "moonshotai/kimi-k3",
		Reason:    policy.ExclusionContextWindow,
	}}, resolved.Diagnostics)
}

func TestResolverPreservesUnsignedHistoryExclusionAfterOverflowAdmission(t *testing.T) {
	const model = "deepseek-ai/deepseek-v4-pro"
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
	const model = "deepseek-ai/deepseek-v4-pro"
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
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)
	expectedOutputTokens := 500

	resolved := resolver.Resolve(router.Request{
		EstimatedInputTokens: 1_000,
		RoutingKnobs:         &router.Overrides{ExpectedOutputTokens: &expectedOutputTokens},
		SubsidizedModelCostFactor: map[string]float64{
			"deepseek-ai/deepseek-v4-pro": 0.25,
		},
	})

	require.Len(t, resolved.Candidates, 1)
	candidate := resolved.Candidates[0]
	assert.Equal(t, 0.25, candidate.CacheReadMultiplier)
	assert.Equal(t, 0.25, candidate.MarginalCostFactor)
	assert.Equal(t, 0.25, candidate.EffectiveInputUSDPer1M)
	assert.Equal(t, 0.625, candidate.EffectiveOutputUSDPer1M)
	assert.InDelta(t, candidate.EstimatedCostUSD*0.25, candidate.EffectiveEstimatedCostUSD, 1e-12)
}

// NOTE: no surviving catalog row carries a long-context pricing tier, so the
// long-context resolution path is unreachable through real catalog data and its
// fixture is gone.

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
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3-flash"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		// Both are excluded on the wire; only one is an explicit exclusion.
		ExcludedModels: map[string]struct{}{
			"deepseek-ai/deepseek-v4-pro": {},
			"zai-org/glm-5.3-flash":       {},
		},
		AllowedModels: map[string]struct{}{"zai-org/glm-5.3-flash": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionNotAllowlisted,
	}, "a model absent from the allowlist must be reported as not-allowlisted")
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "zai-org/glm-5.3-flash",
		Reason:    policy.ExclusionRequested,
	}, "an allowlisted model excluded explicitly stays a requested exclusion")
}

// Without an allowlist configured, exclusion diagnostics must be unchanged.
func TestResolverKeepsRequestedExclusionWhenNoAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		ExcludedModels: map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}},
	})

	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionRequested,
	})
}

func TestResolverDropsAutomaticallyDisabledModels(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3-flash"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}},
	})

	assert.Equal(t, []string{"zai-org/glm-5.3-flash"}, resolved.CandidateModels())
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "deepseek-ai/deepseek-v4-pro",
		RosterID:  "deepseek-ai/deepseek-v4-pro",
		Reason:    policy.ExclusionAutomaticDisabled,
	})
}

// Soft filter: disabling every candidate leaves the pool intact rather than
// failing the turn, because the models remain reachable through a user pin.
func TestResolverKeepsPoolWhenAutomaticDisablesWouldEmptyIt(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AutomaticExcludedModels: map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}},
	})

	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, resolved.CandidateModels())
}

// A session demotion (AutomaticExcludedModels) is bounded by the
// installation's allowlist in both directions: it can only move the pick to a
// model the allowlist already admits, and when the allowlist admits nothing
// else the demoted model is kept rather than the pool widened.
func TestResolverSessionDemotionStaysInsideAllowlist(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b", "zai-org/glm-5.3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	t.Run("allowlist admits a sibling", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b"),
			AutomaticExcludedModels: set("deepseek-ai/deepseek-v4-pro"),
		})

		assert.Equal(t, []string{"qwen/qwen3.8-27b"}, resolved.CandidateModels())
	})

	t.Run("allowlist admits only the demoted model", func(t *testing.T) {
		resolved := resolver.Resolve(router.Request{
			AllowedModels:           set("deepseek-ai/deepseek-v4-pro"),
			AutomaticExcludedModels: set("deepseek-ai/deepseek-v4-pro"),
		})

		assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, resolved.CandidateModels(),
			"the demotion must not admit a model the allowlist excludes")
		assert.NotContains(t, resolved.CandidateModels(), "zai-org/glm-5.3")
	})
}

func TestResolverDirectlyEnforcesAllowlistForStrategySpecificCandidates(t *testing.T) {
	resolver := policy.NewResolver(
		set("moonshotai/kimi-k3"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		AllowedModels: map[string]struct{}{"zai-org/glm-5.3-flash": {}},
	})

	assert.Empty(t, resolved.Candidates)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "moonshotai/kimi-k3",
		Reason:    policy.ExclusionNotAllowlisted,
	})
}

// Effort-qualified arm/roster selections must resolve via base ID and propagate the effort level.
func TestBindingForSelectionResolvesEffortQualifiedArmID(t *testing.T) {
	resolved := policy.ResolvedCandidates{
		ByArmID: map[string]policy.Binding{
			"deepseek-ai/deepseek-v4-pro": {ArmID: "deepseek-ai/deepseek-v4-pro", CatalogID: "deepseek-ai/deepseek-v4-pro", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"deepseek-ai/deepseek-v4-pro": {ArmID: "deepseek-ai/deepseek-v4-pro", CatalogID: "deepseek-ai/deepseek-v4-pro", Provider: providers.ProviderAIAND},
		},
	}

	for _, tc := range []struct {
		name       string
		armID      string
		rosterID   string
		wantFound  bool
		wantEffort string
	}{
		{name: "effort-qualified arm id", armID: "deepseek-ai/deepseek-v4-pro:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "effort-qualified roster id", rosterID: "deepseek-ai/deepseek-v4-pro:xhigh", wantFound: true, wantEffort: "xhigh"},
		{name: "bare arm id", armID: "deepseek-ai/deepseek-v4-pro", wantFound: true, wantEffort: ""},
		{name: "unknown arm id", armID: "unknown/model", wantFound: false, wantEffort: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, ok := resolved.BindingForSelection(tc.armID, tc.rosterID)
			assert.Equal(t, tc.wantFound, ok)
			if tc.wantFound {
				assert.Equal(t, "deepseek-ai/deepseek-v4-pro", binding.CatalogID)
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
			"deepseek-ai/deepseek-v4-pro": {CatalogID: "deepseek-ai/deepseek-v4-pro", Provider: providers.ProviderAIAND},
		},
		ByRosterID: map[string]policy.Binding{
			"deepseek-ai/deepseek-v4-pro": {CatalogID: "deepseek-ai/deepseek-v4-pro", Provider: providers.ProviderAIAND},
		},
	}

	_, ok := resolved.BindingForSelection("deepseek-ai/deepseek-v4-pro:custom", "deepseek-ai/deepseek-v4-pro:custom")
	assert.False(t, ok, "a non-effort colon suffix must not be stripped to reach the base-keyed binding")
}

func TestResolverRoutesOnlyGatewayAliasedModelsWhenGatewayConfigured(t *testing.T) {
	// req.GatewayProviders is the installation's exclusively-routed provider
	// set; the AIand-only build ships no dedicated gateway provider, so a vendor
	// name stands in.
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b", "zai-org/glm-5.3"),
		set(providers.ProviderAnthropic, providers.ProviderAIAND, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND),
		CustomBindings:   map[string][]string{"deepseek-ai/deepseek-v4-pro": {providers.ProviderAIAND}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", resolved.Candidates[0].CatalogID)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "qwen/qwen3.8-27b",
		RosterID:  "qwen/qwen3.8-27b",
		Reason:    policy.ExclusionGatewayNotServed,
	})
	assert.Contains(t, resolved.Diagnostics, policy.Diagnostic{
		CatalogID: "zai-org/glm-5.3",
		RosterID:  "zai-org/glm-5.3",
		Reason:    policy.ExclusionGatewayNotServed,
	})
}

func TestResolverIgnoresProviderExclusionsForGatewayRouting(t *testing.T) {
	// The org that broke prod excluded every vendor to force its gateway; with
	// gateway routing those exclusions must not touch the gateway's own models.
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAnthropic, providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND),
		GatewayProviders: set(providers.ProviderAIAND),
		CustomBindings:   map[string][]string{"deepseek-ai/deepseek-v4-pro": {providers.ProviderAIAND}},
	})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
}

func TestResolverYieldsNoCandidatesWhenGatewayKeysHaveNoAliases(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro", "zai-org/glm-5.3"),
		set(providers.ProviderAnthropic, providers.ProviderAIAND),
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
	// Every catalog row binds only AIand, so the second per-model arm needs an
	// installation-declared Anthropic binding alongside it.
	resolver := policy.NewArmResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND, providers.ProviderAnthropic),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{
		EnabledProviders: set(providers.ProviderAIAND, providers.ProviderAnthropic),
		GatewayProviders: set(providers.ProviderAIAND, providers.ProviderAnthropic),
		CustomBindings: map[string][]string{
			"deepseek-ai/deepseek-v4-pro": {providers.ProviderAIAND, providers.ProviderAnthropic},
		},
	})

	require.Len(t, resolved.Candidates, 2)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
	assert.Equal(t, providers.ProviderAnthropic, resolved.Candidates[1].Provider)
}

func TestResolverKeepsVendorRoutingWhenNoGatewayConfigured(t *testing.T) {
	resolver := policy.NewResolver(
		set("deepseek-ai/deepseek-v4-pro"),
		set(providers.ProviderAIAND),
		catalogRosterID,
		policy.ManagedProviderPolicy(),
	)

	resolved := resolver.Resolve(router.Request{})

	require.Len(t, resolved.Candidates, 1)
	assert.Equal(t, providers.ProviderAIAND, resolved.Candidates[0].Provider)
}
