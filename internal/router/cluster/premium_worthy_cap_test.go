package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

// A per-request cost-cap relaxation must let a premium lane win on merit for the
// turn that earned it, without touching the deployment cap: the deployment
// budget still decides every turn whose request carries nothing.
//
// Scenario (two lanes, three clusters, TopP=2 so the prompt's blend covers two
// premium-electing clusters):
//
//	premium $0.004/1k — leads clusters 0 and 1 on quality
//	cheap   $0.001/1k — leads cluster 2 on quality
//
// The prompt vector is equidistant from all three one-hot centroids, so TopP=2
// picks clusters 0 and 1 (ties broken by the lower cluster index) — the two the
// premium lane wins — and the blend therefore elects premium. At the deployment
// knee (3.0x) the premium lane's 4x sits outside the global budget and the cheap
// lane routes instead; at the premium-worthy rung (6.0x) it sits inside and the
// blend's own winner routes.
func TestScorer_PerRequestCostCapMultipleRelaxesCap(t *testing.T) {
	const (
		premiumModel = "claude-opus-4-7"  // $0.004/1k — 4x the cheap lane
		cheapModel   = "claude-haiku-4-5" // $0.001/1k
		clusters     = 3
	)
	topP := 2
	premiumIn, cheapIn := 0.004, 0.001
	ttft, tps := 0.2, 100.0
	axes := map[string]ModelAxis{
		premiumModel: {InputPer1KUSD: &premiumIn, TTFTSeconds: &ttft, TPS: &tps},
		cheapModel:   {InputPer1KUSD: &cheapIn, TTFTSeconds: &ttft, TPS: &tps},
	}
	quality := Rankings{
		0: {premiumModel: 0.9, cheapModel: 0.1},
		1: {premiumModel: 0.9, cheapModel: 0.1},
		2: {cheapModel: 0.9, premiumModel: 0.1},
	}
	knobs := &DefaultRoutingKnobs{
		Alpha:                []float64{0.9, 0.9, 0.9},
		SpeedWeight:          0.0,
		OutputCostRatio:      0.0,
		ExpectedOutputTokens: 2000,
		PerModelVerbosity:    false,
	}
	emb := &fakeEmbedder{vec: weightedVec(EmbedDim, clusters, 1.0, 0.0)}
	// The deployment knee ships, so a nil request field must reproduce it.
	capKnee := DefaultCostCapMultiple
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		qualityMeans:    quality,
		modelAxes:       axes,
		defaultKnobs:    knobs,
		clusterCount:    clusters,
		topP:            &topP,
		costCapMode:     CostCapModeGlobal,
		costCapMultiple: &capKnee,
	})

	baseReq := router.Request{PromptText: "agentic continuation"}

	// Deployment cap: the premium lane is 4x the cheap lane, outside the 3x
	// budget, so the blend's own winner never enters the candidate set.
	deployment, err := scorer.Route(context.Background(), baseReq)
	require.NoError(t, err)
	assert.Equal(t, cheapModel, deployment.Model, "the deployment cap must hold when the request carries nothing")
	require.NotNil(t, deployment.Metadata)
	assert.ElementsMatch(t, []string{cheapModel}, deployment.Metadata.CandidateModels,
		"the capped pool must exclude the premium lane")

	// Premium-worthy turn: the request's own clamp widens the budget to 6x, the
	// premium lane enters the candidate pool, and the blend's own winner routes.
	premiumWorthy := 6.0
	relaxedReq := baseReq
	relaxedReq.CostCapMultiple = &premiumWorthy
	relaxed, err := scorer.Route(context.Background(), relaxedReq)
	require.NoError(t, err)
	assert.Equal(t, premiumModel, relaxed.Model, "a premium-worthy request must make the premium lane win on merit")
	require.NotNil(t, relaxed.Metadata)
	assert.ElementsMatch(t, []string{premiumModel, cheapModel}, relaxed.Metadata.CandidateModels,
		"the relaxed cap widens the candidate pool instead of rewriting scores")
	assert.Equal(t, deployment.Metadata.ChosenScore, relaxed.Metadata.CandidateScores[cheapModel],
		"the cap widens the candidate set, never the scores behind it")

	// A clamp tighter than the deployment cap is still honored, so the field is
	// a clamp and not only a widening: 1x admits only the cheap lane.
	tighter := 1.0
	tightReq := baseReq
	tightReq.CostCapMultiple = &tighter
	tight, err := scorer.Route(context.Background(), tightReq)
	require.NoError(t, err)
	assert.Equal(t, cheapModel, tight.Model)
	require.NotNil(t, tight.Metadata)
	assert.ElementsMatch(t, []string{cheapModel}, tight.Metadata.CandidateModels)

	// A negative clamp is ignored so a mis-computed decode can never disable the
	// cap.
	negative := -1.0
	negReq := baseReq
	negReq.CostCapMultiple = &negative
	neg, err := scorer.Route(context.Background(), negReq)
	require.NoError(t, err)
	assert.Equal(t, deployment.Model, neg.Model, "a negative clamp must fall back to the deployment cap")
	assert.Equal(t, deployment.Metadata.CandidateModels, neg.Metadata.CandidateModels)
}

