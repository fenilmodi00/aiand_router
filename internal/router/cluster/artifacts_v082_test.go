package cluster

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

// v082Roster is the same 8-model AIand roster as v0.80/v0.79 (v0.82 is a
// quality-cell swap + knob retune — no roster or geometry change).
var v082Roster = v079Roster

// v082Alpha is the coding-first default_routing_knobs.alpha vector from
// ticket #85 (scripts/build_v082_coding.py): the per-cluster knapsack
// optimum over the mixed cells (sub_10 knowledge labels + C2 coding
// labels). Clusters 7, 10 and 12 carry coding-only shrunk-mean cells
// (>=20 usable coding prompts with variance); the other 13 keep their
// v0.80 knowledge cells. Replay on the merged labels: 68.54%
// (cluster-weighted) / 69.37% (per-prompt) at $0.56/1k vs v0.80's
// 68.35% / 69.13% at $0.96/1k on the same labels.
var v082Alpha = []float64{
	0.97, 0.17, 0.58, 0.18, 0.5, 0.5, 0.69, 0.56,
	0.78, 0.63, 0.11, 0.58, 0.18, 0.5, 0.67, 0.6,
}

// v082AlphaFloor is v0.80's floor clamped to never exceed the retuned
// default alpha. Clusters 1, 3, 6, 10 and 12 freeze at the default —
// the knapsack already put them on their chosen lane.
var v082AlphaFloor = []float64{
	0.3, 0.17, 0.55, 0.18, 0.3, 0.3, 0.19, 0.3,
	0.3, 0.55, 0.11, 0.55, 0.18, 0.3, 0.3, 0.55,
}

// v082CodingCells pins the bundle policy: only clusters with >=20
// usable coding prompts (z-scored, variance-bearing) take the coding
// cell; every other cluster's knowledge cell is unchanged.
var v082CodingCells = map[int]bool{7: true, 10: true, 12: true}

// TestV082BundleLoads pins the v0.82 coding-first bundle against the
// same boot gates and invariants as v0.80, plus the cell-provenance
// guard: coding clusters' quality means must differ from v0.80 while
// knowledge clusters' must be identical. Skips while artifacts/v0.82
// is absent and pins hard once it lands.
func TestV082BundleLoads(t *testing.T) {
	if _, err := fs.Stat(embeddedArtifacts, bundleDirForVersion("v0.82")); err != nil {
		t.Skipf("artifacts/v0.82 not built yet — run scripts/build_v082_coding.py once the coding bundle lands (%v)", err)
	}

	bundle, err := LoadBundle("v0.82")
	require.NoError(t, err, "v0.82 must parse end-to-end from the embedded tree")
	require.True(t, bundle.IsV2)
	assert.Equal(t, "v0.82", bundle.Metadata.Version)
	assert.Equal(t, "v0.80", bundle.Metadata.Parent)
	assert.Equal(t, 16, bundle.Centroids.K)
	require.Len(t, bundle.Registry.DeployedModels, 8, "registry carries exactly the 8")
	for _, e := range bundle.Registry.DeployedModels {
		assert.Equal(t, providers.ProviderAIAND, e.Provider)
		assert.Contains(t, v082Roster, e.Model)
		assert.False(t, e.Proxy)
		assert.Contains(t, e.BenchColumn, "routerarena_measured")
	}

	// Retuned knobs: the exact per-cluster alpha vector from the
	// mixed-cell knapsack, and the floor clamped to it.
	knobs := bundle.Metadata.Training.DefaultRoutingKnobs
	require.NotNil(t, knobs, "v0.82 must ship default_routing_knobs")
	require.Len(t, knobs.Alpha, 16)
	for i := range knobs.Alpha {
		assert.InDelta(t, v082Alpha[i], knobs.Alpha[i], 1e-9, "cluster %d alpha must pin the coding-retuned value", i)
	}
	require.Len(t, knobs.AlphaFloor, 16)
	for i := range knobs.AlphaFloor {
		assert.InDelta(t, v082AlphaFloor[i], knobs.AlphaFloor[i], 1e-9, "cluster %d alpha_floor must pin the clamped value", i)
		// a floor above the default alpha would invert the dial
		assert.LessOrEqual(t, knobs.AlphaFloor[i], knobs.Alpha[i]+1e-9,
			"cluster %d alpha_floor must not exceed the retuned default alpha", i)
	}

	v080, err := LoadBundle("v0.80")
	require.NoError(t, err)

	// AIand-only boot: exactly the 8, every candidate on aiand.
	aiand := map[string]struct{}{providers.ProviderAIAND: {}}
	s2, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{}, aiand)
	require.NoError(t, err, "AIand-only deploy must boot v0.82")
	require.Len(t, s2.models, 8)
	for _, c := range s2.candidates {
		assert.Equal(t, providers.ProviderAIAND, c.Provider)
		assert.Contains(t, v082Roster, c.Model)
	}

	// Full provider set: STILL exactly the 8 — the registry is the
	// roster, so vendor keys or BYOK headers cannot widen eligibility.
	all := make(map[string]struct{})
	for _, p := range providers.AllProviders() {
		all[p] = struct{}{}
	}
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{}, all)
	require.NoError(t, err)
	require.Len(t, s.models, 8, "full-provider boot must keep the registry's 8")
	for _, c := range s.candidates {
		assert.Equal(t, providers.ProviderAIAND, c.Provider)
	}

	// Frozen geometry: centroids byte-identical to v0.80 (the parent)
	// — v0.82 is a cell swap + knob retune, no geometry change.
	require.Equal(t, len(v080.Centroids.Data), len(bundle.Centroids.Data))
	for i := range v080.Centroids.Data {
		require.Equal(t, v080.Centroids.Data[i], bundle.Centroids.Data[i], "centroid %d drifted", i)
	}

	// Cell provenance: coding clusters' quality means differ from
	// v0.80; knowledge clusters' are byte-identical. This is the
	// binding bundle policy from parent #80 — if it drifts, the
	// build regressed.
	require.NotNil(t, bundle.QualityMeans)
	require.NotNil(t, v080.QualityMeans)
	for k := 0; k < 16; k++ {
		row, ok := bundle.QualityMeans[k]
		v080Row, ok080 := v080.QualityMeans[k]
		require.True(t, ok && ok080, "cluster %d must have quality means in both bundles", k)
		same := true
		for m, q := range row {
			if q080, exists := v080Row[m]; !exists || q080 != q {
				same = false
				break
			}
		}
		if v082CodingCells[k] {
			assert.False(t, same, "cluster %d must take the coding cell (differ from v0.80)", k)
		} else {
			assert.True(t, same, "cluster %d must keep the v0.80 knowledge cell byte-identical", k)
		}
	}

	// Dial monotonicity: QualityBias->Alpha stays non-decreasing over
	// the whole dial travel.
	prev := -1.0
	for g := 0; g <= 100; g++ {
		a := s.dialToAlpha(float64(g) / 100)
		assert.GreaterOrEqual(t, a, prev, "alpha must be non-decreasing in the dial (t=%v)", float64(g)/100)
		prev = a
	}
	assert.InDelta(t, 0.0, s.dialToAlpha(0), 1e-9, "t=0 must map to alpha 0")
	assert.InDelta(t, 1.0, s.dialToAlpha(1), 1e-9, "t=1 must map to alpha 1")
}
