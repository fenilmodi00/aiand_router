package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const classifierTestSubToken = "sk-ant-oat01-classifier-subscription-token"

// The decision carries the classifier reason, and a classifier that also
// satisfies the opt-in usage-bypass gate is still attributed to the classifier
// lane (checked first) so telemetry can tell the two apart.
func TestUsageBypassDecision_ClassifierLaneOutranksUsageBypass(t *testing.T) {
	const model = "deepseek/deepseek-v4.1-flash"
	threshold := 0.80
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(classifierTestSubToken)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.20, WindowMinutes: 300},
	})
	svc := &Service{clients: dispatch.NewClients(map[string]providers.Client{providers.ProviderAIAND: &bypassFakeProvider{}}), usageObserver: obs}
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})
	req := router.Request{RequestedModel: model}

	classifier, ok := svc.usageBypassDecision(ctx, http.Header{}, req, nil, turntype.Classifier)
	require.True(t, ok)
	assert.Equal(t, router.Decision{Provider: providers.ProviderAIAND, Model: model, Reason: reasonClassifierPassthrough}, classifier)

	mainLoop, ok := svc.usageBypassDecision(ctx, http.Header{}, req, nil, turntype.MainLoop)
	require.True(t, ok)
	assert.Equal(t, router.Decision{Provider: providers.ProviderAIAND, Model: model, Reason: reasonUsageBypass}, mainLoop)

	_, ok = svc.usageBypassDecision(ctx, http.Header{}, req, []string{model}, turntype.Classifier)
	assert.False(t, ok, "a session strike on the requested model blocks the classifier lane too")

	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{model})
	_, ok = svc.usageBypassDecision(ctx, http.Header{}, req, nil, turntype.Classifier)
	assert.False(t, ok, "subscription-state model sets disable classifier usage bypass")
}

func TestRunTurnLoop_ClassifierPassthrough_NoSessionState(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3-flash","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{Provider: providers.ProviderAIAND, Model: "gpt-5.5", Reason: "cluster", PinnedUntil: time.Now().Add(time.Hour)}
	svc := NewService(&betaTestRouter{}, map[string]providers.Client{providers.ProviderAIAND: &bypassFakeProvider{}}, nil, false, nil, store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: &betaTestRouter{}})
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{"deepseek/deepseek-v4.1-flash"})
	var zeroKey [sessionpin.SessionKeyLen]byte

	res, err := svc.runTurnLoop(ctx, env, feats, "api-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})

	require.NoError(t, err)
	assert.Equal(t, turntype.Classifier, res.TurnType)
	assert.False(t, res.UsageBypass, "classifier turns must use their classifier policy even when subscription quota is available")
	assert.Empty(t, res.Decision)
	assert.Equal(t, zeroKey, res.SessionKey, "a classifier carries no session key, so nothing can be written back")
	assert.Empty(t, res.PriorServedModel, "the conversation's pin is not consulted for a classifier")
	assert.False(t, res.SessionEverSwitched)
}

func TestRunTurnLoop_ClassifierForceModel_OutranksPassthrough(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3-flash","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	feats := env.RoutingFeatures(false)
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: &bypassFakeProvider{}}, nil, false, nil, newStubPinStore(), false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, classifierTestSubToken)

	res, err := svc.runTurnLoop(ctx, env, feats, "api-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model, ForceModel: "deepseek/deepseek-v4-pro"})

	require.NoError(t, err)
	assert.False(t, res.UsageBypass)
	assert.Equal(t, translate.ReasonUserForceModel, res.Decision.Reason)
	assert.Equal(t, "deepseek/deepseek-v4-pro", res.Decision.Model)
}
