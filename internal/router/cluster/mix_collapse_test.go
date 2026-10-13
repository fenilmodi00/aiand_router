package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

// register-probe corpus + committed embedding cache (same files
// cmd/routing-report drives, so no ONNX runtime is needed at test time).
const (
	registerProbeCorpusPath = "testdata/register_probes.jsonl"
	registerProbeEmbPath    = "testdata/register_probes.emb"
)

// registerProbe is one labelled probe from the corpus. Register is the
// traffic class (conversational / trivial_nl / knowledge_qa / easy_code /
// hard_code / agentic_tool) the routing mix is asserted per.
type registerProbe struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Register string `json:"register"`
	Note     string `json:"note"`
}

// registerProbeEmbHeader mirrors the JSON header written by
// scripts/embed_register_probes.py.
type registerProbeEmbHeader struct {
	EmbedderID   string `json:"embedder_id"`
	EmbedDim     int    `json:"embed_dim"`
	N            int    `json:"n"`
	CorpusSHA256 string `json:"corpus_sha256"`
	Comment      string `json:"comment"`
}

// staticProbeEmbedder satisfies Embedder from precomputed vectors keyed by
// the tail-truncated probe text — exactly what the Scorer passes to Embed.
// Same trick as cmd/routing-report: real blend, no ONNX, deterministic.
type staticProbeEmbedder struct {
	id     string
	dim    int
	byText map[string][]float32
}

func (e *staticProbeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v, ok := e.byText[text]
	if !ok {
		return nil, fmt.Errorf("no precomputed embedding for probe text %q", text)
	}
	return v, nil
}

func (e *staticProbeEmbedder) ID() string { return e.id }
func (e *staticProbeEmbedder) Dim() int   { return e.dim }

// loadRegisterProbes reads the committed probe corpus and its precomputed
// embeddings, and fails loudly (rather than silently testing nothing) if the
// cache is stale relative to the corpus — the same guard cmd/routing-report's
// own test enforces.
func loadRegisterProbes(t *testing.T) ([]registerProbe, *staticProbeEmbedder) {
	t.Helper()
	raw, err := os.ReadFile(registerProbeCorpusPath)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)

	var probes []registerProbe
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p registerProbe
		require.NoError(t, json.Unmarshal([]byte(line), &p), "corpus line: %s", line)
		probes = append(probes, p)
	}
	require.NotEmpty(t, probes, "register-probe corpus is empty")

	embRaw, err := os.ReadFile(registerProbeEmbPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(embRaw), 8, "emb file too short")
	require.Equal(t, "PEM1", string(embRaw[:4]), "bad magic in %s", registerProbeEmbPath)
	hlen := int(binary.LittleEndian.Uint32(embRaw[4:8]))
	require.LessOrEqual(t, 8+hlen, len(embRaw), "truncated emb header")
	var hdr registerProbeEmbHeader
	require.NoError(t, json.Unmarshal(embRaw[8:8+hlen], &hdr), "parse emb header")

	if hdr.CorpusSHA256 != fmt.Sprintf("%x", sum) {
		t.Fatalf("STALE embedding cache: %s was built for corpus sha %s but the corpus is now %x.\n"+
			"Run: python scripts/embed_register_probes.py", registerProbeEmbPath, hdr.CorpusSHA256[:12], sum)
	}
	require.Equal(t, len(probes), hdr.N, "emb row count must match corpus probe count")
	need := 8 + hlen + hdr.N*hdr.EmbedDim*4
	require.Equal(t, need, len(embRaw), "emb body size mismatch: header implies %d bytes, file has %d", need, len(embRaw))

	byText := make(map[string][]float32, len(probes))
	p := 8 + hlen
	for i := range probes {
		v := make([]float32, hdr.EmbedDim)
		for d := range hdr.EmbedDim {
			v[d] = math.Float32frombits(binary.LittleEndian.Uint32(embRaw[p : p+4]))
			p += 4
		}
		byText[TailTruncate(probes[i].Text, DefaultConfig().MaxPromptChars)] = v
	}
	return probes, &staticProbeEmbedder{id: hdr.EmbedderID, dim: hdr.EmbedDim, byText: byText}
}

