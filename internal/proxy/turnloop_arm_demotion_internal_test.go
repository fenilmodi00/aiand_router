package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/translate"
)

const (
	demotedPinModel = "z-ai/glm-5.3"
	freshTurnModel  = "deepseek/deepseek-v4-pro"
)

func demotionTurnLoopEnv(t *testing.T) (*translate.RequestEnvelope, translate.RoutingFeatures) {
	t.Helper()
	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	return env, env.RoutingFeatures(false)
}
