package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

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

// textOnlyForcedModel names a live catalog row with ImageInputUnsupported
// (qwen/qwen3-coder-next was removed from the aiand catalog with the single-
// upstream chop; deepseek-v4-pro carries the text-only flag today).
const textOnlyForcedModel = "deepseek/deepseek-v4-pro"

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

func TestRunTurnLoop_ForcedImageCapModel_ReachesProvider(t *testing.T) {
	// v0 adaptation: no catalog binding declares an image cap, so the
	// image-count-only exclusion here is synthetic; an image-capable row
	// (not the text-only const) must be the forced model for the force to be
	// servable at all.
	const model = "z-ai/glm-5.3-flash"
	const imageCount = 17
	available := map[string]struct{}{model: {}}
	fr := &tierProbeRouter{available: available}
	svc := NewService(fr, nil, nil, false, nil, &forcedPinStore{}, false,
		providers.ProviderAIAND, model, nil).
		WithAvailableModels(available).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithPlannerEnabled(false)

	const imageBlock = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}`
	imageBlocks := strings.TrimSuffix(strings.Repeat(imageBlock+",", imageCount), ",")
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":[` + imageBlocks + `]}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	require.Equal(t, imageCount, feats.ImageCount)

	excludedModels := map[string]struct{}{model: {}}
	req := router.Request{
		RequestedModel: feats.Model,
		ForceModel:     model,
		HasImages:      true,
		ImageCount:     imageCount,
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		Exclusions: router.Exclusions{}.
			ExcludeSet(excludedModels, router.ExclusionRequested).
			ExcludeSet(excludedModels, router.ExclusionImageLimit).
			ExcludeSet(excludedModels, router.ExclusionSafety),
	}
	pin := sessionpin.Pin{Provider: providers.ProviderAIAND, Model: model}
	assert.True(t, forcedPinEligible(pin, req), "the explicit force must survive an image-count-only exclusion")

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-1", uuid.New(), "", nil, req)
	require.NoError(t, err)
	assert.Equal(t, model, res.Decision.Model)
	assert.Equal(t, providers.ProviderAIAND, res.Decision.Provider)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.True(t, res.StickyHit)
	assert.Empty(t, fr.captured, "an honored force must not invoke the scorer")

	bindings := svc.resolveBindingsForDispatch(withRequestImageCount(context.Background(), imageCount), res.Decision)
	require.Len(t, bindings, 1)
	assert.Equal(t, providers.ProviderAIAND, bindings[0].Provider)
}

func TestReadmitForcedImageLimitCopiesExcludedModels(t *testing.T) {
	const model = textOnlyForcedModel
	req := router.Request{
		ImageCount: 17,
		Exclusions: router.Exclusions{}.
			ExcludeSet(map[string]struct{}{model: {}, "other-model": {}}, router.ExclusionRequested).
			ExcludeModel(model, router.ExclusionImageLimit),
	}

	readmitted := readmitForcedImageLimit(req, model)

	assert.Zero(t, readmitted.ImageCount)
	assert.False(t, readmitted.Exclusions.Excludes(model))
	assert.True(t, readmitted.Exclusions.Excludes("other-model"))
	assert.True(t, req.Exclusions.Excludes(model), "readmission must not mutate the caller's exclusion set")
	assert.Equal(t, 17, req.ImageCount, "readmission must not mutate the caller's request")
}

// forcedPinEligible is the gate for the hard-pinned-turn fast path, which is a
// separate branch from the one above and needs the same guard.
func TestForcedPinEligible_RejectsTextOnlyModelOnImageTurn(t *testing.T) {
	pin := sessionpin.Pin{Provider: providers.ProviderAIAND, Model: textOnlyForcedModel}

	assert.False(t, forcedPinEligible(pin, router.Request{HasImages: true}),
		"text-only forced pin must be ineligible for an image-bearing turn")
	assert.True(t, forcedPinEligible(pin, router.Request{HasImages: false}),
		"same pin stays eligible without images")
	assert.True(t, forcedPinEligible(
		sessionpin.Pin{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash"},
		router.Request{HasImages: true}),
		"an image-capable forced pin stays eligible on an image turn")
}
