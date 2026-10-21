package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions/entitlement"
)

func maxScopedContext() context.Context {
	return entitlement.WithProductScope(context.Background(), entitlement.PlanMax)
}

func TestUnsubscribedRequestKeepsUnrestrictedRouting(t *testing.T) {
	svc := &Service{}

	req := svc.withPolicyRequestContext(context.Background(), router.Request{})

	assert.False(t, req.ProductEligibility.Restricts())
	assert.Empty(t, req.ExcludedModels)
}

func TestMaxRouteRefusesAnUnknownSourceDecision(t *testing.T) {
	unknownRouter := &registryRouter{decision: router.Decision{Model: "muse-spark-1.3", Provider: providers.ProviderAIAND}}
	svc := &Service{router: unknownRouter}

	_, err := svc.Route(maxScopedContext(), router.Request{})

	require.ErrorIs(t, err, eligibility.ErrModelIneligible)
}

func TestMaxRouteServesOpenSourceDecision(t *testing.T) {
	openRouter := &registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderAIAND}}
	svc := &Service{router: openRouter}

	decision, err := svc.Route(maxScopedContext(), router.Request{})

	require.NoError(t, err)
	assert.Equal(t, "deepseek/deepseek-v4-pro", decision.Model)
}

func TestDispatchServesAnEligibleDecision(t *testing.T) {
	fireworks := &fakeClient{name: providers.ProviderAIAND, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
	svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: fireworks})

	rec := httptest.NewRecorder()
	in := plannedInputs(rec, newPreludeBuffer(rec), []catalog.ProviderBinding{{Provider: providers.ProviderAIAND}}, nil)

	_, err := svc.dispatchWithFallback(maxScopedContext(), in)

	require.NoError(t, err)
	assert.Equal(t, 1, fireworks.calls)
}

// The subscription pass-through lane serves the requested model verbatim
// without routing, so an ineligible model disengages it and the turn falls
// through to routed dispatch instead.
func TestUsageBypassDisengagesForAnIneligibleModel(t *testing.T) {
	const token = "sk-ant-oat01-test-subscription-token"
	threshold := 0.80
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300}})
	svc := &Service{usageObserver: obs}
	ctx := context.WithValue(maxScopedContext(), AnthropicSubscriptionContextKey{}, token)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})

	_, engaged := svc.usageBypassDecision(ctx, http.Header{}, router.Request{
		RequestedModel:     "deepseek/deepseek-v4.1-flash",
		ProductEligibility: eligibility.MaxOpenSourceOnly,
	}, nil, turntype.MainLoop)

	assert.False(t, engaged)
}

// A refusal is the caller's problem to fix (pick a covered model), not an
// upstream failure: it must not fall through to the generic 502.
func TestIneligibleModelIsAClassifiedClientError(t *testing.T) {
	cls, matched := ClassifyDispatchError(fmt.Errorf("strategy %q: %w", "cluster", eligibility.ErrModelIneligible))

	require.True(t, matched)
	assert.Equal(t, DispatchErrorProductIneligible, cls.Kind)
	assert.Equal(t, http.StatusForbidden, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
}
