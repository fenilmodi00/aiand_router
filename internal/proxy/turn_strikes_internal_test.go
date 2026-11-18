package proxy

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"
)

type turnStrikePinStore struct {
	rolePinStore
	mu      sync.Mutex
	strikes map[string][]string
	cleared []string
}

func (s *turnStrikePinStore) StrikeTurnModel(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.strikes == nil {
		s.strikes = map[string][]string{}
	}
	s.strikes[role] = append(s.strikes[role], model)
	return nil
}

func (s *turnStrikePinStore) ClearTurnStrikes(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, role)
	return nil
}

var _ sessionpin.TurnStrikeStore = (*turnStrikePinStore)(nil)

// With session-lifetime demotion off, a rescued primary failure is still kept
// out for the agent turn on every row the next request merges.
func TestRescuedFailureStrikesModelForAgentTurn(t *testing.T) {
	store := &turnStrikePinStore{}
	svc := newRescuedDemotionTestService(store, false)
	ctx := withTurnFailures(context.Background())

	demoted, _ := strikeRescuedPrimary(svc, ctx, &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Headers: http.Header{}})
	assert.Empty(t, demoted, "no session-lifetime demotion while the flag is off")

	served := router.Decision{Model: freshTurnModel, Reason: "cluster:v0.2"}
	svc.strikeRescuedModelsForTurn(ctx, nil, served, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
	assert.Equal(t, map[string][]string{
		sessionpin.DefaultRole:                 {demotedArm},
		hmmHistoryRole(sessionpin.DefaultRole): {demotedArm},
	}, store.strikes)
}

func TestTurnStrikeSkippedWhenTurnFailedOrPinned(t *testing.T) {
	served := router.Decision{Model: freshTurnModel, Reason: "cluster:v0.2"}
	for name, strike := range map[string]func(*Service, context.Context){
		"turn failed": func(svc *Service, ctx context.Context) {
			svc.strikeRescuedModelsForTurn(ctx, assert.AnError, served, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
		"hard pinned": func(svc *Service, ctx context.Context) {
			svc.strikeRescuedModelsForTurn(ctx, nil, served, true, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
		"forced model": func(svc *Service, ctx context.Context) {
			forced := router.Decision{Model: freshTurnModel, Reason: translate.ReasonUserForceModel}
			svc.strikeRescuedModelsForTurn(ctx, nil, forced, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &turnStrikePinStore{}
			svc := newRescuedDemotionTestService(store, false)
			ctx := withTurnFailures(context.Background())
			recordTurnFailure(ctx, demotedArm)
			strike(svc, ctx)
			assert.Empty(t, store.strikes)
		})
	}
}
