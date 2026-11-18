package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	outputLimitFirstModel  = "deepseek/deepseek-v4-pro"
	outputLimitSecondModel = "z-ai/glm-5.3"
)

func TestMaxedOutServedModel_RequiresMatchingTerminalEvidence(t *testing.T) {
	ended := time.Unix(100, 0)
	for _, output := range []int{128, 8192, 32000, 64000} {
		for _, tc := range []struct {
			name   string
			marker time.Time
			want   string
		}{
			{"legacy unknown", time.Time{}, ""},
			{"older writer replaced usage", ended.Add(-time.Second), ""},
			{"confirmed cap", ended, outputLimitFirstModel},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, output), func(t *testing.T) {
				pin := sessionpin.Pin{
					Model: outputLimitSecondModel, LastServedModel: outputLimitFirstModel + ":high",
					LastOutputTokens: output, LastTurnEndedAt: ended, LastOutputLimitAt: tc.marker,
				}
				assert.Equal(t, tc.want, maxedOutServedModel(pin))
				pin.LastServedModel = ""
				want := ""
				if tc.want != "" {
					want = outputLimitSecondModel
				}
				assert.Equal(t, want, maxedOutServedModel(pin), "legacy serving identity falls back to the anchor")
			})
		}
	}
}

func TestNormalizeHMMStayPin_HighOutputIsNotExhaustion(t *testing.T) {
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: nil}, nil, false, nil, nil, false,
		providers.ProviderAIAND, outputLimitFirstModel, nil)
	pin := sessionpin.Pin{Provider: providers.ProviderAIAND, LastServedModel: outputLimitFirstModel,
		LastOutputTokens: 32000, LastTurnEndedAt: time.Now().Add(-time.Second), PinnedUntil: time.Now().Add(time.Hour)}
	got, ok := svc.normalizeHMMStayPin(router.Request{}, pin)
	require.True(t, ok)
	assert.Equal(t, outputLimitFirstModel, got.Model)
	pin.LastOutputLimitAt = pin.LastTurnEndedAt
	_, ok = svc.normalizeHMMStayPin(router.Request{}, pin)
	assert.False(t, ok)
}

func TestPostCommandContinuation_UsesTerminalEvidence(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprintf("capped=%t", capped), func(t *testing.T) {
			store := newForceModelMapStore()
			key := [sessionpin.SessionKeyLen]byte{1}
			pin := sessionpin.Pin{SessionKey: key, Role: sessionpin.DefaultRole, Strategy: router.StrategyCluster,
				Provider: providers.ProviderAIAND, Model: outputLimitFirstModel, LastServedModel: outputLimitFirstModel,
				LastOutputTokens: 32000, LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour), Reason: "cluster"}
			if capped {
				pin.LastOutputLimitAt = pin.LastTurnEndedAt
			}
			store.pins[forceModelMapKey(key, pin.Role)] = pin
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAIAND, outputLimitFirstModel, nil)
			ctx := router.WithStrategy(context.Background(), router.StrategyCluster)
			svc.grantPostCommandContinuation(ctx, uuid.New(), key, pin.Role)
			got, found := svc.consumePostCommandContinuation(ctx, key, pin.Role)
			assert.Equal(t, !capped, found)
			if found {
				assert.Equal(t, outputLimitFirstModel, got.Model)
			}
			_, found = svc.consumePostCommandContinuation(ctx, key, pin.Role)
			assert.False(t, found, "a continuation remains one-shot")
		})
	}
}

func TestRefreshPin_PreservesOutputLimitEvidence(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAIAND, outputLimitFirstModel, nil)
	ended := time.Now().Add(-time.Second)
	pin := sessionpin.Pin{Provider: providers.ProviderAIAND, Model: outputLimitSecondModel, LastServedModel: outputLimitFirstModel,
		LastTurnEndedAt: ended, LastOutputLimitAt: ended, LastOutputTokens: 128}
	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{1}, pin, sessionpin.DefaultRole,
		router.Decision{Provider: providers.ProviderAIAND, Model: outputLimitSecondModel})
	require.Len(t, store.upserts, 1)
	assert.Equal(t, outputLimitFirstModel, maxedOutServedModel(store.upserts[0]), "refreshing the anchor cannot lose the prior served outcome")
}
