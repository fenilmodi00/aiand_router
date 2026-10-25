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
	// The v0.89 mix (cells-only hybrid design): trivial lanes -> glm-5.3-flash,
	// hard lanes -> glm-5.3, diversity lanes keep qwen/motif/v4-pro/v4.1-flash.
	// kimi-k3 is roster-listed but wins no cluster. winmap_v089_test.go pins
	// the same map by explicit version.
	expect := map[string][]int{
		"deepseek/deepseek-v4-pro":     {3, 5},
		"deepseek/deepseek-v4.1-flash": {1},
		"motif/motif-3":                {14},
		"qwen/qwen3.8-27b":             {6},
		"z-ai/glm-5.3":                 {0, 7, 8, 12, 13},
		"z-ai/glm-5.3-flash":           {2, 4, 9, 10, 11, 15},
	}
	for m, cls := range expect {
		require(fmt.Sprint(wins[m]) == fmt.Sprint(cls), m+" must win clusters "+fmt.Sprint(cls)+", got "+fmt.Sprint(wins[m]))
	}
	require(len(wins) == 6, "all 6 routing winners must win clusters, got "+fmt.Sprint(len(wins)))
}
