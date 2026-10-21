package cluster

// Sweep: the deployment cost cap on the served bundle.
//
// ROUTER_CLUSTER_COST_CAP_MULTIPLE is the deployment knob that decides how
// expensive a lane may be before it can win a turn. Lowering it is the
// cheapest available lever on price; this measures what it costs in
// intelligence before anyone turns it.
//
// The cap is applied by the scorer exactly as it is at runtime
// (applyCostCapCluster in cluster mode), so this is the production path, not
// a re-implementation.
//
//	go test ./internal/router/cluster/ -run TestCostCapSweep -v

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCostCapSweep(t *testing.T) {
	labels, order, embedder := loadCorpus(t)
	held := loadHeldOutGrades(t)
	bundle, err := LoadBundle("v0.86")
	require.NoError(t, err)

	for _, cap := range []float64{0, 0.3, 0.5, 0.8, 1.0, 1.5, 2.0, 3.0, 6.0, 10.0} {
		res := replayCorpusWithCap(t, bundle, labels, order, embedder, held, cap)
		out, _ := json.Marshal(map[string]any{
			"cap":             cap,
			"corpus_accuracy": res.Accuracy,
			"cost_per_prompt": res.HonestCostPer,
			"heldout_coding":  res.HeldOutAccuracy,
			"heldout_n":       res.HeldOutScored,
			"models_reached":  res.ModelsReached,
			"mix":             res.Mix,
		})
		fmt.Printf("CAP %s\n", out)
	}
}
