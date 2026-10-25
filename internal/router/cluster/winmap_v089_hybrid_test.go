package cluster

import (
	"fmt"
	"path"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
	"github.com/stretchr/testify/require"
)

const winmapV089HybridVersion = "v0.89-hybrid"

// TestV089HybridWinMap walks all 16 clusters of the v0.89-hybrid bundle
// (loaded by explicit version, not the latest pointer) through
// blendScoresV2 + argmax and asserts the full expected winner map.
// winmap_latest_test.go re-asserts this same map through the latest pointer,
// which promoted to v0.89-hybrid.
func TestV089HybridWinMap(t *testing.T) {
	version, err := ResolveVersion(winmapV089HybridVersion)
	require.NoError(t, err)
	require.Equal(t, winmapV089HybridVersion, version)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{dim: bundle.Centroids.Dim}, map[string]struct{}{providers.ProviderAIAND: {}})
	require.NoError(t, err)

	wins := map[string][]int{}
	for c := 0; c < s.centroids.K; c++ {
		knobs := s.defaultActiveKnobs()
		scores := s.blendScoresV2([]int{c}, knobs, s.models, nil)
		best, _ := argmax(scores, s.models)
		wins[best] = append(wins[best], c)
	}
	t.Logf("WINMAP: %+v", wins)
	for m, cls := range wins {
		t.Logf("  %-35s wins clusters %v", m, cls)
	}

	// Verified winmap: identical to v0.88-relabel's runtime map except the
	// intended c1 flip (v4.1-flash -> motif-3, from the +0.12 motif boost).
	// The kimi zero-cell priors (9 clusters) flip no lanes on their own —
	// they only un-poison per-cluster quality ties for future blends.
	// winmap_latest_test.go asserts the same map via the latest pointer.
	expected := map[string][]int{
		string(catalog.ModelIDKimiK3):           {5},
		string(catalog.ModelIDGLM53):            {3, 7},
		string(catalog.ModelIDGLM53Flash):       {2},
		string(catalog.ModelIDMotif3):           {1, 11},
		string(catalog.ModelIDQwen3827B):        {0, 4, 12, 14},
		string(catalog.ModelIDDeepSeekV4Pro):    {10, 13},
		string(catalog.ModelIDDeepSeekV41Flash): {6, 8, 9, 15},
	}
	for m, want := range expected {
		require.Equalf(t, want, wins[m], "winner clusters for %s", m)
	}
	require.Equal(t, len(expected), len(wins), "distinct winner count: got "+fmt.Sprint(wins))
}

// TestV089HybridFeaturesMatchQualityMeans is the psi_probe == quality_means
// parity gate for the v0.89-hybrid bundle (TestFeaturesMatchQualityMeans in
// model_features_test.go is hardcoded to v0.65; non-latest coverage lives here).
func TestV089HybridFeaturesMatchQualityMeans(t *testing.T) {
	version, err := ResolveVersion(winmapV089HybridVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)

	rawQM, err := embeddedArtifacts.ReadFile(path.Join(bundleDirForVersion(version), "quality_means.json"))
	require.NoError(t, err)
	qualityMeans, err := loadQualityMeans(rawQM)
	require.NoError(t, err)

	rawFeatures, err := embeddedArtifacts.ReadFile(path.Join(bundleDirForVersion(version), "model_features.json"))
	require.NoError(t, err)
	featureQualityMeans, featureAxes, err := loadModelFeatures(rawFeatures, bundle.Centroids.K)
	require.NoError(t, err)

	for _, m := range bundle.Registry.Models() {
		for k := 0; k < bundle.Centroids.K; k++ {
			want, ok := qualityMeans[k][m]
			require.Truef(t, ok, "quality_means.json missing %s cluster %d", m, k)
			got, ok := featureQualityMeans[k][m]
			require.Truef(t, ok, "model_features.json missing %s cluster %d", m, k)
			require.Equalf(t, want, got, "quality cell mismatch for %s cluster %d", m, k)
		}
		require.Equalf(t, bundle.ModelAxes[m], featureAxes[m], "operational axis mismatch for %s", m)
	}
}
