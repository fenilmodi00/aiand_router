package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/translate"
)

// A name that resolves to no model writes no pin and keeps the ack wording the
// statusline scripts match on.
func TestForceModelCommand_TypedUnknownKeepsRouting(t *testing.T) {
	for _, typed := range []string{"/fm opsu", "/fm opus-5-", "/fm gpt-5.6-astra", "/fm opus fix the tests"} {
		t.Run(typed, func(t *testing.T) {
			store := &recordingPinStore{}
			svc := NewService(nil, nil, nil, false, nil, store, false,
				providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)
			env := typedCommandEnv(t, typed)
			cmd, found := env.ExtractForceModelCommand()
			require.True(t, found, "command parsed")

			rec := httptest.NewRecorder()
			require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env, cmd,
				uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

			assert.Empty(t, store.upserts, "no pin")
			assert.Contains(t, rec.Body.String(), "isn't a recognized model")
		})
	}
}

func TestForceModelCommand_TypoSuggestsWithoutPinning(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)
	env := typedCommandEnv(t, "/fm opsu")
	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found, "command parsed")

	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env, cmd,
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "a suggestion never pins")
	assert.Contains(t, rec.Body.String(), "isn't a recognized model")
	assert.Contains(t, rec.Body.String(), `Did you mean \"opus\"?`)
}

func typedCommandEnv(t *testing.T, text string) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "z-ai/glm-5.2",
		"messages": []map[string]any{{"role": "user", "content": text}},
	})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}
