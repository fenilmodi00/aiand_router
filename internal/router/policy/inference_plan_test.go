package policy_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

func TestPlanResolverPreservesExistingSidecarBinding(t *testing.T) {
	candidateResolver := policy.NewResolver(
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
		func(model catalog.Model) string { return "roster/" + model.ID },
	)
	routingRequest := router.Request{}
	candidates := candidateResolver.Resolve(routingRequest)
	require.NotEmpty(t, candidates.Candidates)
	selectedCandidate := candidates.Candidates[0]
	existingBinding, found := candidates.BindingForSelection(selectedCandidate.ArmID, selectedCandidate.RosterID)
	require.True(t, found)

	planResolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), candidateResolver)
	require.NoError(t, err)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: routingRequest,
		Selection: policy.CandidateSelection{
			ArmID:    selectedCandidate.ArmID,
			RosterID: selectedCandidate.RosterID,
		},
	})
	require.NoError(t, err)

	assert.Equal(t, existingBinding, plan.SelectedBinding())
	assert.Equal(t, policy.DefaultRegistry().Revision(), plan.RegistryRevision())
	assert.Equal(t, policy.SelectionStrategyRouter, plan.Provenance().SelectionStrategy)
}

func TestPlanResolverAppliesTypedOverridePrecedence(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceInstallation, CatalogID: "z-ai/glm-5.2"},
			{Source: policy.OverrideSourceRequest, CatalogID: "z-ai/glm-5.3-flash"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "z-ai/glm-5.3-flash", plan.SelectedBinding().CatalogID)
	assert.Equal(t, policy.OverrideSourceRequest, plan.Provenance().OverrideSource)
}

func TestPlanResolverRejectsInvalidExplicitOverrideWithoutSubstitution(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceRequest, CatalogID: "not-a-catalog-model"},
			{Source: policy.OverrideSourceInstallation, CatalogID: "z-ai/glm-5.2"},
		},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRejectsDisallowedAndDuplicateOverrideSources(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
	)

	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourceClientAuthoritative, CatalogID: "z-ai/glm-5.3-flash",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorOverrideNotAllowed)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceRequest, CatalogID: "z-ai/glm-5.3-flash"},
			{Source: policy.OverrideSourceRequest, CatalogID: "z-ai/glm-5.3-flash"},
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorDuplicateOverride)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourcePolicyDefault, CatalogID: "z-ai/glm-5.3-flash",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRequiresTypedForceModelOverride(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: router.Request{ForceModel: "z-ai/glm-5.3-flash"},
		Selection:     policy.CandidateSelection{RosterID: "z-ai/glm-5.3-flash"},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRestrictsFixedPolicyToReviewedModels(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", plan.SelectedBinding().CatalogID)

	_, err = planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeHandoverSummary,
		Overrides: []policy.TargetOverride{{
			Source: policy.OverrideSourceDeployment, CatalogID: "z-ai/glm-5.2",
		}},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverDeclaresOnlyEligibleBindingFallbacks(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{
			RosterID: "z-ai/glm-5.3-flash",
		},
	})
	require.NoError(t, err)

	// ai& is the only upstream, so the selected binding has no eligible
	// alternative to fall back to.
	assert.Equal(t, "z-ai/glm-5.3-flash", plan.SelectedBinding().CatalogID)
	assert.Equal(t, providers.ProviderAIAND, plan.SelectedBinding().Provider)
	assert.Empty(t, plan.AlternativeBindings())
}

func TestPlanResolverResolvesDeclaredModelFallbacks(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"z-ai/glm-5.2"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
		func(model catalog.Model) string { return model.ID },
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	require.Len(t, plan.AlternativeBindings(), 1)
	assert.Equal(t, "z-ai/glm-5.2", plan.AlternativeBindings()[0].CatalogID)
}

func TestPlanResolverReportsMissingFixedTargetAsIneligible(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"z-ai/glm-5.2"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND),
		func(model catalog.Model) string { return model.ID },
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	_, err = planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorNoEligibleBinding)
}

