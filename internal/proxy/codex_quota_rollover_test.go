package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy/usage"
)

// codexSubscriptionTestCtx returns a ctx carrying the installation id the
// subscription-aware credential resolution keys on.
func codexSubscriptionTestCtx() context.Context {
	return context.WithValue(context.Background(), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
}

func TestCodexExtraCreditsRechecksPersonalIncludedObservation(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		client := openaicompat.NewClient("", "")
		observer := usage.NewObserver([]byte("synthetic-credit-salt"), time.Minute, time.Now)
		ctx := codexSubscriptionTestCtx()
		token, _ := presentSubscriptionTokens(ctx, http.Header{})
		require.NotEmpty(t, token)
		observer.Record(observer.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300, ResetAt: time.Now().Add(time.Hour)}})
		svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAIAND: client}).WithUsageObserver(observer).WithCodexExtraCredits(allowed)
		require.Equal(t, !allowed, svc.codexSubscriptionExhausted(ctx, http.Header{}))
	}
}
