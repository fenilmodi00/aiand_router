package cluster

// Committed, synthetic replay harness: routes the authored register-probe
// corpus (testdata/register_probes.jsonl + register_probes.emb, regenerated
// with scripts/embed_register_probes.py) through the REAL Scorer.Route with
// the v0.89-hybrid candidate bundle and the production gate configuration,
// asserting the per-register tier outcomes the v0.89 hybrid design promises.
// No private traffic data is used — the corpus is authored (see the `note`
// field on each probe).

import (
	"context"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"github.com/stretchr/testify/require"
)

// batteryVersion is the candidate bundle the battery runs against. Pinned
// explicitly (not via latest) so promotion of latest cannot silently change
// what this test exercises.
const batteryVersion = "v0.89-hybrid"

// batteryTier maps a routable model to its price tier (input $/Mtok):
// CHEAP <0.5, MID <1, HIGH >=1. Returns "" for unknown models.
func batteryTier(model string) string {
	switch model {
	case string(catalog.ModelIDGLM53Flash), string(catalog.ModelIDDeepSeekV41Flash):
		return "CHEAP"
	case string(catalog.ModelIDQwen3827B), string(catalog.ModelIDMotif3):
		return "MID"
	case string(catalog.ModelIDGLM53), string(catalog.ModelIDKimiK3), string(catalog.ModelIDDeepSeekV4Pro):
		return "HIGH"
	}
	return ""
}

// TestRegisterTierBatteryV089Hybrid routes every corpus probe through the
// real Scorer with gates at production defaults and asserts:
//
//  1. every c0-primary hard-register probe (hard_code, agentic_tool) is
//     escalated to the difficulty gate model (glm-5.3) and carries
//     Gate=difficulty — the short-probe register path must fire on
//     instruction phrasing, not only on long prompts;
//  2. no c0-primary chat-register probe (trivial_nl, conversational) is
//     escalated: it keeps its blend argmax model with Gate="";
//  3. every trivial-cluster-primary probe lands on the trivial gate model;
//  4. every non-gated probe is invariant under gates on/off.
func TestRegisterTierBatteryV089Hybrid(t *testing.T) {
	f := loadGateFixture(t, batteryVersion)
	gated := f.scorer(t, gateTestConfig(true))
	disabled := f.scorer(t, gateTestConfig(false))

	c0HardSeen, chatC0Seen := 0, 0
	cfg := gateTestConfig(true)
	for _, p := range f.probes {
		vec := f.byText[TailTruncate(p.Text, gateMaxPromptChars)]
		require.NotNilf(t, vec, "probe %s has no cached embedding", p.ID)
		top := topPNearest(vec, gated.centroids, gated.cfg.TopP)
		inC0 := gateMatchesAnyCluster(cfg.DifficultyGate.PrimaryClusters, top)
		inTrivial := gateMatchesAnyCluster(cfg.TrivialGate.PrimaryClusters, top)
		hard := hardSignal(len(p.Text), TailTruncate(p.Text, gateMaxPromptChars),
			cfg.DifficultyGate.HardMinChars, cfg.DifficultyGate.HardCodeMinChars,
			cfg.DifficultyGate.HardInstructionMinChars)
		isHard := p.Register == "hard_code" || p.Register == "agentic_tool"
		isChat := p.Register == "trivial_nl" || p.Register == "conversational"

		dec, err := gated.Route(context.Background(), router.Request{PromptText: p.Text})
		require.NoErrorf(t, err, "probe %s", p.ID)

		switch {
		case inC0 && hard:
			if isHard {
				c0HardSeen++
			}
			require.Equalf(t, catalog.ModelIDGLM53.String(), dec.Model,
				"c0 hard-signal probe %s (%s) must escalate via the difficulty gate", p.ID, p.Register)
			require.Equalf(t, GateDifficulty, dec.Metadata.Gate, "probe %s gate metadata", p.ID)
		case inC0 && isChat && !inTrivial:
			chatC0Seen++
			plain, err := disabled.Route(context.Background(), router.Request{PromptText: p.Text})
			require.NoError(t, err)
			require.Equalf(t, plain.Model, dec.Model,
				"c0 chat probe %s must not escalate; got %q vs plain %q", p.ID, dec.Model, plain.Model)
			require.Equalf(t, "", dec.Metadata.Gate, "probe %s must carry no gate metadata", p.ID)
			require.NotEqualf(t, "HIGH", batteryTier(dec.Model),
				"c0 chat probe %s routed to premium model %q", p.ID, dec.Model)
		case inTrivial:
			require.Equalf(t, catalog.ModelIDGLM53Flash.String(), dec.Model,
				"trivial-lane probe %s routed %q", p.ID, dec.Model)
		}

		// Gates-off invariance for everything the gates did not decide.
		if dec.Metadata.Gate == "" {
			plain, err := disabled.Route(context.Background(), router.Request{PromptText: p.Text})
			require.NoError(t, err)
			require.Equalf(t, plain.Model, dec.Model,
				"non-gated probe %s changed under gates: %q vs plain %q", p.ID, dec.Model, plain.Model)
		}
	}
	// Fixture relevance guards: the battery must keep containing gated-lane
	// probes, or assertions above pass vacuously.
	require.GreaterOrEqual(t, c0HardSeen, 10, "corpus lost its c0 hard-register probes")
	require.GreaterOrEqual(t, chatC0Seen, 5, "corpus lost its c0 chat probes")
}

// TestShortRegisterProbesGateOnC0 pins the short-probe ends of the heuristic
// through the real Route path: a short instruction-register task escalates,
// while short trivial/casual text of similar shape does not. Probes use a
// vector-level stub keyed to centroid 0 (the fixture embedder's fallback), so
// no re-embedding is needed when new short probes are authored.
func TestShortRegisterProbesGateOnC0(t *testing.T) {
	f := loadGateFixture(t, batteryVersion)
	gated := f.scorer(t, gateTestConfig(true))
	disabled := f.scorer(t, gateTestConfig(false))

	fire := "implement exponential backoff with jitter for the webhook client"
	noFire := map[string]string{
		"thanks, that really helped a lot":                        "gratitude",
		"run the linter and commit the fixes":                     "imperative but under the length floor",
		"write a tweet announcing our new app launch":             "copywriting imperative",
		"Traceback (most recent call last), see the attached log": "code register under the code length floor",
	}

	dec, err := gated.Route(context.Background(), router.Request{PromptText: fire})
	require.NoError(t, err)
	require.Equal(t, catalog.ModelIDGLM53.String(), dec.Model, "short instruction-register probe must escalate")
	require.Equal(t, GateDifficulty, dec.Metadata.Gate)

	for text, why := range noFire {
		decGated, err := gated.Route(context.Background(), router.Request{PromptText: text})
		require.NoError(t, err)
		decPlain, err := disabled.Route(context.Background(), router.Request{PromptText: text})
		require.NoError(t, err)
		require.Equalf(t, decPlain.Model, decGated.Model,
			"%s (%s) must not change under gates", text, why)
		require.Emptyf(t, decGated.Metadata.Gate, "%s must carry no gate metadata", text)
	}
}
