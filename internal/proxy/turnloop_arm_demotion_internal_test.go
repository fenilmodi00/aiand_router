package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const (
	demotedPinModel = "z-ai/glm-5.2"
	freshTurnModel  = "deepseek/deepseek-v4-pro"
)

func demotionTurnLoopEnv(t *testing.T) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	return env, env.RoutingFeatures(false)
}

func runDemotionTurnLoop(t *testing.T, svc *Service, ctx context.Context) turnLoopResult {
	t.Helper()
	env, features := demotionTurnLoopEnv(t)
	res, err := svc.runTurnLoop(
		ctx,
		env,
		features,
		"api-key",
		uuid.New(),
		"",
		http.Header{},
		router.Request{
			RequestedModel:       features.Model,
			ExcludedModels:       svc.excludedModelsForRequest(ctx),
			EstimatedInputTokens: features.Tokens,
			HasTools:             features.HasTools,
			ConversationMessages: conversationMessagesForRouting(env),
		},
	)
	require.NoError(t, err)
	return res
}

func demotedPin(model string, demoted ...string) sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		Model:           model,
		Reason:          "cluster:v0.2",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastTurnEndedAt: time.Now().Add(-time.Minute),
		LastServedModel: model,
		DemotedModels:   demoted,
	}
}
