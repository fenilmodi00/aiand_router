package middleware

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/requestcontext"
	"aiand-os/router/internal/subscriptions/entitlement"
)

func TestVerifiedInternalTestSkipsAllSubscriberAllowanceAndProductReads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(ctxKeyAPIKey, &auth.APIKey{ID: "test-key", CredentialSubjectID: "real-subscriber"})
		c.Set(ctxKeySubscriptionOwner, auth.SubscriptionOwner{SubscriberID: "real-subscriber"})
		ctx := requestcontext.WithInternalTestIdentity(context.Background(), requestcontext.InternalTestIdentity{SubjectID: "test-subject", SessionID: "fresh-session"})
		c.Request = c.Request.WithContext(entitlement.WithProductScope(ctx, entitlement.PlanMax))
	})
	// A nil service panics if either subscriber path is entered.
	engine.Use(WithSubscriberAllowance(nil), WithSubscriberProductScope(nil))
	engine.POST("/v1/messages", func(c *gin.Context) {
		require.Empty(t, SubscriptionOwnerFrom(c).SubscriberID)
		plan, ok := entitlement.ProductScopeFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, entitlement.PlanMax, plan)
		_, covered := entitlement.CoverageFromContext(c.Request.Context())
		require.False(t, covered)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("X-Aiand-User-Email", "customer@example.com")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}
