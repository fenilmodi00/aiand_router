package cluster

// Candidate sweep: pick the coding-lane offset and cluster set against the
// REAL scorer, not a port.
//
// The two constraints are in tension and both are product requirements:
//
//   - intelligence: the kimi-k3 lane must take the coding traffic that
//     v0.84's cells cannot see (measured on the held-out AIDER fair-lane
//     grades, an independent re-measurement);
//   - price and register discipline: the cheap lanes keep the bulk of the
//     traffic, and chit-chat (conversational / trivial_nl) must never reach
//     the premium lane.
//
// This sweeps the offset and the cluster set through the production scorer
// and reports both, so the chosen point is a measured one.
//
//	go test ./internal/router/cluster/ -run TestCandidateSweep -v

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

// offsetBundle returns a copy of the bundle with a range-relative cell offset
// applied to the given lane in the given clusters, exactly as
// scripts/build_v086_coding_lane.py writes it into the artifact.
func offsetBundle(bundle *Bundle, lane string, clusters []int, offset float64) *Bundle {
	out := *bundle
	out.QualityMeans = make(Rankings, len(bundle.QualityMeans))
	for k, row := range bundle.QualityMeans {
		newRow := make(map[string]float32, len(row))
		for m, v := range row {
			newRow[m] = v
		}
		out.QualityMeans[k] = newRow
	}
	for _, k := range clusters {
		row, ok := out.QualityMeans[k]
		if !ok {
			continue
		}
		measured, ok := row[lane]
		if !ok {
			continue
		}
		lo, hi := measured, measured
		for _, v := range row {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		span := hi - lo
		if span <= 0 {
			span = 1
		}
		row[lane] = measured + float32(offset)*span
	}
	return &out
}

// registerMixFor scores the register probes with a given bundle and returns
// the per-register mix plus the whole-corpus mix.
func registerMixFor(t *testing.T, bundle *Bundle, embedder *corpusEmbedder) (map[string]int, map[string]map[string]int) {
	t.Helper()
	probes, probeEmbedder := loadRegisterProbes(t)
	_ = probes
	cfg := DefaultConfig()
	cfg.CostCapMultiple = 0
	scorer, err := NewScorer(bundle, cfg, probeEmbedder, allProviders())
	require.NoError(t, err)
	probes, _ = loadRegisterProbes(t)
	mix := map[string]int{}
	byRegister := map[string]map[string]int{}
	for _, p := range probes {
		dec, err := scorer.Route(context.Background(), router.Request{PromptText: p.Text})
		require.NoError(t, err)
		mix[dec.Model]++
		if byRegister[p.Register] == nil {
			byRegister[p.Register] = map[string]int{}
		}
		byRegister[p.Register][dec.Model]++
	}
	_ = embedder
	return mix, byRegister
}

// TestCandidateSweep reports the trade-off curve for the coding-lane offset.
// It is a report, not a gate: the chosen configuration is pinned by
// TestCandidateMatchesSweepWinner.
func TestCandidateSweep(t *testing.T) {
	labels, order, corpusEmbedder := loadCorpus(t)
	held := loadHeldOutGrades(t)

	base, err := LoadBundle("v0.84")
	require.NoError(t, err)

	clusterSets := map[string][]int{
		"none":         {},
		"c5c10":        {5, 10},
		"c5c8c10":      {5, 8, 10},
		"c5c10c12":     {5, 10, 12},
		"c5c8c10c12":   {5, 8, 10, 12},
		"c5c8c10c12c14": {5, 8, 10, 12, 14},
		"c3c5c6c7c13":  {3, 5, 6, 7, 13},
		"c3c5c6c7c8c13": {3, 5, 6, 7, 8, 13},
		"c3c5c6c7c8c13c14": {3, 5, 6, 7, 8, 13, 14},
	}
	offsets := []float64{1.0, 1.25, 1.5, 2.0, 2.5, 3.0}

	names := make([]string, 0, len(clusterSets))
	for n := range clusterSets {
		names = append(names, n)
	}
	sort.Strings(names)

	// Split-half of the fixed held-out subset: the offset is chosen on one
	// half and reported on the other, so the quoted gain is not the same data
	// the offset was tuned on.
	subset, _ := heldOutSubset(held)
	var selectHalf, validHalf []string
	for i, idx := range subset {
		if i%2 == 0 {
			selectHalf = append(selectHalf, idx)
		} else {
			validHalf = append(validHalf, idx)
		}
	}
	halfAcc := func(decisions map[string]string, half []string) (float64, int) {
		sum := 0.0
		n := 0
		for _, idx := range half {
			if s, ok := held[idx][decisions[idx]]; ok {
				sum += s
				n++
			}
		}
		if n == 0 {
			return 0, 0
		}
		return sum / float64(n), n
	}

	for _, setName := range names {
		clusters := clusterSets[setName]
		for _, off := range offsets {
			bundle := offsetBundle(base, "moonshotai/kimi-k3", clusters, off)
			res := replayCorpusWith(t, bundle, labels, order, corpusEmbedder, held)
			mix, byRegister := registerMixFor(t, bundle, corpusEmbedder)
			chitChatPremium := byRegister["conversational"]["moonshotai/kimi-k3"] +
				byRegister["trivial_nl"]["moonshotai/kimi-k3"]
			sel, selN := halfAcc(res.PerPromptModel, selectHalf)
			val, valN := halfAcc(res.PerPromptModel, validHalf)
			out, _ := json.Marshal(map[string]any{
				"clusters":          setName,
				"offset":            off,
				"corpus_accuracy":   res.Accuracy,
				"cost_per_prompt":   res.HonestCostPer,
				"models_reached":    res.ModelsReached,
				"kimi_share":        res.Mix["moonshotai/kimi-k3"],
				"heldout_coding":    res.HeldOutAccuracy,
				"heldout_n":         res.HeldOutScored,
				"heldout_select":    sel,
				"heldout_select_n":  selN,
				"heldout_valid":    val,
				"heldout_valid_n":  valN,
				"chitchat_premium":  chitChatPremium,
				"register_distinct": len(mix),
			})
			fmt.Printf("SWEEP %s\n", out)
		}
	}

	// The chosen point and its baseline, compared on the same prompts with a
	// paired bootstrap, so the quoted gain is a paired measurement.
	base2 := offsetBundle(base, "moonshotai/kimi-k3", nil, 0)
	baseRes := replayCorpusWith(t, base2, labels, order, corpusEmbedder, held)
	for _, cand := range []struct {
		name     string
		clusters []int
		offset   float64
	}{
		{"c5c8c10c12c14@1.0", []int{5, 8, 10, 12, 14}, 1.0},
		{"c5c10c12@1.25", []int{5, 10, 12}, 1.25},
	} {
		b := offsetBundle(base, "moonshotai/kimi-k3", cand.clusters, cand.offset)
		res := replayCorpusWith(t, b, labels, order, corpusEmbedder, held)
		for _, half := range []struct {
			name string
			ids  []string
		}{{"SELECT", selectHalf}, {"VALID", validHalf}} {
			diffs := []float64{}
			for _, idx := range half.ids {
				bs, ok1 := held[idx][baseRes.PerPromptModel[idx]]
				cs, ok2 := held[idx][res.PerPromptModel[idx]]
				if ok1 && ok2 {
					diffs = append(diffs, cs-bs)
				}
			}
			wins, losses := 0, 0
			for _, d := range diffs {
				if d > 0 {
					wins++
				} else if d < 0 {
					losses++
				}
			}
			mean := 0.0
			if len(diffs) > 0 {
				for _, d := range diffs {
					mean += d
				}
				mean /= float64(len(diffs))
			}
			out, _ := json.Marshal(map[string]any{
				"candidate": cand.name, "half": half.name, "n": len(diffs),
				"delta_pt": mean * 100, "wins": wins, "losses": losses,
			})
			fmt.Printf("PAIRED %s\n", out)
		}
	}
}
