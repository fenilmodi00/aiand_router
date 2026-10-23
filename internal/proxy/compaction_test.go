package proxy

import (
	"context"
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/cluster"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rolePinStore serves a distinct pin per role so the thread pin and the
// _hmm_history row can disagree.
type rolePinStore struct {
	stubPinStore
	byRole map[string]sessionpin.Pin
}

func (s *rolePinStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.byRole[role]
	return pin, found, nil
}

// A compaction turn whose every summarizer was ruled out only by the size
// estimate must surface the native prompt-too-long error. A retryable 503
// "cluster scorer failed" kept Claude Code retrying the same compaction.
func TestTurnLoop_CompactionHardPinContextOverflowIsNotRoutingOutage(t *testing.T) {
	const summarizer = "z-ai/glm-5.3-flash"
	resolver := func(req HardPinRequest) (string, string, bool) {
		if _, excluded := req.ExcludedModels[summarizer]; excluded {
			return "", "", false
		}
		return providers.ProviderAIAND, summarizer, true
	}
	run := func(t *testing.T, resolver HardPinResolver, contextWindowExcluded map[string]struct{}) error {
		s := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil, &rolePinStore{}, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil).
			WithHardPinResolver(resolver)
		s.compactionHardPinEnabled = true
		s.availableModels = map[string]struct{}{summarizer: {}, policy.PrecompactionDefaultModel: {}}
		env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3","max_tokens":1024,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`))
		require.NoError(t, err)
		features := env.RoutingFeatures(false)
		_, err = s.runTurnLoop(context.Background(), env, features, "test-key", uuid.Nil, "", nil, router.Request{
			RequestedModel:              features.Model,
			EnabledProviders:            map[string]struct{}{providers.ProviderAIAND: {}},
			ExcludedModels:              map[string]struct{}{summarizer: {}, policy.PrecompactionDefaultModel: {}},
			ContextWindowExcludedModels: contextWindowExcluded,
		})
		return err
	}

	sizeExcluded := map[string]struct{}{summarizer: {}, policy.PrecompactionDefaultModel: {}}
	err := run(t, resolver, sizeExcluded)
	require.ErrorIs(t, err, policy.ErrContextWindowExceeded)
	assert.NotErrorIs(t, err, cluster.ErrClusterUnavailable)
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, class.Status)

	outsideBundle := func(HardPinRequest) (string, string, bool) { return "", "", false }
	err = run(t, outsideBundle, sizeExcluded)
	assert.ErrorIs(t, err, policy.ErrContextWindowExceeded,
		"a summarizer only the compaction path can reach still classifies the size-only failure")

	err = run(t, resolver, nil)
	assert.ErrorIs(t, err, cluster.ErrClusterUnavailable, "exclusions unrelated to size keep the routing-outage class")
}
