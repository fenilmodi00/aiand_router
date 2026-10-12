package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const recoveryOpusModel = "moonshotai/kimi-k3"

// recoverySiblingStream is a complete Responses-shaped success stream for the
// rescuing sibling arm: every roster row is AIand-only, so an Anthropic turn
// landing on AIand dispatches on /v1/responses and the translator reads it
// back from Responses frames.
const recoverySiblingStream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"total_tokens\":6}}}\n\n"

// modelDispatchClient routes each attempt by decision.Model so a single
// AIAND registration can play both the failing primary arm and the sibling
// that rescues the turn.
type modelDispatchClient struct {
	byModel map[string]providers.Client
}

func (m *modelDispatchClient) Proxy(ctx context.Context, d router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	client, ok := m.byModel[d.Model]
	if !ok {
		return &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}
	}
	return client.Proxy(ctx, d, prep, w, r)
}

func (m *modelDispatchClient) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	return &providers.UpstreamErrorResponse{Status: http.StatusNotImplemented}
}

func TestTwoModelGatewayRecoveryReadmitsStruckSibling(t *testing.T) {
	primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
	siblingStream := recoverySiblingStream
	sibling := &fakeClient{name: "recovery", outcomes: []fakeOutcome{
		{writeBytes: []byte(siblingStream)},
		{writeBytes: []byte(siblingStream)},
		{writeBytes: []byte(siblingStream)},
	}}
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierFor("zai-org/glm-5.3"))): {
			Strategy: router.StrategyCluster, DemotedModels: []string{"zai-org/glm-5.3", recoveryOpusModel},
		},
	}}
	decision := router.Decision{Provider: providers.ProviderAIAND, Model: "zai-org/glm-5.3", Metadata: &router.RoutingMetadata{
		RosterFailover:    true,
		RescueModels:      []string{"zai-org/glm-5.3"},
		SidecarRescuePool: []string{recoveryOpusModel},
		CandidateModels:   []string{"zai-org/glm-5.3"},
	}}
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderAIAND: &modelDispatchClient{byModel: map[string]providers.Client{
			"zai-org/glm-5.3": primary,
			recoveryOpusModel: sibling,
		}},
	}, nil, false, nil, store, false, providers.ProviderAIAND, recoveryOpusModel, nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil })
	svc.now = func() time.Time { return rateLimitTestNow }
	body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"synthetic exhausted pool"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(twoModelRecoveryContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	assert.Equal(t, 3, primary.calls)
	assert.GreaterOrEqual(t, sibling.calls, 1)
	require.NoError(t, err, "after both strikes a precommit primary failure must still try the permitted sibling")
	assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
	assert.Contains(t, rec.Body.String(), "message_stop")
}

func TestTwoModelRecoveryOnlyLeasesOneExpiredArmPerRequest(t *testing.T) {
	cooldowns := map[string]time.Time{
		"zai-org/glm-5.3": rateLimitTestNow,
		recoveryOpusModel: rateLimitTestNow,
	}
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(demotionTurnRole): {Strategy: router.StrategyCluster, DemotionCooldowns: cooldowns},
	}}
	svc, _ := cooldownTurnLoopService(store, true)
	result := runDemotionTurnLoop(t, svc, context.Background())
	defer result.releaseCooldownProbes()

	require.Len(t, result.CooldownProbeReleases, 1, "one request must not lock both allowed models while selecting its probe")
	// Equal expiries tie-break by name, so the Opus arm is the probe.
	assert.Equal(t, map[string]time.Time{recoveryOpusModel: rateLimitTestNow}, result.CooldownProbes)
	assert.Equal(t, []string{"zai-org/glm-5.3"}, result.SessionDemotedModels, "the expired arm this request did not lease stays out of the turn")
}

// An in-process lease is a safety net for stores without durable leases: a
// request that never released must not deny the arm forever.
func TestLocalRecoveryProbeLeaseExpires(t *testing.T) {
	svc, _ := cooldownTurnLoopService(&rolePinStore{byRole: map[string]sessionpin.Pin{}}, true)
	now := rateLimitTestNow
	svc.now = func() time.Time { return now }
	key := nonZeroSessionKey()

	_, acquired := svc.acquireRecoveryProbe(context.Background(), key, recoveryOpusModel)
	require.True(t, acquired)
	_, acquired = svc.acquireRecoveryProbe(context.Background(), key, recoveryOpusModel)
	assert.False(t, acquired, "a live lease admits one probe")

	now = now.Add(recoveryProbeLeaseDuration)
	release, acquired := svc.acquireRecoveryProbe(context.Background(), key, recoveryOpusModel)
	require.True(t, acquired, "an abandoned lease frees after its safety duration")
	release()
	_, acquired = svc.acquireRecoveryProbe(context.Background(), key, recoveryOpusModel)
	assert.True(t, acquired, "a released lease frees immediately")
}

