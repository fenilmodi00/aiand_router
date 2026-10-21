package cluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCandidateK12Loads is the load-gate for the candidate-k12 bake-off bundle:
// the prod-turn re-cluster (K=12) + in-house harness-faithful quality candidate
// must parse end-to-end, construct a Scorer, and route the offline-screen mix.
// It asserts the properties the staging bake-off relies on (K=12, quality
// present for every cluster, the ai& roster routable) and that every cluster
// picks a model from the deployed roster.
func TestCandidateK12Loads(t *testing.T) {
	bundle, err := LoadBundle("candidate-k12")
	require.NoError(t, err, "candidate-k12 must parse end-to-end")
	require.True(t, bundle.IsV2, "candidate-k12 is a v2 bundle (quality_means present)")

	// K=12 geometry.
	require.NotNil(t, bundle.Centroids)
	assert.Equal(t, 12, bundle.Centroids.K, "candidate-k12 must be a K=12 re-cluster")
	assert.Equal(t, 768, bundle.Centroids.Dim, "jina-v2-base-code-int8 is 768-dim")
	assert.Equal(t, EmbedderJinaV2, bundle.EmbedderID())
	assert.Equal(t, 768, bundle.EmbedDim())

	// The roster is the ai& catalog: every deployed model is one of the
	// surviving catalog IDs, and every one of them carries operational axes.
	models := bundle.Registry.Models()
	require.NotEmpty(t, models, "candidate-k12 must deploy models")
	for _, m := range models {
		_, ok := bundle.ModelAxes[m]
		assert.Truef(t, ok, "%s must have operational axes", m)
	}

	// Quality present for every (cluster, model): the loader validates this, but
	// assert it explicitly so a regression is legible.
	for k := 0; k < bundle.Centroids.K; k++ {
		row, ok := bundle.QualityMeans[k]
		require.Truef(t, ok, "quality_means missing cluster %d", k)
		for _, m := range models {
			_, ok := row[m]
			require.Truef(t, ok, "quality_means cluster %d missing model %q", k, m)
		}
	}

	// The roster must carry the ai& models the router can dispatch.
	for _, m := range []string{"z-ai/glm-5.3", "z-ai/glm-5.2", "z-ai/glm-5.3-flash", "moonshotai/kimi-k3", "moonshotai/kimi-k2.7"} {
		assert.Contains(t, models, m, "%s must be a deployed model", m)
	}

	// ai& is the only upstream, so the deploy's provider set is a single key.
	providers := map[string]struct{}{
		"aiand": {},
	}

	// Build a real Scorer through the jina-v2 fake embedder (matches id/dim, so
	// NewScorer's embedder guard passes without ONNX).
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{dim: bundle.Centroids.Dim}, providers)
	require.NoError(t, err, "candidate-k12 must construct a Scorer")

	// Every tiered ai& model in the roster is routable.
	routable := RoutableModelSet(bundle.Registry, providers)
	for _, m := range []string{"z-ai/glm-5.2", "z-ai/glm-5.3", "z-ai/glm-5.3-flash", "moonshotai/kimi-k3"} {
		_, ok := routable[m]
		assert.Truef(t, ok, "%s must be routable", m)
	}
	// The untiered passthrough rows are never cluster targets.
	_, flashRoutable := routable["deepseek/deepseek-v4-flash"]
	assert.Falsef(t, flashRoutable, "an untiered model must not be a cluster target")

	// Per-cluster argmax through the live v2 blend (the authoritative runtime
	// path: blendScoresV2 over a single cluster with the bundle's default knobs =
	// alpha 0.7). Every cluster must pick a model from the deployed roster.
	knobs := s.defaultActiveKnobs()
	require.Len(t, knobs.Alpha, 12, "default_routing_knobs.alpha must be a length-12 vector")
	for i, a := range knobs.Alpha {
		assert.InDeltaf(t, 0.7, a, 1e-9, "cluster %d alpha must be the 0.7 sweet spot", i)
	}

	require.NotEmpty(t, s.models, "the scorer must have routable models")

	deployed := make(map[string]struct{}, len(models))
	for _, m := range models {
		deployed[m] = struct{}{}
	}
	for c := 0; c < bundle.Centroids.K; c++ {
		scores := s.blendScoresV2([]int{c}, knobs, s.models, nil)
		winner, _ := argmax(scores, s.models)
		require.NotEmptyf(t, winner, "cluster %d must have a non-empty argmax winner", c)
		_, inRoster := deployed[winner]
		assert.Truef(t, inRoster, "cluster %d winner %q must be a deployed model", c, winner)
	}
}
