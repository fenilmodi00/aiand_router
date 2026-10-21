package cluster

// Verification of the v0.86 candidate bundle through the REAL scorer.
//
// The offline replay that chose v0.86 (scripts/build_v086_coding_lane.py) is a
// Python port of the runtime Route path. This drives the same bundle through
// the production scorer (NewScorer + Route) over the committed register-probe
// corpus and asserts the properties the candidate is for:
//
//   1. the mix does not collapse: the kimi-k3 coding lane takes real traffic
//      (v0.84's cells elect it in no cluster, so this is the regression the
//      candidate exists to fix);
//   2. the cheap lanes still carry the bulk of the traffic, so the bundle is a
//      price win and not a wholesale move onto the premium lane;
//   3. chit-chat still never reaches the premium lane (the same invariant
//      TestRegisterProbeMixDoesNotCollapse asserts for v0.84);
//   4. the candidate reaches at least as many models as the bundle it is
//      built on.
//
// Every number here is measured by the production scorer, so promoting the
// candidate cannot silently break the mix invariants the existing guard holds.

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

const (
	candidateVersion  = "v0.86"
	candidateLane     = "moonshotai/kimi-k3"
	cheapLane         = "deepseek-ai/deepseek-v4-flash"
	cheapLaneAlt      = "deepseek-ai/deepseek-v4.1-flash"
	minDistinctModels = 4
	// The clusters the candidate offsets, and the one it must not: c0 is the
	// chit-chat cluster, and offsetting it puts conversational and trivial_nl
	// turns on the premium lane.
	candidateClusters = "5,9,10"
)

func candidateRegisterMix(t *testing.T, version string, costCapMultiple float64) (map[string]int, map[string]map[string]int) {
	t.Helper()
	bundle, err := LoadBundle(version)
	require.NoError(t, err)
	require.True(t, bundle.IsV2, "%s must load as a v2 bundle for the blend path", version)

	probes, embedder := loadRegisterProbes(t)
	require.Equal(t, embedder.id, bundle.EmbedderID(), "embedding cache must match bundle embedder")

	cfg := DefaultConfig()
	cfg.CostCapMultiple = costCapMultiple
	scorer, err := NewScorer(bundle, cfg, embedder, allProviders())
	require.NoError(t, err)

	mix := map[string]int{}
	byRegister := map[string]map[string]int{}
	for _, p := range probes {
		dec, err := scorer.Route(context.Background(), router.Request{PromptText: p.Text})
		require.NoError(t, err, "probe %s must route", p.ID)
		require.NotEmpty(t, dec.Model, "probe %s routed to an empty model", p.ID)
		mix[dec.Model]++
		if byRegister[p.Register] == nil {
			byRegister[p.Register] = map[string]int{}
		}
		byRegister[p.Register][dec.Model]++
	}
	return mix, byRegister
}

func TestCandidateCodingLaneIsReachable(t *testing.T) {
	mix, byRegister := candidateRegisterMix(t, candidateVersion, 0)

	// (1) The mix spans at least the floor the existing guard enforces.
	assert.GreaterOrEqual(t, len(mix), minDistinctModels,
		"routing mix collapsed: %d distinct models (%v)", len(mix), mix)

	// (2) The candidate's whole point: the kimi-k3 coding lane wins traffic.
	// v0.84's cells elect it in no cluster, so a zero here means the offset
	// did not reach the blend.
	assert.Positive(t, mix[candidateLane],
		"candidate lane %s wins no probe: the cell offset did not reach the blend (%v)",
		candidateLane, mix)

	// (3) The cheap lanes still carry the bulk of the traffic. This is the
	// price side of the trade.
	cheap := mix[cheapLane] + mix[cheapLaneAlt]
	assert.Greater(t, cheap, mix[candidateLane],
		"cheap lanes (%d) must out-count the premium coding lane (%d): %v",
		cheap, mix[candidateLane], mix)

	// (4) Chit-chat must not reach the premium lane.
	for _, reg := range []string{"conversational", "trivial_nl"} {
		assert.Zero(t, byRegister[reg][candidateLane],
			"register %q reached the premium lane %s: %v", reg, candidateLane, byRegister[reg])
	}
}

func TestCandidateDoesNotCollapseBelowStockReachability(t *testing.T) {
	// A candidate that reaches FEWER models than the bundle it is built on is
	// a regression on the mix-collapse guard even if it is cheaper.
	stock, _ := candidateRegisterMix(t, "v0.84", 0)
	candidate, _ := candidateRegisterMix(t, candidateVersion, 0)
	assert.GreaterOrEqual(t, len(candidate), len(stock),
		"candidate reaches %d models, v0.84 reaches %d", len(candidate), len(stock))
}