func TestRecoveryProbeLeaseCoversRequestDeadline(t *testing.T) {
	svc, _ := cooldownTurnLoopService(&rolePinStore{byRole: map[string]sessionpin.Pin{}}, true)

	assert.Equal(t, recoveryProbeLeaseDuration, svc.recoveryProbeLeaseFor(context.Background()))

	ctx, cancel := context.WithDeadline(context.Background(), rateLimitTestNow.Add(30*time.Minute))
	defer cancel()
	assert.Equal(t, 31*time.Minute, svc.recoveryProbeLeaseFor(ctx), "a longer request keeps its lease one minute past the deadline")

	short, cancelShort := context.WithDeadline(context.Background(), rateLimitTestNow.Add(time.Minute))
	defer cancelShort()
	assert.Equal(t, recoveryProbeLeaseDuration, svc.recoveryProbeLeaseFor(short), "a short request keeps the safety duration")
}

type clearedCooldown struct {
	role, model string
	until       time.Time
}

// recoveryLeaseStore is rolePinStore with a durable-lease contract so the
// proxy takes the shared-lease path and reports recoveries to the store.
type recoveryLeaseStore struct {
	rolePinStore
	leaseFor  []time.Duration
	cleared   []clearedCooldown
	attempted []string
	denyAll   bool
}

func (s *recoveryLeaseStore) AcquireRecoveryProbe(_ context.Context, _ [sessionpin.SessionKeyLen]byte, model string, _ uuid.UUID, leaseFor time.Duration) (bool, error) {
	s.attempted = append(s.attempted, model)
	s.leaseFor = append(s.leaseFor, leaseFor)
	return !s.denyAll, nil
}

func (s *recoveryLeaseStore) ReleaseRecoveryProbe(context.Context, [sessionpin.SessionKeyLen]byte, string, uuid.UUID) error {
	return nil
}

func (s *recoveryLeaseStore) ClearDemotionCooldown(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, model string, observedUntil time.Time) error {
	s.cleared = append(s.cleared, clearedCooldown{role: role, model: model, until: observedUntil})
	return nil
}

var _ sessionpin.RecoveryProbeStore = (*recoveryLeaseStore)(nil)

// A durable lease is requested as a duration, never a worker-clock instant,
// and a probe that served cleanly lifts its cooldown from both rows the next
// turn merges, keyed to the expiry it observed.
func TestRecoveredProbeClearsCooldownFromBothRows(t *testing.T) {
	expired := rateLimitTestNow.Add(-time.Second)
	store := &recoveryLeaseStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(demotionTurnRole): {Strategy: router.StrategyCluster, DemotionCooldowns: map[string]time.Time{recoveryOpusModel: expired}},
	}}}
	svc, _ := cooldownTurnLoopService(store, true)
	result := runDemotionTurnLoop(t, svc, context.Background())
	result.releaseCooldownProbes()
	require.Equal(t, []time.Duration{recoveryProbeLeaseDuration}, store.leaseFor)
	require.Equal(t, map[string]time.Time{recoveryOpusModel: expired}, result.CooldownProbes)

	svc.clearRecoveredCooldown(context.Background(), result, freshTurnModel, nil)
	assert.Empty(t, store.cleared, "a turn served by another model recovers nothing")

	svc.clearRecoveredCooldown(context.Background(), result, recoveryOpusModel, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway})
	assert.Empty(t, store.cleared, "a failed probe leaves the cooldown for the strike to rewrite")

	svc.clearRecoveredCooldown(context.Background(), result, recoveryOpusModel, nil)
	assert.Equal(t, []clearedCooldown{
		{role: demotionTurnRole, model: recoveryOpusModel, until: expired},
		{role: hmmHistoryRole(demotionTurnRole), model: recoveryOpusModel, until: expired},
	}, store.cleared)
}

