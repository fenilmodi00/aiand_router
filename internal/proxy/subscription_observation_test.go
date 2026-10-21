package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/subscriptions/entitlement"
)

// TestPresentSubscriptionTokens_MaxProductScopeReportsNone: the Max product
// scope never treats a consumer subscription as a funding source, so no token is
// reported and the route is not covered.
func TestPresentSubscriptionTokens_MaxProductScopeReportsNone(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-live-token")
	ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanMax)

	codex, anthro := presentSubscriptionTokens(ctx, h)
	assert.Empty(t, anthro)
	assert.Empty(t, codex)
	assert.False(t, RequestPresentsCoveringSubscription(ctx, h, routePathMessages))
}
