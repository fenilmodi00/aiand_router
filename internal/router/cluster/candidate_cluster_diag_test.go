package cluster

// Cluster diagnostics for the coding-lane candidate.
//
// The candidate offsets kimi-k3's cell in a set of clusters, so the set must
// contain the coding-heavy clusters and NOT the chit-chat ones: offsetting a
// cluster that chit-chat lands in routes conversational and trivial_nl turns
// onto the premium lane.
//
// This reports, per cluster, how the 180 register probes are distributed over
// the clusters their top-4 neighbourhood contains, split by register class.
// The candidate's cluster set is then chosen from this table rather than
// guessed.
//
//	go test ./internal/router/cluster/ -run TestClusterComposition -v

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClusterComposition(t *testing.T) {
	probes, embedder := loadRegisterProbes(t)
	bundle, err := LoadBundle("v0.84")
	require.NoError(t, err)

	// The same top-P selection Route uses, so the numbers below are the
	// clusters a probe's decision is actually blended over.
	topClusters := func(text string) []int {
		vec, e := embedder.Embed(context.Background(), TailTruncate(text, DefaultConfig().MaxPromptChars))
		require.NoError(t, e)
		return topPNearest(vec, bundle.Centroids, DefaultConfig().TopP)
	}

	byCluster := map[int]map[string]int{}
	for _, p := range probes {
		for _, k := range topClusters(p.Text) {
			if byCluster[k] == nil {
				byCluster[k] = map[string]int{}
			}
			byCluster[k][p.Register]++
			byCluster[k]["total"]++
		}
	}

	regs := []string{"conversational", "trivial_nl", "knowledge_qa", "easy_code", "hard_code", "agentic_tool"}
	out := map[string]any{}
	keys := make([]int, 0, len(byCluster))
	for k := range byCluster {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		row := map[string]any{}
		chitChat := 0
		for _, r := range regs {
			row[r] = byCluster[k][r]
			if r == "conversational" || r == "trivial_nl" {
				chitChat += byCluster[k][r]
			}
		}
		row["total"] = byCluster[k]["total"]
		row["chitchat"] = chitChat
		out[fmt.Sprintf("c%d", k)] = row
	}
	enc, _ := json.Marshal(out)
	fmt.Printf("CLUSTER_COMPOSITION %s\n", enc)

	// A cluster is chit-chat-safe when no chit-chat probe's neighbourhood
	// contains it.
	fmt.Println("CHIT_CHAT_SAFE_CLUSTERS:")
	for _, k := range keys {
		chitChat := 0
		for _, r := range []string{"conversational", "trivial_nl"} {
			chitChat += byCluster[k][r]
		}
		if chitChat == 0 {
			fmt.Printf("  c%d (probes in neighbourhood: %d)\n", k, byCluster[k]["total"])
		}
	}
}
