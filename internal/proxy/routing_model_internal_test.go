package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRoutingModelDoesNotReadLegacyBetaPreference(t *testing.T) {
	svc := (&Service{}).WithSessionStrategyStore(&betaTestPreferenceStore{getErr: errors.New("legacy preference must not be read")})
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	key := [sessionpin.SessionKeyLen]byte{1}
	installation := uuid.New()
	_, err := svc.applySessionStrategy(ctx, installation, key)
	require.Error(t, err)
	ctx = router.WithRoutingModel(ctx, router.ModelAutomatic)
	selected, err := svc.applySessionStrategy(ctx, installation, key)
	require.NoError(t, err)
	require.Equal(t, router.StrategyHMMEmbedding, router.StrategyFromContext(selected))
}

func TestRoutingModelRedirectsLegacyBetaCommandsWithoutChangingPreferences(t *testing.T) {
	for _, model := range []router.RoutingModel{router.ModelAutomatic, router.ModelHMM, router.ModelQwen} {
		for _, commandText := range []string{"/beta", "/beta-llm"} {
			t.Run(string(model)+commandText, func(t *testing.T) {
				store := &betaTestPreferenceStore{}
				svc := (&Service{}).WithSessionStrategyStore(store)
				ctx := router.WithRoutingModel(context.Background(), model)
				envelope := betaTestEnvelope(t, commandText, true)
				command, found := envelope.ExtractBetaCommand()
				require.True(t, found)
				response := httptest.NewRecorder()
				key := [sessionpin.SessionKeyLen]byte{1}
				require.NoError(t, svc.handleBetaCommand(ctx, response, envelope, command, uuid.New(), key, key, 1))
				require.Contains(t, response.Body.String(), "wv router set-model")
				require.Zero(t, store.toggles)
				require.False(t, store.found)
			})
		}
	}
}
