package policy

import (
	"errors"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanProjection_BoundsAlternatives(t *testing.T) {
	alternatives := make([]Binding, 0, maxProjectedAlternatives+3)
	for index := 0; index < maxProjectedAlternatives+3; index++ {
		alternatives = append(alternatives, Binding{CatalogID: "z-ai/glm-5.3-flash", Provider: providers.ProviderAIAND, BindingIndex: index})
	}
	plan := ResolvedPlan{
		purpose:             PurposeHandoverSummary,
		policyID:            "aux-handover-summary",
		selectedBinding:     Binding{CatalogID: "z-ai/glm-5.3-flash", Provider: providers.ProviderAIAND},
		alternativeBindings: alternatives,
		provenance:          PlanProvenance{SelectionStrategy: SelectionStrategyFixedCatalog, OverrideSource: OverrideSourcePolicyDefault},
	}

	projection := plan.Projection()

	assert.Len(t, projection.Alternatives, maxProjectedAlternatives)
	assert.Equal(t, len(alternatives), projection.AlternativeCount)
	assert.Equal(t, providers.ProviderAIAND, projection.SelectedTarget.Provider)
	assert.Equal(t, OverrideSourcePolicyDefault, projection.Provenance.OverrideSource)
}

func TestProjectResolutionError_OnlyProjectsTypedFailures(t *testing.T) {
	_, ok := ProjectResolutionError(errors.New("pool exhausted: dsn=postgres://user:secret@host"))
	assert.False(t, ok)

	projected, ok := ProjectResolutionError(resolutionError(ResolutionErrorUnknownPurpose, Purpose("nope"), "", "purpose is not registered"))
	require.True(t, ok)
	assert.Equal(t, ResolutionErrorUnknownPurpose, projected.Code)
	assert.Equal(t, Purpose("nope"), projected.Purpose)
}

func TestDeploymentProjection_OmitsCandidateDumpForRouterPolicies(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAIAND: {}}
	projection := DefaultRegistry().DeploymentProjection(DeploymentPolicyConfig{AvailableProviders: available})

	assert.Equal(t, []string{providers.ProviderAIAND}, projection.AvailableProviders)
	for _, entry := range projection.Policies {
		if entry.SelectionStrategy == SelectionStrategyRouter {
			assert.Empty(t, entry.CandidateBindings, entry.PolicyID)
			assert.Equal(t, projection.RoutableModels, entry.RoutableModels, entry.PolicyID)
		}
	}
}

func TestInspect_RouterPurposeUsesDeploymentServingUniverse(t *testing.T) {
	available := map[string]struct{}{providers.ProviderAIAND: {}}
	resolver, err := NewPlanResolver(DefaultRegistry(), NewResolver(
		catalog.RoutingTargetSet(available), available, func(model catalog.Model) string { return model.ID }))
	require.NoError(t, err)
	// The deployment serving universe for router-selected purposes is the
	// routable catalog set: tiered rows only. A pin-only (untiered) row has
	// bindings and is dispatchable by explicit force, but it is never an
	// automatic routing target, so inspection must refuse it.
	request := InspectionRequest{Purpose: PurposeOpenAIResponses, Model: "z-ai/glm-5.2"}

	_, err = resolver.Inspect(request, DeploymentPolicyConfig{AvailableProviders: available})
	var resolution *ResolutionError
	require.ErrorAs(t, err, &resolution)
	assert.Equal(t, ResolutionErrorNoEligibleBinding, resolution.Code)

	// A tiered row is inside the serving universe.
	tiered, err := resolver.Inspect(InspectionRequest{Purpose: PurposeOpenAIResponses, Model: "deepseek/deepseek-v4-pro"},
		DeploymentPolicyConfig{AvailableProviders: available})
	require.NoError(t, err)
	assert.Equal(t, "deepseek/deepseek-v4-pro", tiered.SelectedBinding().CatalogID)

	served := catalog.HMMRoutingTargetSet(available)
	plan, err := resolver.Inspect(InspectionRequest{Purpose: PurposeOpenAIResponses, Model: "z-ai/glm-5.3-flash"},
		DeploymentPolicyConfig{AvailableProviders: available, RoutableModels: served})
	require.NoError(t, err)
	assert.Equal(t, "z-ai/glm-5.3-flash", plan.SelectedBinding().CatalogID)

	projection := DefaultRegistry().DeploymentProjection(DeploymentPolicyConfig{AvailableProviders: available, RoutableModels: served})
	assert.Equal(t, len(served), projection.RoutableModels)
}
