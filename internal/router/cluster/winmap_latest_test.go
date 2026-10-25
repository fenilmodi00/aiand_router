package cluster

import (
	"fmt"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
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
	// The v0.89-hybrid mix: relabel lanes with kimi zero-evidence priors and
	// the motif-3 c1 boost; the trivial/difficulty selection gates sit on top
	// of this map (gates_test.go / gates_battery_test.go cover them).
	expect := map[string][]int{
		string(catalog.ModelIDKimiK3):           {5},
		string(catalog.ModelIDGLM53):            {3, 7},
		string(catalog.ModelIDGLM53Flash):       {2},
		string(catalog.ModelIDMotif3):           {1, 11},
		string(catalog.ModelIDQwen3827B):        {0, 4, 12, 14},
		string(catalog.ModelIDDeepSeekV4Pro):    {10, 13},
		string(catalog.ModelIDDeepSeekV41Flash): {6, 8, 9, 15},
	}
	for m, cls := range expect {
		require(fmt.Sprint(wins[m]) == fmt.Sprint(cls), m+" must win clusters "+fmt.Sprint(cls)+", got "+fmt.Sprint(wins[m]))
	}
	require(len(wins) == 7, "all 7 routing winners must win clusters, got "+fmt.Sprint(len(wins)))
}
