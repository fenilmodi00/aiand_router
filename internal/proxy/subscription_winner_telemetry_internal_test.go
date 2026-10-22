package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailedAPIFallbackReplacesInboundSubscriptionAttribution(t *testing.T) {
	ctx := WithManagedSubscriptionUsage(context.Background())
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("oauth-sentinel"),
		Source: credSourceClient,
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
	telemetry := InsertTelemetryParams{RequestedModel: "deepseek/deepseek-v4-pro", DecisionModel: "deepseek/deepseek-v4-pro"}
	applySubscriberTelemetry(ctx, &telemetry)
	assert.Equal(t, "deepseek/deepseek-v-pro", telemetry.IntendedModelFamily)
	assert.Equal(t, "deepseek/deepseek-v-pro", telemetry.FinalModelFamily)
}
