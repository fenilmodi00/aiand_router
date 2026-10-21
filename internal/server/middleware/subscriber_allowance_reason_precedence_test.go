package middleware_test

import (
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

// billingGatedAllowanceChain wires the allowance gate ahead of the balance gate
// exactly as server.go mounts them on the inference routes. The order is load
// bearing and invisible from either middleware on its own: the allowance gate
// marks a covering caller linked-first before any balance is read, so only the
// balance gate running afterwards can upgrade that to credits_depleted.
func billingGatedAllowanceChain(
	t *testing.T,
	repo *stubBillingRepo,
	allowances *stubAllowances,
	serve gin.HandlerFunc,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	allowanceSvc := entitlement.NewService(
		&stubEntitlements{current: activeSubscriberEntitlement(), found: true},
		allowances,
	).WithClock(func() time.Time { return allowanceNow })

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("router_api_key", subscriberAPIKey())
		withInstallation(c, "org_precedence")
	})
	engine.Use(middleware.WithSubscriberAllowance(allowanceSvc))
	engine.Use(middleware.WithBalanceCheck(billing.NewService(repo), billing.MinBalanceMicros))
	engine.POST("/v1/messages", serve)
	return engine
}
