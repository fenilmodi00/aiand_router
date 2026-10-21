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
	resolver := newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet(providers.ProviderAIAND, providers.ProviderAIAND))
	plan, err := resolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeEscalationJudge})
	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAIAND, plan.SelectedTarget().Provider)
	assert.Equal(t, "zai-org/glm-5.3-flash", plan.SelectedTarget().UpstreamID)
	assert.Empty(t, plan.AlternativeTargets())
	assert.Equal(t, 1, plan.Budget().MaxAttempts)
	assert.EqualValues(t, 20_000, plan.Budget().TimeoutMillis)
	// The judge is a fixed-catalog target: with no provider wired the request
	// cannot be served.
	resolver = newPlanResolver(t, modelSet(policy.EscalationJudgeModel), providerSet())
	_, err = resolver.Resolve(policy.ResolutionRequest{Purpose: policy.PurposeEscalationJudge, RouterRequest: router.Request{EnabledProviders: providerSet()}})
	require.Error(t, err)
}

func TestEscalationJudgeDeploymentIsOptional(t *testing.T) {
	// The judge is optional: a deployment without ai& wired may still serve
	// the main policies, but enabling the judge needs its fixed provider.
	config := policy.DeploymentPolicyConfig{AvailableProviders: providerSet(providers.ProviderAIAND), TargetOverrides: []policy.PurposeTargetOverride{}}
	for _, purpose := range []policy.Purpose{policy.PurposeTitleGeneration, policy.PurposeClassifier, policy.PurposeProbe, policy.PurposeSubAgentDispatch, policy.PurposeClientCompaction} {
		config.TargetOverrides = append(config.TargetOverrides, policy.PurposeTargetOverride{Purpose: purpose, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: policy.UtilityHardPinDefaultModel, Provider: providers.ProviderAIAND}})
	}
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config))
	config.EnabledOptionalPurposes = map[policy.Purpose]bool{policy.PurposeEscalationJudge: true}
	require.NoError(t, policy.DefaultRegistry().ValidateDeployment(config), "the judge resolves through the same wired provider")
}
