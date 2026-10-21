package cluster

// Counterfactual: what the proposed model swap would score.
//
// The proposal is: on the current bundle, send the traffic that goes to
// deepseek-v4-pro to zai-org/glm-5.3-flash instead, and the traffic that goes
// to moonshotai/kimi-k3 to zai-org/glm-5.3 instead.
//
// This measures it as a pure counterfactual: the ROUTING is unchanged (the
// real scorer still decides), and only the SCORE of the swapped lanes changes
// - the held-out grade of the substitute model on the same prompt. No new
// bundle is needed to answer the question, and no prompt-set difference can
// bias it.
//
//	go test ./internal/router/cluster/ -run TestSwapCounterfactual -v

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSwapCounterfactual(t *testing.T) {
	labels, order, embedder := loadCorpus(t)
	held := loadHeldOutGrades(t)
	if len(held) == 0 {
		t.Skip("held-out grades not present (local-only fixture)")
	}
	subset, lanes := heldOutSubset(held)
	if len(subset) == 0 {
		t.Skip("no fixed held-out subset")
	}
	_ = lanes

	bundle, err := LoadBundle("v0.86")
	require.NoError(t, err)
	res := replayCorpusWith(t, bundle, labels, order, embedder, held)

	// The proposed substitutions.
	swaps := map[string]string{
		"moonshotai/kimi-k3":              "zai-org/glm-5.3",
		"deepseek-ai/deepseek-v4-pro":    "zai-org/glm-5.3-flash",
	}

	// Score the fixed subset three ways: as served, with each swap applied
	// alone, and with both applied.
	score := func(apply map[string]string) (float64, int, map[string]int) {
		sum := 0.0
		n := 0
		counts := map[string]int{}
		for _, idx := range subset {
			served := res.PerPromptModel[idx]
			if sub, ok := apply[served]; ok {
				served = sub
			}
			g, ok := held[idx][served]
			if !ok {
				continue
			}
			sum += g
			n++
			counts[served]++
		}
		if n == 0 {
			return 0, 0, counts
		}
		return sum / float64(n), n, counts
	}

	base, n, _ := score(nil)
	report := map[string]any{"n": n, "as_served": base}
	for from, to := range swaps {
		a, _, _ := score(map[string]string{from: to})
		report[from+" -> "+to] = a
	}
	both, _, _ := score(swaps)
	report["both swaps"] = both

	enc, _ := json.Marshal(report)
	fmt.Printf("SWAP_COUNTERFACTUAL %s\n", enc)

	// Which prompts each swapped lane actually serves, so the exposure of the
	// swap is visible and not just its average effect.
	exposure := map[string]int{}
	for _, idx := range subset {
		served := res.PerPromptModel[idx]
		if _, ok := swaps[served]; ok {
			exposure[served]++
		}
	}
	keys := make([]string, 0, len(exposure))
	for k := range exposure {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("SWAP_EXPOSURE %s served on %d of %d fixed-subset prompts\n", k, exposure[k], n)
	}
}
