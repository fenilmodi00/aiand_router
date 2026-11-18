package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
)

const multiBindingModel = string(catalog.ModelIDDeepSeekV4Pro)

func providersOf(bindings []catalog.ProviderBinding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Provider)
	}
	return out
}

func keyed(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func TestOrderedBindingsWithoutFailoverServesOnlyTheDecisionProvider(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision: router.Decision{Model: multiBindingModel, Provider: providers.ProviderAIAND},
	})
	assert.Equal(t, []string{providers.ProviderAIAND}, providersOf(got))
}

func TestOrderedBindingsKeepsTheDecisionProviderFirst(t *testing.T) {
	got := dispatch.OrderedBindings(dispatch.BindingRequest{
		Decision:                 router.Decision{Model: multiBindingModel, Provider: providers.ProviderAIAND},
		DeploymentKeyedProviders: keyed(providers.ProviderAIAND),
	})
	assert.Equal(t, []string{providers.ProviderAIAND}, providersOf(got))
}

func TestOrderedBindingsUnboundForcedCatalogModelHasNoWalk(t *testing.T) {
	req := dispatch.BindingRequest{
		Decision:                 router.Decision{Model: multiBindingModel, Provider: providers.ProviderAIAND},
		DeploymentKeyedProviders: keyed(),
	}
	assert.Equal(t, []string{providers.ProviderAIAND}, providersOf(dispatch.OrderedBindings(req)))

	req.ForceModel = true
	assert.Nil(t, dispatch.OrderedBindings(req))
}
