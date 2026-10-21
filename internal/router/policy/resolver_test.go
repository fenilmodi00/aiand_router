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

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
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