func TestPlanResolverEnforcesBudgetEnvelopeAndSpendCap(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: router.Request{EstimatedInputTokens: 1_000},
		Selection:     policy.CandidateSelection{RosterID: "z-ai/glm-5.3-flash"},
		Budget: &policy.BudgetOverride{
			Source:      policy.BudgetSourceRequest,
			MaxSpendUSD: 0.0001,
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorBudgetViolation)

	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeAnthropicMessages)
	specs[index].Budget.TimeoutMillis = 8_000
	registry, registryErr := policy.NewRegistry(specs)
	require.NoError(t, registryErr)
	candidateResolver := policy.NewResolver(
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
		func(model catalog.Model) string { return model.ID },
	)
	boundedPlanResolver, resolverErr := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, resolverErr)
	_, err = boundedPlanResolver.Resolve(policy.ResolutionRequest{
		Purpose:   policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{RosterID: "z-ai/glm-5.3-flash"},
		Budget: &policy.BudgetOverride{
			Source:        policy.BudgetSourceRequest,
			TimeoutMillis: 9_000,
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorBudgetViolation)
}

func TestResolvedPlanReturnsImmutableCopies(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
	)
	plan, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:   policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{RosterID: "z-ai/glm-5.3-flash"},
	})
	require.NoError(t, err)

	constraints := plan.HardConstraints()
	constraints[0] = policy.Constraint("changed")
	preferences := plan.SoftPreferences()
	preferences[0] = policy.SoftPreference("changed")

	assert.NotEqual(t, policy.Constraint("changed"), plan.HardConstraints()[0])
	assert.NotEqual(t, policy.SoftPreference("changed"), plan.SoftPreferences()[0])
}

func TestRegistryValidatesDeploymentTargets(t *testing.T) {
	config := validDeploymentPolicyConfig()
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))

	config.TargetOverrides = config.TargetOverrides[:len(config.TargetOverrides)-1]
	err := policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "requires a typed deployment target")

	config = validDeploymentPolicyConfig()
	config.TargetOverrides[0].Target.Provider = "other-provider"
	err = policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "has no available catalog binding")

	// An untiered catalog row exists but is not a routing target, so a
	// deployment target naming it is refused as not routable.
	config = validDeploymentPolicyConfig()
	config.TargetOverrides[0].Target.CatalogID = "deepseek/deepseek-v4-pro"
	err = policy.DefaultRegistry().ValidateDeployment(config)
	assert.ErrorContains(t, err, "is not a routable catalog model")
}

func TestPlanResolverDoesNotFallBackToRosterForStaleArm(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("z-ai/glm-5.3-flash"),
		providerSet(providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Selection: policy.CandidateSelection{
			ArmID:    "stale-arm",
			RosterID: "z-ai/glm-5.3-flash",
		},
	})
	assertResolutionErrorCode(t, err, policy.ResolutionErrorUnknownSelection)
}

func TestPlanResolverIncludesEveryAlternativeBinding(t *testing.T) {
	specs := policy.DefaultRegistry().Specs()
	index := policyIndex(t, specs, policy.PurposeHandoverSummary)
	specs[index].Fallback = policy.FallbackSpec{
		Kind:         policy.FallbackKindPlanAlternatives,
		Alternatives: []string{"z-ai/glm-5.2"},
	}
	registry, err := policy.NewRegistry(specs)
	require.NoError(t, err)
	candidateResolver := policy.NewResolver(
		modelSet("z-ai/glm-5.3-flash", "z-ai/glm-5.2"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
		func(model catalog.Model) string { return model.ID },
	)
	planResolver, err := policy.NewPlanResolver(registry, candidateResolver)
	require.NoError(t, err)

	plan, err := planResolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeHandoverSummary})
	require.NoError(t, err)
	alternatives := plan.AlternativeBindings()
	// ai& is the only upstream, so the alternative model has exactly one
	// binding: the plan declares it once.
	require.Len(t, alternatives, 1)
	assert.Equal(t, "z-ai/glm-5.2", alternatives[0].CatalogID)
	assert.Equal(t, providers.ProviderAIAND, alternatives[0].Provider)
}

