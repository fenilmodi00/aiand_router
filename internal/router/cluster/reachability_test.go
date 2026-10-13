package cluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadReachabilityBundle loads the candidate bundle whose reachability the
// report is meant to expose. Reachability never embeds, so a fake embedder
// matching the bundle's declared id/dim keeps the test hermetic (same trick as
// loadDialBundle).
func loadReachabilityBundle(t *testing.T, capMultiple float64, capMode string) *Scorer {
	t.Helper()
	bundle, err := LoadBundle("v0.84")
	require.NoError(t, err)
	require.True(t, bundle.IsV2)
	cfg := DefaultConfig()
	cfg.CostCapMultiple = capMultiple
	cfg.CostCapMode = capMode
	s, err := NewScorer(bundle, cfg, &fakeEmbedder{dim: bundle.Centroids.Dim}, allProviders())
	require.NoError(t, err)
	return s
}

// TestReachability_InternallyConsistent asserts the report agrees with the
// scorer's own inputs: every deployed model is reported exactly once, the
// winner sets match the per-model cluster wins, the cost multiples are
// non-negative and sorted, and the alpha interval is inside [0,1]. Read-only
// diagnostic, so a wrong number here is the failure this guards against.
func TestReachability_InternallyConsistent(t *testing.T) {
	for _, tc := range []struct {
		capMultiple float64
		capMode     string
	}{
		{DefaultCostCapMultiple, CostCapModeGlobal},
		{DefaultCostCapMultiple, CostCapModeCluster},
		{0, CostCapModeGlobal},
	} {
		s := loadReachabilityBundle(t, tc.capMultiple, tc.capMode)
		report, err := s.Reachability()
		require.NoError(t, err, "v2 bundle must produce a reachability report (cap %v, mode %v)", tc.capMultiple, tc.capMode)

		assert.Equal(t, s.version, report.ClusterVersion)
		assert.Equal(t, s.centroids.K, report.ClusterCount)
		assert.Equal(t, tc.capMultiple, report.CostCapMultiple)
		assert.Equal(t, tc.capMode, report.CostCapMode)
		assert.Equal(t, qualityBiasCalibrationGrid, report.AlphaGridPoints)

		// One row per deployed model, no extras, no omissions.
		require.Len(t, report.Models, len(s.models))
		seen := make(map[string]int, len(s.models))
		for _, row := range report.Models {
			seen[row.Model]++
			assert.GreaterOrEqual(t, row.CostMultipleVsCheapest, 0.0,
				"%s: cost multiple must be non-negative", row.Model)
			assert.NotZero(t, row.EffectiveCostPer1KInputUSD,
				"%s: effective cost must be reported", row.Model)
			if row.WinsAlphaLow != nil {
				assert.NotNil(t, row.WinsAlphaHigh, "%s: alpha interval must be closed", row.Model)
				assert.GreaterOrEqual(t, *row.WinsAlphaLow, 0.0)
				assert.LessOrEqual(t, *row.WinsAlphaHigh, 1.0)
				assert.LessOrEqual(t, *row.WinsAlphaLow, *row.WinsAlphaHigh)
			}
			if tc.capMultiple <= 0 {
				// Cap disabled: the lane test collapses to the whole roster.
				assert.True(t, row.InsideCostCapLane, "%s: cap disabled leaves every lane eligible", row.Model)
			}
		}
		for _, m := range s.models {
			assert.Equal(t, 1, seen[m], "deployed model %s must be reported exactly once", m)
		}

		// The report's rows are sorted by cost multiple, so the multiples are
		// read off in order and the cheapest lane is 1.0x.
		for i := 1; i < len(report.Models); i++ {
			prev, cur := report.Models[i-1], report.Models[i]
			assert.LessOrEqual(t, prev.CostMultipleVsCheapest, cur.CostMultipleVsCheapest,
				"rows must be ordered cheapest-effective-cost first")
		}
		require.NotEmpty(t, report.Models)
		assert.InDelta(t, 1.0, report.Models[0].CostMultipleVsCheapest, 1e-9,
			"the cheapest lane is 1.0x itself")

		// A winner-set member must actually have cluster wins to its name.
		byModel := make(map[string]ModelReachability, len(report.Models))
		for _, row := range report.Models {
			byModel[row.Model] = row
		}
		for _, m := range report.WinnersWithCostCap {
			row, ok := byModel[m]
			require.True(t, ok, "capped winner %s must be a deployed model", m)
			assert.NotEmpty(t, row.ClustersWonWithCostCap,
				"capped winner %s must have at least one cluster win under the cap", m)
		}
		for _, m := range report.WinnersWithoutCostCap {
			row, ok := byModel[m]
			require.True(t, ok, "uncapped winner %s must be a deployed model", m)
			assert.NotEmpty(t, row.ClustersWonWithoutCostCap,
				"uncapped winner %s must have at least one cluster win with the cap off", m)
		}
		// Capped winners are a subset of uncapped ones: the cap only narrows.
		uncapped := make(map[string]struct{}, len(report.WinnersWithoutCostCap))
		for _, m := range report.WinnersWithoutCostCap {
			uncapped[m] = struct{}{}
		}
		for _, m := range report.WinnersWithCostCap {
			assert.Contains(t, uncapped, m, "a capped winner must also win with the cap off")
		}
	}
}

// TestReachability_MatchesV1Guard documents the v1 contract: a bundle with no
// quality_means to blend errors instead of reporting an empty roster.
func TestReachability_MatchesV1Guard(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	bundle := bundleFromBlobs(t, "v1-test", cb, rb, regb)
	s, err := NewScorer(bundle, cfgForTest(), &fakeEmbedder{}, allProviders())
	require.NoError(t, err)

	_, err = s.Reachability()
	require.ErrorIs(t, err, ErrClusterUnavailable)
	assert.Contains(t, err.Error(), "requires a v2 bundle")
}
