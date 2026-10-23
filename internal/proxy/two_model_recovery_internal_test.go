package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/sessionpin"
)

const recoveryOpusModel = "z-ai/glm-5.2"

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

func TestRecoveryProbeSkipsModelWithoutEligibleProviderBinding(t *testing.T) {
	store := &recoveryLeaseStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Strategy: router.StrategyCluster, DemotionCooldowns: map[string]time.Time{
			recoveryOpusModel: rateLimitTestNow.Add(-time.Minute),
		}},
	}}}
	svc, _ := cooldownTurnLoopService(store, true)
	env, features := demotionTurnLoopEnv(t)
	result, err := svc.runTurnLoop(context.Background(), env, features, "api-key", uuid.New(), "", http.Header{}, router.Request{
		RequestedModel:       features.Model,
		EnabledProviders:     map[string]struct{}{providers.ProviderAIAND: {}},
		EstimatedInputTokens: features.Tokens,
		HasTools:             features.HasTools,
		HasImages:            true, // glm-5.2 accepts no images: no eligible binding
		ConversationMessages: conversationMessagesForRouting(env),
	})
	require.NoError(t, err)
	defer result.releaseCooldownProbes()

	assert.Empty(t, store.attempted, "a model with no binding for this request must not consume the recovery lease")
	assert.Empty(t, result.CooldownProbes)
}

func twoModelGatewayContext() context.Context {
	ctx := rescuedFailureCtx()
	ctx = context.WithValue(ctx, InstallationAllowedModelsContextKey{}, []string{catalog.ModelIDDeepSeekV4Pro.String(), recoveryOpusModel})
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("synthetic-key"), ModelAliases: map[string]string{catalog.ModelIDDeepSeekV4Pro.String(): catalog.ModelIDDeepSeekV4Pro.String()}},
		{Provider: providers.ProviderAIAND, Plaintext: []byte("synthetic-key"), ModelAliases: map[string]string{recoveryOpusModel: recoveryOpusModel}},
	})
}