// routeAllRegisterProbes drives every probe through the REAL production
// scorer: production constructor (NewScorer), the committed bundle, the real
// blend (blendScoresV2 via Route), and the real top-P geometry selection.
// costCapMultiple mirrors the deployment's cfg.CostCapMultiple; pass
// DefaultCostCapMultiple to measure production exactly, or 0 to disable the
// cap and measure the blend's own reachability over the full roster.
func routeAllRegisterProbes(t *testing.T, bundle *Bundle, embed Embedder, probes []registerProbe, costCapMultiple float64) map[string]map[string]int {
	t.Helper()
	cfg := DefaultConfig() // TopP=4, MaxPromptChars=1024 — production defaults
	cfg.CostCapMultiple = costCapMultiple
	scorer, err := NewScorer(bundle, cfg, embed, allProviders())
	require.NoError(t, err)

	byRegister := make(map[string]map[string]int, 6)
	for _, p := range probes {
		dec, err := scorer.Route(context.Background(), router.Request{PromptText: p.Text})
		require.NoError(t, err, "probe %s must route", p.ID)
		require.NotEmpty(t, dec.Model, "probe %s routed to an empty model", p.ID)
		if byRegister[p.Register] == nil {
			byRegister[p.Register] = make(map[string]int)
		}
		byRegister[p.Register][dec.Model]++
	}
	return byRegister
}

// premiumInputTiers returns the deployed roster's input-price tiers, cheapest
// first, one slice of model names per distinct price level. The chit-chat
// gate's intent — a conversational turn has no business reaching the premium
// lane — is a price-tier statement, and the roster's own cost data is the only
// source for it.
//
// Why not cmd/routing-report's premiumModels/opusModels sets
// (cmd/routing-report/main.go:45-56)? Those name claude-opus-5/4-8/4-7,
// gpt-5.5, gemini-3.1-pro-preview, claude-sonnet-4-6 — the pre-prune
// Anthropic/OpenAI/Google roster. The current AIand-only 8-model roster
// (model_registry.json) deploys none of them, so filtering on those names
// would make the assertion trivially true regardless of routing and unable to
// catch a mix collapse onto kimi-k3. Price tier is roster-composition-proof:
// it re-derives whenever the roster or the price book changes.
//
// Tiers are bottom-up: 1x = the cheapest lane (free), and each subsequent
// rung is the next distinct price level. The guard's thresholds below are
// stated in these rungs, so the assertion survives re-pricing without being
// renumbered.
func premiumInputTiers(t *testing.T, bundle *Bundle) [][]string {
	t.Helper()
	roster := bundle.Registry.Models()
	prices := make(map[string]float64, len(roster))
	for _, m := range roster {
		axes, ok := bundle.ModelAxes[m]
		require.True(t, ok, "model %s missing from model_axes", m)
		require.NotNil(t, axes.InputPer1KUSD, "model %s has no input price", m)
		prices[m] = *axes.InputPer1KUSD
	}
	var levels []float64
	seen := make(map[float64]bool)
	for _, p := range prices {
		if !seen[p] {
			seen[p] = true
			levels = append(levels, p)
		}
	}
	sort.Float64s(levels)
	tiers := make([][]string, 0, len(levels))
	for _, lv := range levels {
		var tier []string
		for _, m := range roster {
			if prices[m] == lv {
				tier = append(tier, m)
			}
		}
		tiers = append(tiers, tier)
	}
	return tiers
}

func tierShare(byRegister map[string]map[string]int, register string, tier []string) (hits, total int) {
	for m, n := range byRegister[register] {
		total += n
		for _, pm := range tier {
			if m == pm {
				hits += n
				break
			}
		}
	}
	return hits, total
}

