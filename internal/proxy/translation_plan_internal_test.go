package proxy

import (
	"aiand-os/router/internal/dispatch"
	"context"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
)

func compatibilityService(mode TranslationCompatibilityMode) *Service {
	return &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAIAND: nil,
		}),
		translationCompatibilityMode: mode,
	}
}

func TestApplyTranslationPlan_IntrinsicallyIncompatible(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityEnforce)
	_, err := svc.applyTranslationPlan(context.Background(), router.Request{
		TranslationRequirements: router.TranslationRequirements{NativeOnly: true},
	})

	assert.ErrorIs(t, err, ErrTranslationIntrinsicallyIncompatible)
}

// The aiand catalog carries no CustomToolInputUnsupported rows, so a
// custom-tool request excludes nothing today; the requirement must still be
// wired to the enforced exclusion path in every compatibility mode, and the
// shared helper must hard-exclude whatever the set names.
func TestTranslationPlan_CustomToolExclusionIsEnforcedInEveryMode(t *testing.T) {
	for _, mode := range []TranslationCompatibilityMode{
		TranslationCompatibilityOff,
		TranslationCompatibilityShadow,
		TranslationCompatibilityEnforce,
	} {
		t.Run(string(mode), func(t *testing.T) {
			plan := compatibilityService(mode).planTranslation(router.Request{
				TranslationRequirements: router.TranslationRequirements{
					SourceFormat:  router.WireFormatOpenAI,
					Endpoint:      router.EndpointOpenAIResponses,
					FunctionTools: true,
					CustomTools:   true,
				},
			})
			assert.Empty(t, plan.ExcludedModels, "no aiand catalog model is flagged custom-tool unsupported")

			plan.excludeModels("custom_tool_unsupported", map[string]struct{}{"synthetic/no-input-wrapper": {}}, true)
			assert.Contains(t, plan.ExcludedModels, "synthetic/no-input-wrapper")
			requireModelExclusion(t, plan, "custom_tool_unsupported", "synthetic/no-input-wrapper", true)
		})
	}
}

// Shadow mode records the exclusion as evidence without removing the model.
func TestTranslationPlan_CustomToolExclusionShadowOnlyRecords(t *testing.T) {
	plan := compatibilityService(TranslationCompatibilityShadow).planTranslation(router.Request{
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:  router.WireFormatOpenAI,
			Endpoint:      router.EndpointOpenAIResponses,
			FunctionTools: true,
			CustomTools:   true,
		},
	})

	plan.excludeModels("custom_tool_unsupported", map[string]struct{}{"synthetic/no-input-wrapper": {}}, false)
	assert.NotContains(t, plan.ExcludedModels, "synthetic/no-input-wrapper")
	requireModelExclusion(t, plan, "custom_tool_unsupported", "synthetic/no-input-wrapper", false)
}

func TestTranslationPlan_FunctionToolsKeepCustomToolUnsupportedModels(t *testing.T) {
	plan := compatibilityService(TranslationCompatibilityEnforce).planTranslation(router.Request{
		TranslationRequirements: router.TranslationRequirements{
			SourceFormat:  router.WireFormatOpenAI,
			Endpoint:      router.EndpointOpenAIResponses,
			FunctionTools: true,
		},
	})

	assert.Empty(t, plan.ExcludedModels)
}

// compatibilityExcludesModel rules a model out for a custom-tool request only
// when the catalog flags it; the aiand rows all accept the wrapper.
func TestCompatibilityExcludesModel_CustomTools(t *testing.T) {
	req := router.Request{TranslationRequirements: router.TranslationRequirements{CustomTools: true}}
	for _, model := range catalog.Models {
		assert.False(t, compatibilityExcludesModel(req, model.ID), model.ID)
	}
	assert.Empty(t, catalog.CustomToolUnsupportedSet())
}

func requireModelExclusion(t *testing.T, plan TranslationPlan, code, model string, enforced bool) {
	t.Helper()
	for _, exclusion := range plan.Exclusions {
		if exclusion.Code == code && exclusion.Model == model {
			assert.Equal(t, enforced, exclusion.Enforced)
			return
		}
	}
	t.Fatalf("missing exclusion code=%q model=%q in %#v", code, model, plan.Exclusions)
}
