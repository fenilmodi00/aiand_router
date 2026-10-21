package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// gateServing registers the gate as middleware and serves the route with the
// given handler, so the handler runs inside the gate the way a dispatched turn
// does rather than after it.
func gateServing(
	t *testing.T,
	entitlements *stubEntitlements,
	allowances *stubAllowances,
	serve gin.HandlerFunc,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := entitlement.NewService(entitlements, allowances).WithClock(func() time.Time { return allowanceNow })

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("router_api_key", subscriberAPIKey()) })
	engine.Use(middleware.WithSubscriberAllowance(svc))
	engine.POST("/v1/messages", serve)
	return engine
}

func TestWithSubscriberAllowance_MaxCoveringSubscriptionIsNotSubscriptionOnly(t *testing.T) {
	entitlements := &stubEntitlements{current: maxSubscriberEntitlement(), found: true}
	allowances := &stubAllowances{}
	var subscriptionOnly bool
	var coverage entitlement.Coverage
	engine := gateServing(t, entitlements, allowances, func(c *gin.Context) {
		subscriptionOnly = billing.SubscriptionOnlyFromContext(c.Request.Context())
		coverage, _ = entitlement.CoverageFromContext(c.Request.Context())
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat-abc123")
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, subscriptionOnly)
	assert.Equal(t, entitlement.SubscriberID(allowanceSubscriberID), coverage.SubscriberID)
}