// TestRegisterProbeMixDoesNotCollapse is the regression guard against
// routing-mix collapse (ticket #94): guard the artefact's shipped knobs/cells
// from silently concentrating all traffic onto one model, and from chit-chat
// registers (conversational / trivial_nl) reaching the premium price tier.
//
// Drives all 180 labelled probes through the real Scorer — no stubbed blend,
// no fixture-generated corpus — so it fails if knobs, cells, or calibration
// change the mix.
func TestRegisterProbeMixDoesNotCollapse(t *testing.T) {
	probes, embedder := loadRegisterProbes(t)

	// The candidate artifact this ticket ships (v0.84, AA priors applied).
	// Pinned to v0.84 rather than following artifacts/latest so the guard
	// gates the artifact actually under review: it must pass before this
	// candidate is promoted, and it keeps guarding it after promotion.
	bundle, err := LoadBundle("v0.84")
	require.NoError(t, err)
	require.True(t, bundle.IsV2, "v0.84 must load as a v2 bundle for the blend path")
	require.Equal(t, embedder.id, bundle.EmbedderID(), "embedding cache must match bundle embedder")

	roster := bundle.Registry.Models()
	// Uncapped (CostCapMultiple=0) on purpose: this guard measures the BLEND's
	// reachability over the roster — which models the shipped knobs/cells can
	// win — because that is what a calibration retune collapses. The 3x cost
	// cap is a separate policy layer applied after the blend (scorer.go
	// applyCostCap) and is itself covered by the knob tests; keeping it out
	// here means the guard does not silently inherit "the cap already narrowed
	// the pool" from a policy change that is not the collapse under test.
	byRegister := routeAllRegisterProbes(t, bundle, embedder, probes, 0)

	// Mix over the whole corpus.
	totalMix := make(map[string]int)
	for _, reg := range []string{"conversational", "trivial_nl", "knowledge_qa", "easy_code", "hard_code", "agentic_tool"} {
		for m, n := range byRegister[reg] {
			totalMix[m] += n
		}
	}

	var zeroWin []string
	for _, m := range roster {
		if totalMix[m] == 0 {
			zeroWin = append(zeroWin, m)
		}
	}
	sort.Strings(zeroWin)

	// (a) Mix must span at least 4 distinct deployed models.
	//
	// Floor is 4 = the MEASURED post-prior reachability of this artifact over
	// this corpus: with the shipped knobs (alpha 0.96, c2/c3/c4/c6 demoted to
	// 0.80, output_cost_ratio 0.25, speed_weight 0) the 180 register probes
	// route onto 4 of the 8 deployed models — deepseek-v4.1-flash 79 (43.9%),
	// deepseek-v4-flash 64 (35.6%), qwen/qwen3.8-27b 36 (20.0%),
	// deepseek-v4-pro 1 (0.6%) — while glm-5.3, glm-5.3-flash, kimi-k3 and
	// motif-3 win nothing. The collapse this guard exists for is that 2-of-8
	// shape with most of the roster unreachable: a floor of 4 fails on it,
	// while the tie-band measurement (scripts/measure_tie_band.py) shows
	// reachability rising to 5 at band 0.10 as quality ties are explored, so
	// the floor stays below what healthy exploration reaches. It is set at
	// the artifact's own reachability, not an aspiration, so a legitimate
	// retune passes only while it holds the roster open.
	distinct := len(roster) - len(zeroWin)
	const minDistinctModels = 4
	if distinct < minDistinctModels {
		t.Errorf("routing mix collapsed: only %d of %d deployed models win any of the %d register probes (floor is %d) — zero-win models: %s\nfull mix: %s",
			distinct, len(roster), len(probes), minDistinctModels, strings.Join(zeroWin, ", "), formatMix(totalMix))
	}

	// (b) Conversational and trivial_nl must not reach the premium input
	// tiers. tiers[0] is the cheapest lane; the top rung is the last. On this
	// roster that is kimi-k3 ($0.003/1k), with deepseek-v4-pro and glm-5.3
	// sharing the next rung down ($0.001/1k). Both rungs are asserted, so the
	// gate holds strictly and not only at the very top: the measured mixes
	// (conversational 15x v4.1-flash + 12x qwen3.8-27b + 3x v4-flash, of 30;
	// trivial_nl 25x + 4x + 1x, of 30) sit entirely on the $0.00015–$0.0004
	// flash lanes and clear both rungs with 0%.
	//
	// The gate this test asserts is the one the register corpus exists for:
	// chit-chat must not reach the premium lane — cmd/routing-report's
	// per-register premium share, and .context/label-runs/
	// measure_register_probes.py, which reports the register mix and its
	// flash-lane share gate. Asserting the tier share itself, rather than a
	// remembered constant, is what makes a knob/cell/prior change that leaks
	// kimi-k3 onto chit-chat fail the build.
	tiers := premiumInputTiers(t, bundle)
	require.NotEmpty(t, tiers, "roster must have at least one price tier")
	for _, tier := range tiers[len(tiers)-2:] { // top two rungs
		for _, reg := range []string{"conversational", "trivial_nl"} {
			hits, total := tierShare(byRegister, reg, tier)
			require.Equal(t, 30, total, "register %q must have 30 probes in the corpus", reg)
			if hits != 0 {
				frac := 100 * float64(hits) / float64(total)
				t.Errorf("register %q routed %d of %d chit-chat turns (%0.1f%%) to premium input tier %s — chit-chat must not reach the premium lane\n%s mix: %s",
					reg, hits, total, frac, strings.Join(tier, ", "), reg, formatMix(byRegister[reg]))
			}
		}
	}
}

