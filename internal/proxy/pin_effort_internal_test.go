package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestForceModelHeader_PersistsEffortOnPin(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	env := forceCommandEnv(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set(ForceModelHeader, "opus:high")

	_, _, forceErr := svc.applyForceModelHeader(
		context.Background(), req, uuid.New(), DeriveSessionKey(env, "key-1"))
	require.NoError(t, forceErr)

	require.Len(t, store.upserts, 1)
	assert.Equal(t, "high", store.upserts[0].Effort)
}

// req.ForceModel (the header path) carries its own suffix into the in-memory
// forced pin for the current turn.
func TestRunTurnLoop_RequestForceModelCarriesEffort(t *testing.T) {
	store := &forcedPinStore{}
	fr := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	env, err := translate.ParseAnthropic([]byte(`{
		"model":"moonshotai/kimi-k3",
		"system":"Your task is to create a detailed summary of the conversation so far.",
		"messages":[{"role":"user","content":"summarize"}]
	}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		ForceModel:     "kimi:medium",
	})
	require.NoError(t, err)

	assert.Equal(t, "moonshotai/kimi-k3", res.Decision.Model)
	assert.Equal(t, "medium", res.Decision.Effort)
}

// A TTL refresh rewrites the row, and the upsert always takes the incoming
// effort — so the refresh must carry the stored level forward.
func TestRefreshPin_CarriesEffortForward(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	existing := sessionpin.Pin{
		Provider: providers.ProviderAIAND,
		Model:    "moonshotai/kimi-k3",
		Effort:   "xhigh",
		Reason:   translate.ReasonUserForceModel,
	}
	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{},
		existing, "default", router.Decision{
			Provider: existing.Provider, Model: existing.Model, Reason: existing.Reason,
		})
	require.Len(t, store.upserts, 1)
	assert.Equal(t, "xhigh", store.upserts[0].Effort)

	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{},
		existing, "default", router.Decision{
			Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash",
		})
	require.Len(t, store.upserts, 2)
	assert.Empty(t, store.upserts[1].Effort,
		"a refresh onto a different model must not inherit the old level")
}

func TestOrderBandPair_AnchoredHalfKeepsEffort(t *testing.T) {
	large, small := orderBandPair(sessionpin.Pin{
		Provider:       providers.ProviderAIAND,
		Model:          "moonshotai/kimi-k3",
		Effort:         "high",
		PairedProvider: providers.ProviderAIAND,
		PairedModel:    "z-ai/glm-5.3-flash",
	})
	assert.Equal(t, "moonshotai/kimi-k3", large.Model)
	assert.Equal(t, "high", large.Effort)
	assert.Empty(t, small.Effort)
}