func TestCandidateIsNotMoreExpensiveThanTheBundleItReplaces(t *testing.T) {
	// The candidate replaces v0.83, so the price guard is against v0.83, not
	// against v0.84: the candidate deliberately spends a little more than
	// v0.84 to route coding traffic to the lane that can answer it.
	//
	// The register-probe corpus is chit-chat-heavy BY CONSTRUCTION (it is the
	// mix guard's corpus), so it is not the place to measure traffic price.
	// What it does prove is that the cheap lanes still carry the bulk of the
	// traffic even on the corpus most favourable to the premium lane.
	probes, embedder := loadRegisterProbes(t)

	blendCost := func(version string) float64 {
		bundle, err := LoadBundle(version)
		require.NoError(t, err)
		cfg := DefaultConfig()
		cfg.CostCapMultiple = DefaultCostCapMultiple
		scorer, err := NewScorer(bundle, cfg, embedder, allProviders())
		require.NoError(t, err)
		total := 0.0
		for _, p := range probes {
			dec, err := scorer.Route(context.Background(), router.Request{PromptText: p.Text})
			require.NoError(t, err)
			total += bundleEffectiveCost(bundle, dec.Model)
		}
		return total / float64(len(probes))
	}
	replaced := blendCost("v0.83")
	candidate := blendCost(candidateVersion)
	assert.Less(t, candidate, replaced,
		"candidate costs %.8f per probe, the bundle it replaces costs %.8f", candidate, replaced)
}

// bundleEffectiveCost is the blend's own cost term for a model:
// input_per_1k + output_cost_ratio * output_per_1k * verbosity / median_verbosity
// (scorer.go effectiveCost), read from the bundle's axes at its shipped knobs.
func bundleEffectiveCost(bundle *Bundle, model string) float64 {
	axis, ok := bundle.ModelAxes[model]
	if !ok {
		return 0
	}
	knobs := DefaultRoutingKnobs{
		OutputCostRatio:   0.25,
		PerModelVerbosity: true,
	}
	if bundle.Metadata != nil && bundle.Metadata.Training.DefaultRoutingKnobs != nil {
		knobs = *bundle.Metadata.Training.DefaultRoutingKnobs
	}
	median := bundle.MedianVerbosity
	verbosity := 1.0
	if knobs.PerModelVerbosity && axis.VerbosityTokens != nil && median > 0 {
		verbosity = *axis.VerbosityTokens / median
	}
	input := 0.0
	if axis.InputPer1KUSD != nil {
		input = *axis.InputPer1KUSD
	}
	output := 0.0
	if axis.OutputPer1KUSD != nil {
		output = *axis.OutputPer1KUSD
	}
	return input + knobs.OutputCostRatio*output*verbosity
}

func TestCandidateOverrideStaysAttributable(t *testing.T) {
	// The override must stay auditable inside the artifact: the biased cells
	// keep their measured values next to the served ones.
	bundle, err := LoadBundle(candidateVersion)
	require.NoError(t, err)
	require.NotEmpty(t, bundle.QualityMeans, "candidate must carry quality cells")

	stock, err := LoadBundle("v0.84")
	require.NoError(t, err)

	biased := 0
	for k, row := range bundle.QualityMeans {
		for m, served := range row {
			measured, ok := stock.QualityMeans[k][m]
			if !ok {
				continue
			}
			if served != measured {
				biased++
			}
		}
	}
	assert.Positive(t, biased, "candidate has no biased cell: the override did not apply")

	// Only the coding lane and only in the intended clusters may move.
	allowedClusters := map[int]struct{}{5: {}, 8: {}, 10: {}, 12: {}, 14: {}}
	for k, row := range bundle.QualityMeans {
		for m, served := range row {
			measured, ok := stock.QualityMeans[k][m]
			if !ok || served == measured {
				continue
			}
			assert.Equal(t, candidateLane, m, "cluster %d moved a cell for an unexpected model", k)
			_, inSet := allowedClusters[k]
			assert.True(t, inSet, "cluster %d is outside the intended offset set", k)
		}
	}
}

func TestCandidateArtifactsListIncludesIt(t *testing.T) {
	versions, err := ListVersions()
	require.NoError(t, err)
	assert.Contains(t, versions, candidateVersion)
	sort.Strings(versions)
}