// The override must reach the cluster-mode cap path too, not only the global
// one. Cluster mode unions the global budget with each top cluster's own
// elected budget, so the cheapest lane can hold every cluster the prompt
// touches while a premium lane is admitted purely by the global term — which
// is exactly the argument the request field has to move.
func TestScorer_PerRequestCostCapMultipleClusterMode(t *testing.T) {
	const (
		premiumModel = "claude-opus-4-7"  // $0.004/1k — 4x the cheap lane
		cheapModel   = "claude-haiku-4-5" // $0.001/1k
		clusters     = 3
	)
	topP := 2
	premiumIn, cheapIn := 0.004, 0.001
	ttft, tps := 0.2, 100.0
	axes := map[string]ModelAxis{
		premiumModel: {InputPer1KUSD: &premiumIn, TTFTSeconds: &ttft, TPS: &tps},
		cheapModel:   {InputPer1KUSD: &cheapIn, TTFTSeconds: &ttft, TPS: &tps},
	}
	// The cheap lane elects every cluster, so no cluster's own budget can ever
	// admit the premium lane; only the global term is left to widen.
	quality := Rankings{
		0: {cheapModel: 0.9, premiumModel: 0.1},
		1: {cheapModel: 0.9, premiumModel: 0.1},
		2: {cheapModel: 0.9, premiumModel: 0.1},
	}
	knobs := &DefaultRoutingKnobs{
		Alpha:                []float64{0.9, 0.9, 0.9},
		SpeedWeight:          0.0,
		OutputCostRatio:      0.0,
		ExpectedOutputTokens: 2000,
		PerModelVerbosity:    false,
	}
	emb := &fakeEmbedder{vec: weightedVec(EmbedDim, clusters, 1.0, 0.0)}
	capKnee := DefaultCostCapMultiple
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		qualityMeans:    quality,
		modelAxes:       axes,
		defaultKnobs:    knobs,
		clusterCount:    clusters,
		topP:            &topP,
		costCapMode:     CostCapModeCluster,
		costCapMultiple: &capKnee,
	})
	base := router.Request{PromptText: "agentic continuation"}

	capped, err := scorer.Route(context.Background(), base)
	require.NoError(t, err)
	assert.Equal(t, cheapModel, capped.Model)
	require.NotNil(t, capped.Metadata)
	assert.ElementsMatch(t, []string{cheapModel}, capped.Metadata.CandidateModels,
		"cluster mode at the deployment knee must keep the premium lane out of the pool")

	relaxed := 6.0
	req := base
	req.CostCapMultiple = &relaxed
	opened, err := scorer.Route(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, cheapModel, opened.Model,
		"the relaxation widens the pool; it does not fabricate a winner the blend did not score higher")
	require.NotNil(t, opened.Metadata)
	assert.ElementsMatch(t, []string{premiumModel, cheapModel}, opened.Metadata.CandidateModels,
		"cluster mode must pass the request clamp through to the cap functions")
}
