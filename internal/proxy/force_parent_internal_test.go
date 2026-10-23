package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForceParentRoutesChildrenAndSupportsScopeTransitions(t *testing.T) {
	const apiKeyID = "parent-scope-test-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-parent-session"})
	ctx = router.WithStrategy(ctx, router.StrategyCluster)
parent, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Implement a synthetic feature"}]}`, catalog.ModelIDGemma431B.String())))
	require.NoError(t, err)
child, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Research a synthetic helper"}]}`, catalog.ModelIDGemma431B.String())))
	require.NoError(t, err)
	parentKey := deriveSessionKeyForRequest(ctx, parent, apiKeyID)
	childKey := deriveSessionKeyForRequest(ctx, child, apiKeyID)
	controlKey := deriveForceModelSessionKeyForRequest(ctx, parent, apiKeyID, parentKey)
	require.NotEqual(t, parentKey, childKey)
	require.Equal(t, controlKey, deriveForceModelSessionKeyForRequest(ctx, child, apiKeyID, childKey))
	store := newForceModelMapStore()
	scorer := &tierProbeRouter{available: map[string]struct{}{catalog.ModelIDGLM53Flash.String(): {}}}
	svc := NewService(scorer, nil, nil, false, nil, store, false, providers.ProviderAIAND, catalog.ModelIDGLM53Flash.String(), nil)
	apply := func(command translate.ForceModelResult) string {
		t.Helper()
		_, ack, err := svc.applyForceModelCommand(ctx, command, installationID, parentKey, controlKey)
		require.NoError(t, err)
		return ack.render(parent.SourceFormat())
	}
	route := func(env *translate.RequestEnvelope) turnLoopResult {
		t.Helper()
		features := env.RoutingFeatures(false)
		routed, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		return routed
	}
	apply(translate.ForceModelResult{Model: "opus-4-8"})
	require.Equal(t, catalog.ModelIDGLM53.String(), route(child).Decision.Model, "force-model retains conversation scope")
	// Reproduce a legacy child force, which must not revive when narrowing scope.
	require.NoError(t, store.Upsert(ctx, sessionpin.Pin{
		SessionKey: childKey, Role: roleForTier(catalog.TierHigh), InstallationID: installationID,
	Model: catalog.ModelIDGemma431B.String(), Provider: providers.ProviderAIAND,
		Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour),
	}))
	ack := apply(translate.ForceModelResult{Model: "opus-4-8:high", ParentOnly: true})
	assert.Contains(t, ack, "force-parent applied:")
	assert.Contains(t, ack, "Subagents use automatic routing")
	features := parent.RoutingFeatures(false)
	inheritedChild, err := svc.runTurnLoop(ctx, parent, features, apiKeyID, installationID, "explore", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, catalog.ModelIDGLM53Flash.String(), inheritedChild.Decision.Model, "identified subagents must route even when they inherit the parent transcript")
	parentRoute := route(parent)
assert.Equal(t, catalog.ModelIDGLM53.String(), parentRoute.Decision.Model)
	assert.Equal(t, "high", parentRoute.Decision.Effort)
	childRoute := route(child)
	assert.Equal(t, catalog.ModelIDGLM53Flash.String(), childRoute.Decision.Model)
	assert.NotEqual(t, translate.ReasonUserForceModel, childRoute.Decision.Reason)
	require.NotEmpty(t, scorer.captured, "child must reach automatic routing")
	stored, found, err := store.Get(ctx, controlKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sessionKeyHex(parentKey), stored.ForcedThreadKey)
	assert.Contains(t, apply(translate.ForceModelResult{Model: "not-a-catalog-model", ParentOnly: true}), "isn't a recognized model")
assert.Equal(t, catalog.ModelIDGLM53.String(), route(parent).Decision.Model, "rejected commands preserve the active scope")
	apply(translate.ForceModelResult{Model: "opus-4-8"})