func TestRecoveryProbeLeaseDeniedModelsAreHardExcluded(t *testing.T) {
	cooldowns := map[string]time.Time{
		"zai-org/glm-5.3": rateLimitTestNow,
		recoveryOpusModel: rateLimitTestNow,
	}
	store := &recoveryLeaseStore{
		rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
			hmmHistoryRole(demotionTurnRole): {Strategy: router.StrategyCluster, DemotionCooldowns: cooldowns},
		}},
		denyAll: true,
	}
	svc, scorer := cooldownTurnLoopService(store, true)
	result := runDemotionTurnLoop(t, svc, context.Background())

	assert.Empty(t, result.CooldownProbes)
	assert.ElementsMatch(t, []string{"zai-org/glm-5.3", recoveryOpusModel}, result.CooldownProbeDeniedModels)
	require.Len(t, scorer.requests, 1)
	for _, model := range []string{"zai-org/glm-5.3", recoveryOpusModel} {
		assert.Contains(t, scorer.requests[0].ExcludedModels, model, "lease-denied models must not be restored by soft automatic-exclusion fallback")
	}
	ctx := context.WithValue(context.Background(), SessionCooldownProbeDeniedModelsContextKey{}, result.CooldownProbeDeniedModels)
	assert.Contains(t, svc.rescueExcludedModels(ctx), "zai-org/glm-5.3", "rescue must not readmit a model whose probe is leased elsewhere")
	assert.Contains(t, svc.rescueExcludedModels(ctx), recoveryOpusModel)
}

func TestRecoveryProbeSkipsHardExcludedExpiredModel(t *testing.T) {
	excludedModel := "zai-org/glm-5.3"
	eligibleModel := recoveryOpusModel
	store := &recoveryLeaseStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(demotionTurnRole): {Strategy: router.StrategyCluster, DemotionCooldowns: map[string]time.Time{
			excludedModel: rateLimitTestNow.Add(-time.Minute),
			eligibleModel: rateLimitTestNow,
		}},
	}}}
	svc, _ := cooldownTurnLoopService(store, true)
	ctx := context.WithValue(context.Background(), InstallationExcludedModelsContextKey{}, []string{excludedModel})
	result := runDemotionTurnLoop(t, svc, ctx)
	defer result.releaseCooldownProbes()

	assert.Equal(t, []string{eligibleModel}, store.attempted, "an ineligible old cooldown must not consume the only probe slot")
	assert.Equal(t, map[string]time.Time{eligibleModel: rateLimitTestNow}, result.CooldownProbes)
	assert.NotContains(t, result.SessionDemotedModels, eligibleModel)
}

func TestRecoveryProbeSkipsModelWithoutEligibleProviderBinding(t *testing.T) {
	store := &recoveryLeaseStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(demotionTurnRole): {Strategy: router.StrategyCluster, DemotionCooldowns: map[string]time.Time{
			recoveryOpusModel: rateLimitTestNow.Add(-time.Minute),
		}},
	}}}
	svc, _ := cooldownTurnLoopService(store, true)
	env, features := demotionTurnLoopEnv(t)
	result, err := svc.runTurnLoop(context.Background(), env, features, "api-key", uuid.New(), "", http.Header{}, router.Request{
		RequestedModel:       features.Model,
		EnabledProviders:     map[string]struct{}{providers.ProviderAnthropic: {}},
		EstimatedInputTokens: features.Tokens,
		HasTools:             features.HasTools,
		ConversationMessages: conversationMessagesForRouting(env),
	})
	require.NoError(t, err)
	defer result.releaseCooldownProbes()

	assert.Empty(t, store.attempted, "a model with no binding for this request must not consume the recovery lease")
	assert.Empty(t, result.CooldownProbes)
}

// twoModelRecoveryContext allows both roster models on a deployment-key
// turn: no BYOK keys, so the deployment-key failover walk serves the sibling.
func twoModelRecoveryContext() context.Context {
	return context.WithValue(rescuedFailureCtx(), InstallationAllowedModelsContextKey{}, []string{"zai-org/glm-5.3", recoveryOpusModel})
}

