package cluster

import (
	"fmt"
	"path"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
	"github.com/stretchr/testify/require"
)

const winmapV089Version = "v0.89"

// TestV089WinMap walks all 16 clusters of the v0.89 bundle (loaded by explicit
// version, not the latest pointer) through blendScoresV2 + argmax and asserts
// the full expected winner map. v0.89 expresses the hybrid gating design in
// cells + knobs only: trivial lanes {2,4,9,10,11} -> glm-5.3-flash, hard
// lanes {0,7,8,12,13} -> glm-5.3, diversity lanes qwen (c6), motif (c14),
// v4-pro (c3,c5), v4.1-flash (c1,c15). kimi-k3 is roster-listed but wins no
// cluster (cost keeps it out of every lane, matching the hybrid reference).
func TestV089WinMap(t *testing.T) {
	version, err := ResolveVersion(winmapV089Version)
	require.NoError(t, err)
	require.Equal(t, winmapV089Version, version)
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

	expected := map[string][]int{
		string(catalog.ModelIDDeepSeekV4Pro):    {3, 5},
		string(catalog.ModelIDDeepSeekV41Flash): {1},
		string(catalog.ModelIDMotif3):           {14},
		string(catalog.ModelIDQwen3827B):        {6},
		string(catalog.ModelIDGLM53):            {0, 7, 8, 12, 13},
		string(catalog.ModelIDGLM53Flash):       {2, 4, 9, 10, 11, 15},
	}
	for m, want := range expected {
		require.Equalf(t, want, wins[m], "winner clusters for %s", m)
	}
	require.Equal(t, len(expected), len(wins), "distinct winner count: got "+fmt.Sprint(wins))
	require.Empty(t, wins[string(catalog.ModelIDKimiK3)], "kimi-k3 must not win any cluster in v0.89")
}

// TestV089FeaturesMatchQualityMeans is the psi_probe == quality_means parity
// gate for the v0.89 bundle.
func TestV089FeaturesMatchQualityMeans(t *testing.T) {
	version, err := ResolveVersion(winmapV089Version)
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
