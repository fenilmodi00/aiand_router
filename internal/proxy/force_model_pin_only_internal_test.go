package proxy

import (
	"context"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue invariant: auto mode can only ever serve an approved model; everything
// else is pin-only. Pin-only means an explicit /force-model (or the
// x-aiand-force-model header) still dispatches the model — being outside the
// auto pool must never fail a user's explicit choice, because the user chose
// it deliberately. These tests pin that on the catalog's untiered rows.

// TestRunTurnLoop_ForceModelPinOnlyModelStillDispatches: a session pin on an
// untiered (pin-only) catalog model is served on the force path — the scorer
// is never consulted, and no automatic-only gate drops the pin.
func TestRunTurnLoop_ForceModelPinOnlyModelStillDispatches(t *testing.T) {
	const apiKeyID = "api-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "client-session"})
	env, err := translate.ParseAnthropic([]byte(`{
		"model":"openai/gpt-oss-120b",
		"messages":[{"role":"user","content":"task on the pin-only model"}]
	}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	threadKey := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, threadKey)

	// Test premise: the forced model really is pin-only (untiered).
	m, ok := catalog.ByID("openai/gpt-oss-120b")
	require.True(t, ok)
	require.Equal(t, catalog.TierUnknown, m.Tier, "test premise: gpt-oss-120b is pin-only")
	require.False(t, catalog.IsApprovedAutoRouted("openai/gpt-oss-120b"))

	store := newForceModelMapStore()
	store.pins[forceModelMapKey(forceKey, forceModelSessionRole)] = sessionpin.Pin{
		SessionKey:     forceKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Provider:       providers.ProviderAIAND,
		Model:          "openai/gpt-oss-120b",
		Reason:         translate.ReasonUserForceModel,
		Strategy:       router.StrategyCluster,
		PinnedUntil:    pinNeverExpires,
	}
	freshRouter := &tierProbeRouter{available: map[string]struct{}{"z-ai/glm-5.3-flash": {}}}
	svc := NewService(freshRouter, nil, nil, false, nil, store, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	result, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{
		RequestedModel: features.Model,
	})
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-oss-120b", result.Decision.Model)
	assert.Equal(t, translate.ReasonUserForceModel, result.Decision.Reason)
	assert.True(t, result.StickyHit)
	assert.False(t, result.HardPinned)
	assert.Empty(t, freshRouter.captured, "user force must bypass the scorer")
}

// TestRunTurnLoop_ForceModelPinOnlyModelResolvesThroughHeader: the
// x-aiand-force-model header path resolves a pin-only catalog ID to a
// servable pin rather than refusing it as unknown.
func TestResolveForceModel_PinOnlyModelIsKnown(t *testing.T) {
	for _, id := range []string{
		"deepseek/deepseek-v4-pro-0813",
		"google/gemma-4-31b",
		"qwen/qwen3.6-27b",
		"openai/gpt-oss-120b",
	} {
		m, ok := catalog.ByID(id)
		require.Truef(t, ok, "test premise: %q is a catalog row", id)
		require.Equalf(t, catalog.TierUnknown, m.Tier, "test premise: %q is pin-only", id)
		canonical, provider, known := resolveForceModel(id)
		require.Truef(t, known, "pin-only model %q must be forceable", id)
		assert.Equalf(t, id, canonical, "pin-only model %q must force to itself", id)
		assert.Equalf(t, providers.ProviderAIAND, provider, "pin-only model %q must pin to its catalog binding", id)
	}
}
