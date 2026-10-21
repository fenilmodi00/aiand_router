package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions/entitlement"
)

// Paid extra usage means included quota is spent: OAuth is suppressed and the deployment key serves.
func TestClaudeOverageAuthorizesDeploymentKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := &parityUpstream{okBody: ingress.upstreamOK(false)}
	service := ingress.parityService(upstream)
	observer := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	service.WithUsageObserver(observer)
	observer.Record(observer.Key([]byte(ingress.token)), usage.Snapshot{OverageInUse: true, UnifiedResetAt: time.Now().Add(time.Hour)})

	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ingress.subCtx(), body, recorder, request))
	assert.Zero(t, upstream.subDispatches, "billable Claude OAuth must be suppressed before dispatch")
	assert.Equal(t, 1, upstream.paidDispatches, "observed overage authorizes API inference")
}

func TestPresentSubscriptionTokens_MaxProductScopeReportsNone(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-live-token")
	ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanMax)

	codex, anthro := presentSubscriptionTokens(ctx, h)
	assert.Empty(t, anthro)
	assert.Empty(t, codex)
	assert.False(t, RequestPresentsCoveringSubscription(ctx, h, routePathMessages))

	enrolled := context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	assert.False(t, RequestPresentsCoveringSubscription(enrolled, http.Header{}, routePathMessages))
}
