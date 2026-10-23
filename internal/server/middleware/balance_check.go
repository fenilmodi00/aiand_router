package middleware

import (
	"context"
	"errors"
	"net/http"

	"aiand-os/router/internal/billing"
	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// TopUpURL is the customer-facing page where org admins buy credits.
// Returned in the 402 body so the client can surface a CTA.
const TopUpURL = "https://app.workaiand.ai/organization/settings/aiand-router"

// WithBalanceCheck enforces prepaid credit gating on inference routes.
// Attached only in managed mode and only after WithAuth, so the
// installation lookup below is guaranteed to be populated.
//
// Behavior (evaluated in order):
//   - Override row present → pass through; flag the request context so
//     the proxy's debit hook writes a delta=0 ledger row.
//   - Balance ≤ minBalanceMicros (or no balance row) → HTTP 402.
//   - Otherwise → pass through.
//
// No turn is ever funded by a caller subscription, so there is no
// subscription exemption: every turn either debits the org's prepaid balance
// or is rejected.
//
// The balance read is a single indexed SELECT (~2-5ms in-region). Any
// repo error fails closed with HTTP 503: in a prepaid credit system,
// allowing requests through when the gate cannot read the balance
// creates an unbilled-usage window where platform spend is incurred
// against an unknown balance. A short retry window for clients is the
// correct tradeoff vs. silently letting tenants spend without billing.
func WithBalanceCheck(svc *billing.Service, minBalanceMicros int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		if owner, ok := billing.InternalTestOwnerFrom(c.Request.Context()); ok {
			balance, err := svc.PrepaidBalance(c.Request.Context(), owner)
			if err != nil {
				log.Error("Internal test budget unavailable", "subject_id", owner.TestSubjectID, "err", err)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "test_budget_unavailable"})
				return
			}
			if balance <= minBalanceMicros {
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": "test_budget_depleted"})
				return
			}
			c.Next()
			return
		}
		if _, ok := proxy.AgentShadowEvalFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}

		// A turn covered by an individual Max/Boost allowance debits 0 on the
		// org balance, so gating it on prepaid credits would 402 usage the
		// subscription already bought.
		if subscriberAllowanceCovers(c) {
			c.Next()
			return
		}

		installation := InstallationFrom(c)
		if installation == nil || installation.ExternalID == "" {
			// Should never happen: WithAuth runs first and would have
			// 401'd. Log Debug rather than Error so a synthetic request
			// missing the auth setup doesn't page on-call.
			log.Debug("Balance check skipped: no installation on request context")
			c.Next()
			return
		}

		orgID := installation.ExternalID

		result, err := svc.CheckBalance(c.Request.Context(), orgID)
		if err != nil {
			if errors.Is(err, billing.ErrBalanceRowMissing) {
				log.Info("Balance check rejected: balance row missing", "organization_id", orgID)
				c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
					"error":              "insufficient_credits",
					"top_up_url":         TopUpURL,
					"balance_usd_micros": 0,
					"message":            "Your organization's prepaid credits are depleted. Contact your org admin to add credits.",
				})
				return
			}
			// Infra error reading billing tables. Fail closed: a prepaid
			// gate that lets requests through on read errors creates an
			// unbilled-usage window. Return 503 so clients retry rather
			// than silently spending against an unknown balance.
			log.Error("Balance check failed; refusing request", "err", err, "organization_id", orgID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		if result.HasOverride {
			ctx := context.WithValue(c.Request.Context(), billing.HasOverrideContextKey, true)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}

		threshold := minBalanceMicros

		if result.BalanceMicros <= threshold {
			log.Info("Balance check rejected: balance at or below threshold",
				"organization_id", orgID,
				"balance_usd_micros", result.BalanceMicros,
				"threshold_usd_micros", threshold,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":              "insufficient_credits",
				"top_up_url":         TopUpURL,
				"balance_usd_micros": result.BalanceMicros,
				"message":            "Your organization's prepaid credits are depleted. Contact your org admin to add credits.",
			})
			return
		}

		c.Next()
	}
}