func newPlanResolver(t *testing.T, deployed, available map[string]struct{}) *policy.PlanResolver {
	t.Helper()
	candidateResolver := policy.NewResolver(
		deployed,
		available,
		func(model catalog.Model) string { return model.ID },
	)
	planResolver, err := policy.NewPlanResolver(policy.DefaultRegistry(), candidateResolver)
	require.NoError(t, err)
	return planResolver
}

func validDeploymentPolicyConfig() policy.DeploymentPolicyConfig {
	targetOverrides := make([]policy.PurposeTargetOverride, 0, 5)
	for _, purpose := range []policy.Purpose{
		policy.PurposeTitleGeneration,
		policy.PurposeClassifier,
		policy.PurposeProbe,
		policy.PurposeSubAgentDispatch,
		policy.PurposeClientCompaction,
	} {
		targetOverrides = append(targetOverrides, policy.PurposeTargetOverride{
			Purpose: purpose,
			Target: policy.TargetOverride{
				Source:    policy.OverrideSourceDeployment,
				CatalogID: "z-ai/glm-5.3-flash",
				Provider:  providers.ProviderAIAND,
			},
		})
	}
	return policy.DeploymentPolicyConfig{
		AvailableProviders: providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
		TargetOverrides:    targetOverrides,
	}
}

func modelSet(models ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		set[model] = struct{}{}
	}
	return set
}

func providerSet(providerNames ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(providerNames))
	for _, providerName := range providerNames {
		set[providerName] = struct{}{}
	}
	return set
}

func assertResolutionErrorCode(t *testing.T, err error, expected policy.ResolutionErrorCode) {
	t.Helper()
	var resolutionErr *policy.ResolutionError
	require.True(t, errors.As(err, &resolutionErr), "expected ResolutionError, got %v", err)
	assert.Equal(t, expected, resolutionErr.Code)
}

func TestFixedCatalogTargetSetIncludesTheReviewedModels(t *testing.T) {
	t.Parallel()

	available := providerSet(providers.ProviderAIAND)
	targets := policy.DefaultRegistry().FixedCatalogTargetSet(available)
	assert.Contains(t, targets, "z-ai/glm-5.3")
	assert.Contains(t, targets, "moonshotai/kimi-k3")
	// Every fixed-catalog target is a tiered routing target, so the fixed set
	// is a subset of the generic routing universe.
	for id := range targets {
		assert.Containsf(t, catalog.RoutingTargetSet(available), id,
			"fixed-catalog target %q must be a routing target", id)
	}

	assert.Empty(t, policy.DefaultRegistry().FixedCatalogTargetSet(providerSet()))
}

func TestPlanResolverResolvesUntieredPrecompactionSummarizer(t *testing.T) {
	t.Parallel()

	available := providerSet(providers.ProviderAIAND)
	deployed := policy.DefaultRegistry().FixedCatalogTargetSet(available)
	for id := range catalog.RoutingTargetSet(available) {
		deployed[id] = struct{}{}
	}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		deployed, available, func(m catalog.Model) string { return m.ID }))
	require.NoError(t, err)

	for _, model := range []string{"z-ai/glm-5.3", "moonshotai/kimi-k3"} {
		plan, err := plans.Resolve(policy.ResolutionRequest{
			Purpose:       policy.PurposePrecompactionSummary,
			RouterRequest: router.Request{AllowedModels: modelSet(model)},
			Overrides: []policy.TargetOverride{{
				Source:    policy.OverrideSourceSession,
				CatalogID: model,
				Provider:  providers.ProviderAIAND,
			}},
		})
		require.NoError(t, err, model)
		assert.Equal(t, model, plan.SelectedTarget().CatalogID)
	}
}

func TestRegistryAcceptsUntieredFixedCatalogDeploymentTarget(t *testing.T) {
	t.Parallel()

	config := validDeploymentPolicyConfig()
	config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{
		Purpose: policy.PurposePrecompactionSummary,
		Target: policy.TargetOverride{
			Source:    policy.OverrideSourceDeployment,
			CatalogID: "z-ai/glm-5.3",
			Provider:  providers.ProviderAIAND,
		},
	})
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
}
