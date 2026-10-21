package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailedAPIFallbackReplacesInboundSubscriptionAttribution(t *testing.T) {
	ctx := WithManagedSubscriptionUsage(context.Background())
	usage := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	usage.SubscriptionAttempted = true
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("oauth-sentinel"),
		OAuth:  true,
		Source: credSourceSubscription,
	})
	apiCtx := context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("api-sentinel"),
		Source: credSourceBYOK,
	})

	recordWinningCredentials(ctx, apiCtx)

	require.Same(t, CredentialsFromContext(apiCtx), CredentialsFromContext(ctx))
	winner := CredentialsFromContext(ctx)
	require.Equal(t, credSourceBYOK, winner.Source)
	require.Equal(t, []byte("api-sentinel"), winner.APIKey)
	require.False(t, winner.OAuth)
	assert.False(t, (&Service{}).costNeutralSubscriptionServed(ctx))
	telemetry := InsertTelemetryParams{RequestedModel: "deepseek/deepseek-v4-pro", DecisionModel: "deepseek/deepseek-v4-pro"}
	applySubscriberTelemetry(ctx, &telemetry)
	assert.Empty(t, telemetry.SubscriptionAccountID)
	assert.Empty(t, telemetry.SubscriptionOwnerID)
	assert.Empty(t, telemetry.SubscriptionTier)
}

func TestSubscriptionWinnerTelemetryDoesNotAttributeUnservedOrPaidCapacity(t *testing.T) {
	for _, served := range []bool{false, true} {
		ctx := WithManagedSubscriptionUsage(context.Background())
		winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
		winner.Served = served
		winner.OverageInUse = served
		winner.SubscriptionAccountID = "00000000-0000-0000-0000-000000000101"
		winner.SubscriptionOwnerID = "00000000-0000-0000-0000-000000000102"
		winner.SubscriptionTier = auth.SubscriptionTierShared
		telemetry := InsertTelemetryParams{RequestedModel: "unknown", DecisionModel: "unknown"}
		applySubscriberTelemetry(ctx, &telemetry)
		assert.Empty(t, telemetry.SubscriptionAccountID)
		assert.Empty(t, telemetry.SubscriptionOwnerID)
		assert.Empty(t, telemetry.SubscriptionTier)
		assert.Empty(t, telemetry.IntendedModelFamily)
		assert.Empty(t, telemetry.FinalModelFamily)
	}
}
