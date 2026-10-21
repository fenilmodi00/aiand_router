package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forceClusterRequest(t *testing.T, value string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	if value != "" {
		r.Header.Set(ForceClusterHeader, value)
	}
	return r
}

func TestApplyForceClusterHeader_AbsentIsNoOp(t *testing.T) {
	label, err := applyForceClusterHeader(context.Background(), forceClusterRequest(t, ""))

	require.NoError(t, err)
	assert.Empty(t, label)
}

func TestApplyForceClusterHeader_PlanOwnedRequestIgnoresHeader(t *testing.T) {
	ctx := router.WithStrategy(planOwnedContext(), router.StrategyHMM)

	label, err := applyForceClusterHeader(ctx, forceClusterRequest(t, "maximum"))

	require.NoError(t, err)
	assert.Empty(t, label)
}

func TestApplyForceClusterHeader_ThreadsLabelOnHMMStrategy(t *testing.T) {
	for _, strategy := range []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding, router.StrategyHMMBeta} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := router.WithStrategy(context.Background(), strategy)

			label, err := applyForceClusterHeader(ctx, forceClusterRequest(t, "  Maximum  "))

			require.NoError(t, err, "eligibility is only knowable after the sidecar answers")
			assert.Equal(t, "maximum", label, "the label is trimmed and lowercased, not validated here")
		})
	}
}

// The default cluster strategy scores anonymous centroids, so there is no named
// group to constrain to — refusing beats serving an unconstrained model.
func TestApplyForceClusterHeader_RejectsNonSidecarStrategy(t *testing.T) {
	ctx := router.WithStrategy(context.Background(), router.StrategyCluster)

	label, err := applyForceClusterHeader(ctx, forceClusterRequest(t, "maximum"))

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrForcedClusterUnsupportedStrategy))
	assert.Empty(t, label)

	cls, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.Contains(t, cls.Message, "maximum")
}

// ProxyMessages must refuse before routing, not after: a request that reached
// the scorer would be served from a cluster the caller didn't ask for.
func TestProxyMessages_ForceClusterOnNonSidecarStrategyRejects(t *testing.T) {
	fr := &stripFailureRouter{}
	fp := &stripFailureProvider{}
	svc := NewService(fr, map[string]providers.Client{providers.ProviderAIAND: fp}, nil, false, nil, nil, false,
		providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	body := `{"model":"z-ai/glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()

	err := svc.ProxyMessages(context.Background(), []byte(body), rec, forceClusterRequest(t, "maximum"))

	require.ErrorIs(t, err, ErrForcedClusterUnsupportedStrategy)
	assert.Zero(t, fr.routeCalls, "must abort before routing runs")
	assert.Zero(t, fp.proxyCalls, "must abort before dispatching upstream")
}

// forceClusterCapturingRouter records the request the turn loop routed with, so
// the header's journey from ingress to router.Request is provable.
type forceClusterCapturingRouter struct {
	captured router.Request
	decision router.Decision
}

func (r *forceClusterCapturingRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.captured = req
	return r.decision, nil
}
