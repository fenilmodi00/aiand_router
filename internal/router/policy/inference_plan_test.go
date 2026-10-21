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

func TestPlanResolverRejectsInvalidExplicitOverrideWithoutSubstitution(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5", "gpt-5.6-luna"),
		providerSet(providers.ProviderAIAND, providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose: policy.PurposeAnthropicMessages,
		Overrides: []policy.TargetOverride{
			{Source: policy.OverrideSourceRequest, CatalogID: "not-a-catalog-model"},
			{Source: policy.OverrideSourceInstallation, CatalogID: "gpt-5.6-luna"},
		},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func TestPlanResolverRequiresTypedForceModelOverride(t *testing.T) {
	planResolver := newPlanResolver(t,
		modelSet("claude-haiku-4-5"),
		providerSet(providers.ProviderAIAND),
	)
	_, err := planResolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeAnthropicMessages,
		RouterRequest: router.Request{ForceModel: "claude-haiku-4-5"},
		Selection:     policy.CandidateSelection{RosterID: "claude-haiku-4-5"},
	})

	assertResolutionErrorCode(t, err, policy.ResolutionErrorInvalidOverride)
}

func newPlanResolver(t *testing.T, deployed, available map[string]struct{}) *policy.PlanResolver {
	t.Helper()
	candidateResolver := policy.NewResolver(
		deployed,
		available,
		func(model catalog.Model) string { return model.ID },
		policy.ProviderPolicy{},
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
				CatalogID: "claude-haiku-4-5",
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