// TestRegisterProbeMixReportsBaseline is the measurement companion to
// TestRegisterProbeMixDoesNotCollapse: it names the deployed roster, the
// actual probe mix and the top-tier share per register so the guard's
// thresholds stay grounded in the current artifact's numbers rather than a
// remembered constant. Logged, never asserted.
func TestRegisterProbeMixReportsBaseline(t *testing.T) {
	probes, embedder := loadRegisterProbes(t)
	bundle, err := LoadBundle("v0.84")
	require.NoError(t, err)

	tiers := premiumInputTiers(t, bundle)
	topTier := tiers[len(tiers)-1]
	// Uncapped (cap 0) on purpose: the report shows the blend's reachability
	// across the whole roster, which is what the (a) floor is judged against.
	byRegister := routeAllRegisterProbes(t, bundle, embedder, probes, 0)

	totalMix := make(map[string]int)
	for _, reg := range sortedRegisters(byRegister) {
		for m, n := range byRegister[reg] {
			totalMix[m] += n
		}
	}
	t.Logf("bundle %s: %d deployed models across %d price tiers, top input tier %s @ probe mix over %d probes: %s",
		bundle.Version, len(bundle.Registry.Models()), len(tiers), strings.Join(topTier, ","), len(probes), formatMix(totalMix))
	for i, tier := range tiers {
		var parts []string
		for _, reg := range sortedRegisters(byRegister) {
			hits, total := tierShare(byRegister, reg, tier)
			parts = append(parts, fmt.Sprintf("%s %d/%d", reg, hits, total))
		}
		t.Logf("  tier %d %-70s %s", i+1, strings.Join(tier, ","), strings.Join(parts, "  "))
	}
}

func sortedRegisters(byRegister map[string]map[string]int) []string {
	out := make([]string, 0, len(byRegister))
	for r := range byRegister {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func formatMix(mix map[string]int) string {
	type kv struct {
		m string
		n int
	}
	var pairs []kv
	total := 0
	for m, n := range mix {
		pairs = append(pairs, kv{m, n})
		total += n
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].m < pairs[j].m
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s %d (%0.1f%%)", p.m, p.n, 100*float64(p.n)/float64(total)))
	}
	return strings.Join(parts, ", ")
}