assert.Equal(t, catalog.ModelIDGLM53.String(), route(child).Decision.Model, "force-model widens scope again")
	apply(translate.ForceModelResult{Model: "opus-4-8", ParentOnly: true})
	apply(translate.ForceModelResult{Clear: true})
	assert.Equal(t, catalog.ModelIDGLM53Flash.String(), route(parent).Decision.Model, "clear resumes parent automatic routing")
	assert.Equal(t, catalog.ModelIDGLM53Flash.String(), route(child).Decision.Model, "clear keeps children automatic")
}

func TestForceParentDoesNotOverrideChildCompaction(t *testing.T) {
	const apiKeyID = "parent-compaction-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "parent-compaction-session"})
	parent, err := translate.ParseAnthropic([]byte(`{"model":"deepseek/deepseek-v4.1-flash","max_tokens":4096,"messages":[{"role":"user","content":"Implement the parser"}]}`))
	require.NoError(t, err)
	compaction, err := translate.ParseAnthropic([]byte(`{"model":"deepseek/deepseek-v4.1-flash","max_tokens":4096,"messages":[{"role":"user","content":"Implement the parser"},{"role":"assistant","content":"Inspecting the source"},{"role":"user","content":"Your task is to create a detailed summary. Do not call any tools."}]}`))
	require.NoError(t, err)
	store := newForceModelMapStore()
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAIAND, catalog.ModelIDGLM53.String(), nil).WithCompactionHardPin(true)
	parentKey := deriveSessionKeyForRequest(ctx, parent, apiKeyID)
	require.Equal(t, parentKey, deriveSessionKeyForRequest(ctx, compaction, apiKeyID))
	features := compaction.RoutingFeatures(false)
	route := func() turnLoopResult {
		t.Helper()
		turn, err := svc.runTurnLoop(ctx, compaction, features, apiKeyID, installationID, "explore", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		return turn
	}
	baseline := route()
	controlKey := deriveForceModelSessionKeyForRequest(ctx, parent, apiKeyID, parentKey)
_, _, err = svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: catalog.ModelIDGemma431B.String(), ParentOnly: true}, installationID, parentKey, controlKey)
	require.NoError(t, err)
	child := route()
	assert.Equal(t, baseline.Decision.Model, child.Decision.Model, "a parent force must not change the child's compaction model")
	assert.NotEqual(t, translate.ReasonUserForceModel, child.Decision.Reason)
}

func TestForceParentPreservesChildLoopRescue(t *testing.T) {
	for _, test := range []struct {
		name       string
		parentOnly bool
		child      bool
		wantAction string
	}{
		{"parent-only child", true, true, loopActionEscalated},
		{"parent-only parent", true, false, loopActionUserForced},
		{"conversation-wide child", false, true, loopActionUserForced},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			installationID := uuid.New()
			threadKey, controlKey := loopTestKey(50), loopTestKey(51)
			store := newForceModelMapStore()
			events := &recordingLoopStore{}
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAIAND, catalog.ModelIDGLM53Flash.String(), nil).WithLoopEscalationStore(events)
			_, _, err := svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: catalog.ModelIDGLM53.String(), ParentOnly: test.parentOnly}, installationID, threadKey, controlKey)
			require.NoError(t, err)
			svc.handleLoopEscalation(ctx, loopTestSig, 24, 0.2, 30, installationID, threadKey, sessionpin.DefaultRole, catalog.ModelIDGLM53Flash.String(), test.child, controlKey)
			require.Len(t, events.events, 1)
			assert.Equal(t, test.wantAction, events.events[0].Action)
			if test.wantAction == loopActionEscalated {
				assert.Equal(t, catalog.ModelIDGLM53Flash.String(), events.events[0].LoopingModel)
				pin, found, err := store.Get(ctx, threadKey, sessionpin.DefaultRole)
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, escalateModel, pin.Model)
			}
		})
	}
}
