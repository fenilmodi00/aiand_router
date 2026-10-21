package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An agent returning a screenshot nests the image inside a tool_result rather
// than putting it at the top level of the message content.
const toolResultImageBody = `{"model":"deepseek/deepseek-v4.1-flash","messages":[
	{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/tmp/shot.png"}}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`

const textOnlyForcedModel = "qwen/qwen3-coder-next"

// Control: the same forced pin must still be honored when the turn carries no
// image, so the guard can't be mistaken for "forced pins stopped working".
func TestRunTurnLoop_ForcedTextOnlyModel_HonoredWithoutImages(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{
		textOnlyForcedModel:            {},
		"deepseek/deepseek-v4.1-flash": {},
	}}
	store := &forcedPinStore{pin: sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       textOnlyForcedModel,
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}}
	svc := NewService(fr, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithAvailableModels(fr.available).
		WithPlannerEnabled(false)

	env, err := translate.ParseAnthropic([]byte(`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"plain text turn"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	require.False(t, feats.HasImages)

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, router.Request{
		RequestedModel: feats.Model,
		HasImages:      feats.HasImages,
	})
	require.NoError(t, err)

	assert.Equal(t, textOnlyForcedModel, res.Decision.Model, "a text-only forced pin still serves text-only turns")
	assert.True(t, res.StickyHit)
	assert.Empty(t, fr.captured, "an honored forced pin must not invoke the scorer")
}
