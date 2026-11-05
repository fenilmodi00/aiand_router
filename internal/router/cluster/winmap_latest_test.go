package cluster

import (
	"context"
	"fmt"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
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
	// The v1.2 mix (roster refresh, routing bit-identical to v1.1): trivial
	// lanes -> glm-5.3-flash, hard lanes -> glm-5.3, diversity lanes keep
	// 1017-flash (c1, same-column swap), qwen (c6), motif (c14), v4-pro
	// (c3,c5). kimi-k3.1-pro is roster-listed-but-inert day-0 (kimi-k3
	// predecessor prior) and muse-spark stays roster-listed; neither wins a
	// cluster. winmap_v1_test.go pins the v1.0 map by explicit version.
	expect := map[string][]int{
		"deepseek/deepseek-v4-pro":          {3, 5},
		"deepseek/deepseek-v4.1-flash-1017": {1},
		"motif/motif-3":                     {14},
		"moonshotai/kimi-k3.1-pro":          {},
		"qwen/qwen3.8-27b":                  {6},
		"z-ai/glm-5.3":                      {0, 7, 8, 12, 13},
		"z-ai/glm-5.3-flash":                {2, 4, 9, 10, 11, 15},
	}
	for m, cls := range expect {
		require(fmt.Sprint(wins[m]) == fmt.Sprint(cls), m+" must win clusters "+fmt.Sprint(cls)+", got "+fmt.Sprint(wins[m]))
	}
	require(len(wins) == 6, "all 6 routing winners must win clusters, got "+fmt.Sprint(len(wins)))
}

// TestLatestBundle_MuseSparkSelectableButWinsNothing pins the muse-spark
// role on the served bundle (v1.1 added it, v1.2 keeps it): the kimi-k3
// proxy prior plus its $1.25/$4.25 pricing keep it out of every default
// argmax lane (kimi-k3.1-pro joins it as roster-listed-but-inert in v1.2),
// yet it stays a real routing target — with the incumbent winners excluded
// its cluster is served instead of falling through, and it appears in the
// served bundle's boot candidate set.
func TestLatestBundle_MuseSparkSelectableButWinsNothing(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(version)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewScorer(bundle, DefaultConfig(), &fakeEmbedder{vec: bundle.Centroids.Row(0)}, map[string]struct{}{providers.ProviderAIAND: {}})
	if err != nil {
		t.Fatal(err)
	}

	// Default lanes: muse-spark wins nothing (cost keeps it out of every lane).
	for c := range s.centroids.K {
		knobs := s.defaultActiveKnobs()
		scores := s.blendScoresV2([]int{c}, knobs, s.models, nil)
		best, _ := argmax(scores, s.models)
		if best == "meta/muse-spark" {
			t.Fatalf("muse-spark must not win any cluster on the served bundle; won cluster %d", c)
		}
	}

	// But it is selectable: exclude the six default winners and the scorer
	// routes the turn to muse-spark rather than erroring or pinning kimi-k3.
	excluded := map[string]struct{}{
		"z-ai/glm-5.3": {}, "z-ai/glm-5.3-flash": {}, "deepseek/deepseek-v4-pro": {},
		"deepseek/deepseek-v4.1-flash-1017": {}, "qwen/qwen3.8-27b": {},
		"motif/motif-3": {},
	}
	dec, err := s.Route(context.Background(), router.Request{
		PromptText:       "design a distributed consensus protocol with leader election",
		Exclusions:       router.RequestedExclusions(excluded),
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "meta/muse-spark" {
		t.Fatalf("with the incumbent winners excluded, muse-spark must be selectable; got %q", dec.Model)
	}
}
