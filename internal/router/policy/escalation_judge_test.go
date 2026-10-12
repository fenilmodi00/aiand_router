package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestEscalationJudgePolicyOnlyResolvesAIAND(t *testing.T) {
	// The judge slug is served on AIand through an installation-declared
	// binding; the fixed policy must resolve it there and nowhere else.
	bindings := map[string][]string{policy.EscalationJudgeModel: {providers.ProviderAIAND}}
	resolver := newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet(providers.ProviderAIAND, providers.ProviderAIAND))
	plan, err := resolver.Resolve(policy.ResolutionRequest{
		Purpose:       policy.PurposeEscalationJudge,
		RouterRequest: router.Request{CustomBindings: bindings},
	})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAIAND, plan.SelectedTarget().Provider)
	assert.Equal(t, policy.EscalationJudgeModel, plan.SelectedTarget().UpstreamID)
	assert.Empty(t, plan.AlternativeTargets())
	assert.Equal(t, 1, plan.Budget().MaxAttempts)
	assert.EqualValues(t, 20_000, plan.Budget().TimeoutMillis)
	resolver = newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet(providers.ProviderAnthropic, providers.ProviderAIAND))
	_, err = resolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeEscalationJudge, RouterRequest: router.Request{EnabledProviders: providerSet(providers.ProviderAnthropic)}})
	require.Error(t, err)
}

func TestEscalationJudgeDeploymentIsOptional(t *testing.T) {
	// The judge target is optional: a deployment that serves every required
	// utility hard pin but cannot serve the judge still validates until the
	// purpose is enabled, and enabling it needs the reviewed slug's AIand row.
	config := policy.DeploymentPolicyConfig{AvailableProviders: providerSet(providers.ProviderAIAND, providers.ProviderAIAND), TargetOverrides: []policy.PurposeTargetOverride{}}
	for _, purpose := range []policy.Purpose{policy.PurposeTitleGeneration, policy.PurposeClassifier, policy.PurposeProbe, policy.PurposeSubAgentDispatch, policy.PurposeClientCompaction} {
		config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{Purpose: purpose, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: "zai-org/glm-5.3", Provider: providers.ProviderAIAND}})
	}
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
	// The judge policy owns its target; deployment overrides are rejected.
	config.EnabledOptionalPurposes = map[policy.Purpose]bool{policy.PurposeEscalationJudge: true}
	config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{Purpose: policy.PurposeEscalationJudge, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: "zai-org/glm-5.3", Provider: providers.ProviderAIAND}})
	require.ErrorContains(t, policy.DefaultRegistry().ValidateDeployment(config), "override is not allowed")
	// Without an override, the AIand-capable deployment resolves the judge's
	// reviewed slug through its roster binding.
	config.TargetOverrides = config.TargetOverrides[:len(config.TargetOverrides)-1]
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
}
