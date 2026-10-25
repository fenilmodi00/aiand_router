package cluster

import (
	"fmt"
	"testing"

	"aiand-os/router/internal/providers"
)

// TestLatestWinMapLive reports which models win which clusters under the promoted bundle.
func TestLatestWinMapLive(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(version)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{dim: bundle.Centroids.Dim}, map[string]struct{}{providers.ProviderAIAND: {}})
	if err != nil {
		t.Fatal(err)
	}
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
	require := func(cond bool, msg string) {
		if !cond {
			t.Fatal(msg)
		}
	}
	// The owner-approved mix: every routing lane wins at least one cluster.
	require(len(wins["moonshotai/kimi-k3"]) == 5, "kimi-k3 must keep the coding lane 5,8,12,13,14")
	require(len(wins["z-ai/glm-5.3"]) == 1, "glm-5.3 must win cluster 3")
	require(len(wins["z-ai/glm-5.3-flash"]) == 1, "glm-5.3-flash must win cluster 2")
	require(len(wins["motif/motif-3"]) == 1, "motif-3 must win cluster 9")
	require(len(wins["qwen/qwen3.8-27b"]) == 1, "qwen3.8-27b must win cluster 0")
	require(len(wins["deepseek/deepseek-v4-pro"]) == 6, "deepseek-v4-pro must win clusters 4,6,7,10,11,15")
	require(len(wins["deepseek/deepseek-v4.1-flash"]) == 1, "deepseek-v4.1-flash must win cluster 1")
	require(len(wins) == 7, "all 7 routing winners must win clusters, got "+fmt.Sprint(len(wins)))
}
