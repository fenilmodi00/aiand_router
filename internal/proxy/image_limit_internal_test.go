package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/translate"
)

// v0 adaptation of upstream's TestExcludeImageLimitModels: the aiand catalog
// has one provider, so the per-binding cap scenario is built by giving the
// served model's binding a MaxImagesPerRequest in code via a custom catalog
// row lookup — instead, the tests exercise the filter's own logic with a
// v0-catalog text-only arm and an image-capable arm.

const imageCappedTestModel = "z-ai/glm-5.3"

func TestExcludeImageLimitModels(t *testing.T) {
	model := imageCappedTestModel
	textOnly := "qwen/qwen3-coder-next"
	available := map[string]struct{}{model: {}, textOnly: {}}

	t.Run("zero image count never filters", func(t *testing.T) {
		excluded, removed := excludeImageLimitModels(0, map[string]struct{}{providers.ProviderAIAND: {}}, nil, nil, available)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("text-only model is not an image-limit exclusion", func(t *testing.T) {
		// The text-only arm is the scorer's ImageInput filter's job, not the
		// per-binding cap filter's: unknown-cap bindings stay eligible here.
		excluded, removed := excludeImageLimitModels(17, map[string]struct{}{providers.ProviderAIAND: {}}, nil, nil, available)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("custom binding has no known cap", func(t *testing.T) {
		excluded, removed := excludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderAIAND: {}},
			map[string][]string{model: {"custom"}},
			nil,
			available,
		)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("model with no enabled binding has no known cap", func(t *testing.T) {
		// catalog.AcceptsImageCount treats "no enabled binding" as "no known
		// limit": an enabled-provider set matching none of a model's bindings
		// must not silently empty the pool on an image-heavy request.
		const unboundProvider = "test-unbound-provider"
		excluded, removed := excludeImageLimitModels(
			17,
			map[string]struct{}{unboundProvider: {}},
			nil,
			nil,
			map[string]struct{}{catalog.ModelIDKimiK3.String(): {}},
		)
		assert.Nil(t, excluded)
		assert.Empty(t, removed)
	})
	t.Run("existing exclusions are preserved copy-on-write", func(t *testing.T) {
		existing := map[string]struct{}{"already-excluded": {}}
		excluded, removed := excludeImageLimitModels(
			17,
			map[string]struct{}{providers.ProviderAIAND: {}},
			nil,
			existing,
			map[string]struct{}{model: {}},
		)
		assert.Equal(t, map[string]struct{}{"already-excluded": {}}, existing, "caller's set never mutated")
		// v0 has no capped catalog binding, so nothing new is excluded and the
		// caller's set is returned unchanged (copy-on-write only fires on a
		// real exclusion); nothing is reported removed.
		assert.Equal(t, map[string]struct{}{"already-excluded": {}}, excluded)
		assert.Empty(t, removed)
	})
}

func TestApplyImageLimitPrefilter(t *testing.T) {
	// The consolidated ingress helper must behave like the raw filter plus the
	// per-surface boilerplate it replaced: narrowed pool, a copy of the
	// removed list for the hard-exclusion request fields, and the
	// request-scoped log line only when something was actually excluded.
	available := map[string]struct{}{imageCappedTestModel: {}}

	t.Run("fail-open request keeps pool, returns nil list, stays quiet", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := observability.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)))
		excluded, hardExcluded := applyImageLimitPrefilter(
			ctx, 17, map[string]struct{}{providers.ProviderAIAND: {}}, nil, nil, available,
		)
		assert.Nil(t, excluded)
		assert.Nil(t, hardExcluded)
		assert.NotContains(t, buf.String(), "image limit pre-filter",
			"no exclusion fires, so the request-scoped logger must not be touched")
	})
	t.Run("caller's exclusion set passes through copy-on-write", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := observability.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)))
		existing := map[string]struct{}{"already-excluded": {}}
		excluded, hardExcluded := applyImageLimitPrefilter(
			ctx, 17, map[string]struct{}{providers.ProviderAIAND: {}}, nil, existing, available,
		)
		assert.Equal(t, existing, excluded, "caller's set never mutated")
		assert.Nil(t, hardExcluded)
		assert.NotContains(t, buf.String(), "image limit pre-filter")
	})
}

func TestSafetyExcludedModelsIncludesImageLimits(t *testing.T) {
	// Five images: past any per-binding cap the filter could see, though the
	// v0 catalog carries no cap, so nothing is excluded. The test pins the
	// shape: the safety set re-runs the image filter against an empty base.
	env, err := translate.ParseAnthropic([]byte(`{"model":"` + imageCappedTestModel + `","messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"one"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"two"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"three"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"four"}},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"five"}}
	]}]}`))
	require.NoError(t, err)
	require.Equal(t, 5, env.ImageCount())
	s := &Service{availableModels: map[string]struct{}{imageCappedTestModel: {}}}
	excluded := s.safetyExcludedModels(env, 0, map[string]struct{}{providers.ProviderAIAND: {}}, nil)
	assert.Nil(t, excluded, "no v0 binding declares an image cap, so nothing is excluded")
}

func TestResolveBindingsForDispatchKeepsExplicitForceWhenNoEligibleBinding(t *testing.T) {
	model := imageCappedTestModel
	s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderAIAND: {}}}
	bindings := s.resolveBindingsForDispatch(
		withRequestImageCount(context.Background(), 17),
		router.Decision{Model: model, Provider: providers.ProviderAIAND, Reason: translate.ReasonUserForceModel},
	)
	require.Len(t, bindings, 1)
	assert.Equal(t, providers.ProviderAIAND, bindings[0].Provider)
}

func TestSiblingRescueSkipsBindingOverImageLimit(t *testing.T) {
	var skipped rescueSkipReason
	decisions := walkRescueCandidates(
		router.Decision{Model: "z-ai/glm-5.3-flash", Provider: providers.ProviderAIAND},
		[]string{imageCappedTestModel},
		17,
		nil,
		nil,
		0,
		0,
		0,
		func(string) (string, bool) { return providers.ProviderAIAND, true },
		func(_ string, reason rescueSkipReason) { skipped = reason },
	)
	assert.Empty(t, decisions)
	assert.Equal(t, rescueSkipImageLimit, skipped)
}
