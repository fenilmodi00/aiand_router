package cluster

// Sweep: how much of the deepseek-v4-pro slot should zai-org/glm-5.3-flash
// take?
//
// The proposal under test is the part of the model swap the data supports:
// v4-pro (held-out 0.289) gives way to glm-5.3-flash (held-out 0.235) on the
// slice of traffic v4-pro currently serves, while the kimi-k3 coding lane
// stays untouched.
//
// The lift is expressed as a fraction of each cluster's own cell spread,
// added to glm-5.3-flash's cell, so the blend elects it where it used to
// elect v4-pro. This sweeps the lift and reports the traffic, the price and
// the held-out accuracy for each point.
//
//	go test ./internal/router/cluster/ -run TestGlmFlashLiftSweep -v

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

// liftBundle returns a copy of the bundle with glm-5.3-flash's cell lifted
// toward (and past) deepseek-v4-pro's in every cluster, by `lift` fractions
// of the cluster's own cell spread.
func liftBundle(bundle *Bundle, lane, old string, lift float64) *Bundle {
	out := *bundle
	out.QualityMeans = make(Rankings, len(bundle.QualityMeans))
	for k, row := range bundle.QualityMeans {
		newRow := make(map[string]float32, len(row))
		for m, v := range row {
			newRow[m] = v
		}
		out.QualityMeans[k] = newRow
	}
	for _, row := range out.QualityMeans {
		lv, ok1 := row[lane]
		_, ok2 := row[old]
		if !ok1 || !ok2 {
			continue
		}
		lo, hi := lv, lv
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
		row[lane] = lv + float32(lift)*span
	}
	return &out
}

func TestGlmFlashLiftSweep(t *testing.T) {
	labels, order, corpusEmbedder := loadCorpus(t)
	held := loadHeldOutGrades(t)
	base, err := LoadBundle("v0.86")
	require.NoError(t, err)

	probes, probeEmbedder := loadRegisterProbes(t)
	_ = probes

	for _, lift := range []float64{0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.75, 1.0} {
		bundle := liftBundle(base, "zai-org/glm-5.3-flash", "deepseek-ai/deepseek-v4-pro", lift)
		res := replayCorpusWith(t, bundle, labels, order, corpusEmbedder, held)

		// register mix + chit-chat check
		cfg := DefaultConfig()
		cfg.CostCapMultiple = 0
		scorer, err := NewScorer(bundle, cfg, probeEmbedder, allProviders())
		require.NoError(t, err)
		byRegister := map[string]map[string]int{}
		distinct := map[string]struct{}{}
		for _, p := range loadProbeList(t) {
			dec, err := scorer.Route(context.Background(), router.Request{PromptText: p.Text})
			require.NoError(t, err)
			distinct[dec.Model] = struct{}{}
			if byRegister[p.Register] == nil {
				byRegister[p.Register] = map[string]int{}
			}
			byRegister[p.Register][dec.Model]++
		}
		chitChat := byRegister["conversational"]["zai-org/glm-5.3-flash"] +
			byRegister["trivial_nl"]["zai-org/glm-5.3-flash"]

		out, _ := json.Marshal(map[string]any{
			"lift":            lift,
			"corpus_accuracy": res.Accuracy,
			"cost_per_prompt": res.HonestCostPer,
			"heldout_coding":  res.HeldOutAccuracy,
			"heldout_n":       res.HeldOutScored,
			"models_reached":  res.ModelsReached,
			"glm_flash_share": res.Mix["zai-org/glm-5.3-flash"],
			"v4pro_share":     res.Mix["deepseek-ai/deepseek-v4-pro"],
			"kimi_share":      res.Mix["moonshotai/kimi-k3"],
			"chitchat_premium": chitChat,
			"register_distinct": len(distinct),
		})
		fmt.Printf("LIFT %s\n", out)
	}
}

// loadProbeList returns the register probes (the corpus and its embeddings).
func loadProbeList(t *testing.T) []registerProbe {
	t.Helper()
	probes, _ := loadRegisterProbes(t)
	return probes
}