func TestTwoModelGatewayRecoveryRetriesThenRecordsWithdrawal(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		status    int
		transient bool
		cooldown  bool
		reason    sessionpin.DemotionReason
	}{
		{name: "502 gets bounded cooldown", status: http.StatusBadGateway, transient: true, cooldown: true, reason: sessionpin.DemotionReasonTransientFailure},
		{name: "429 flag off remains permanent", status: http.StatusTooManyRequests, reason: sessionpin.DemotionReasonRescuedFailure},
		{name: "429 flag on has bounded cooldown", status: http.StatusTooManyRequests, transient: true, cooldown: true, reason: sessionpin.DemotionReasonRateLimited},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := &cooldownStubPinStore{}
			primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: scenario.status}}
			sibling := &fakeClient{outcomes: []fakeOutcome{
				{writeBytes: []byte(recoverySiblingStream)},
				{writeBytes: []byte(recoverySiblingStream)},
				{writeBytes: []byte(recoverySiblingStream)},
			}}
			decision := router.Decision{Provider: providers.ProviderAIAND, Model: "zai-org/glm-5.3", Metadata: &router.RoutingMetadata{
				RosterFailover:  true,
				RescueModels:    []string{"zai-org/glm-5.3", recoveryOpusModel},
				CandidateModels: []string{"zai-org/glm-5.3", recoveryOpusModel},
			}}
			svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
				providers.ProviderAIAND: &modelDispatchClient{byModel: map[string]providers.Client{
					"zai-org/glm-5.3": primary,
					recoveryOpusModel: sibling,
				}},
			}, nil, false, nil, store, false, providers.ProviderAIAND, recoveryOpusModel, nil).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
				WithRescuedFailureArmDemotion(true).WithTransientRateLimit(scenario.transient, 45)
			now := rateLimitTestNow
			svc.now = func() time.Time { return now }
			var retryWaits []time.Duration
			svc.WithRetrySleep(func(_ context.Context, wait time.Duration) error {
				retryWaits = append(retryWaits, wait)
				now = now.Add(wait)
				return nil
			})
			rec := httptest.NewRecorder()
			body := []byte(`{"model":"zai-org/glm-5.3","stream":true,"messages":[{"role":"user","content":"synthetic recovery check"}]}`)
			err := svc.ProxyMessages(twoModelRecoveryContext(), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			require.NoError(t, err)
			assert.Equal(t, 3, primary.calls)
			assert.GreaterOrEqual(t, sibling.calls, 1)
			assert.Len(t, retryWaits, 2)
			assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
			assert.Contains(t, rec.Body.String(), "message_stop")
			if scenario.cooldown {
				assert.Empty(t, store.demotions)
				require.Len(t, store.cooldowns, 2)
				for _, withdrawal := range store.cooldowns {
					assert.Equal(t, "zai-org/glm-5.3", withdrawal.model)
					assert.Equal(t, now.Add(45*time.Second), withdrawal.until)
					assert.Equal(t, scenario.reason, withdrawal.reason)
				}
			} else {
				assert.Empty(t, store.cooldowns)
				require.Len(t, store.demotions, 2)
				for _, withdrawal := range store.demotions {
					assert.Equal(t, "zai-org/glm-5.3", withdrawal.model)
					assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, withdrawal.reason)
				}
			}
		})
	}
}

// A fixed expiry must survive history refreshes. Captured writes test the Go
// writer contract; this deliberately does not reimplement SQL merge semantics.
func TestTwoModelRecoveryHMMRefreshDoesNotRewriteCooldown(t *testing.T) {
	until := rateLimitTestNow.Add(45 * time.Second)
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(demotionTurnRole): coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: until}),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)
	now := rateLimitTestNow
	svc.now = func() time.Time { return now }
	res := turnLoopResult{InstallationID: uuid.New(), SessionKey: nonZeroSessionKey(), PinRole: sessionpin.DefaultRole,
		Decision: router.Decision{Provider: providers.ProviderAIAND, Model: freshTurnModel, Reason: hmmHistoryReason}}
	for step := 0; step <= 96; step++ {
		now = rateLimitTestNow.Add(time.Duration(step) * 30 * time.Minute)
		svc.recordHMMTurnHistory(res, providers.ProviderAIAND, freshTurnModel, 0, 0, 0, 0, false)
		turn := runDemotionTurnLoop(t, svc, context.Background())
		turn.releaseCooldownProbes()
		if step == 0 {
			assert.Contains(t, turn.SessionDemotedModels, demotedPinModel)
		} else {
			assert.NotContains(t, turn.SessionDemotedModels, demotedPinModel)
			assert.NotContains(t, scorer.requests[len(scorer.requests)-1].AutomaticExcludedModels, demotedPinModel)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, write := range store.upserts {
		assert.Empty(t, write.DemotionCooldowns, "history/pin refresh must not emit a new cooldown")
		assert.Empty(t, write.DemotedModels, "history/pin refresh must not manufacture a permanent strike")
	}
}
