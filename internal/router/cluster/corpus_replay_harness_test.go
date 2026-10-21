package cluster

// Corpus replay harness: drives the labelled corpus through the REAL scorer.
//
// The offline replays that compare cluster bundles (scripts/bakeoff_v084.py
// and the throwaway comparison scripts) are Python ports of the runtime
// Route path. A port is a liability: it silently diverges from the Go
// implementation, and this file exists so bundle comparisons can be made
// against the production code path instead.
//
// The corpus and its committed embedding cache live under .context/ (not
// committed), so the harness is a TEST that skips when they are absent. It
// reads:
//
//	.context/label-runs/v082-expanded-labels.jsonl  - the label rows
//	.context/label-runs/corpus-embeds-v084.bin        - the embeddings (PEM1)
//
// and reports, per bundle version: accuracy over the prompts that bundle's
// chosen model has a label for, the honest cost over EVERY routed prompt, and
// the served-model mix.
//
// Run it with:
//
//	go test ./internal/router/cluster/ -run TestCorpusReplay -v
//
// It prints a JSON line per version so a comparison can be diffed.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
)

const (
	corpusLabelsPath = "../../../.context/label-runs/v082-expanded-labels.jsonl"
	corpusEmbedsPath = "../../../.context/label-runs/corpus-embeds-v084.bin"
)

// corpusLabel is one graded cell: a prompt x model with its measured score
// and cost.
type corpusLabel struct {
	GlobalIndex string   `json:"global_index"`
	Model       string   `json:"model"`
	Score       *float64 `json:"score"`
	Cost        *float64 `json:"cost"`
	Dataset     string   `json:"dataset"`
	Question    string   `json:"question"`
}

// corpusEmbedHeader mirrors the JSON header written by
// scripts/bakeoff_v084.py.
type corpusEmbedHeader struct {
	EmbedderID string `json:"embedder_id"`
	EmbedDim   int    `json:"embed_dim"`
	N          int    `json:"n"`
}

// corpusEmbedder serves precomputed corpus vectors by prompt text, the same
// trick as staticProbeEmbedder: the real blend runs, no ONNX at test time.
type corpusEmbedder struct {
	id     string
	dim    int
	byText map[string][]float32
}

func (e *corpusEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v, ok := e.byText[text]
	if !ok {
		return nil, fmt.Errorf("no precomputed embedding for corpus text")
	}
	return v, nil
}

func (e *corpusEmbedder) ID() string { return e.id }
func (e *corpusEmbedder) Dim() int   { return e.dim }

// loadCorpus reads the label rows and the embedding cache. The corpus rows
// are ordered by global_index in the cache, so the prompt order is the sorted
// set of global_index values.
func loadCorpus(t *testing.T) ([]corpusLabel, []string, *corpusEmbedder) {
	t.Helper()
	if _, err := os.Stat(corpusLabelsPath); err != nil {
		t.Skipf("label corpus not present at %s (local-only fixture)", corpusLabelsPath)
	}
	if _, err := os.Stat(corpusEmbedsPath); err != nil {
		t.Skipf("corpus embedding cache not present at %s (local-only fixture)", corpusEmbedsPath)
	}

	fh, err := os.Open(corpusLabelsPath)
	require.NoError(t, err)
	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	var labels []corpusLabel
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var l corpusLabel
		require.NoError(t, json.Unmarshal(line, &l), "parse label row")
		labels = append(labels, l)
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, labels)

	raw, err := os.ReadFile(corpusEmbedsPath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(raw), 8, "emb file too short")
	require.Equal(t, "PEM1", string(raw[:4]), "bad magic in %s", corpusEmbedsPath)
	hlen := int(binary.LittleEndian.Uint32(raw[4:8]))
	require.LessOrEqual(t, 8+hlen, len(raw), "truncated emb header")
	var hdr corpusEmbedHeader
	require.NoError(t, json.Unmarshal(raw[8:8+hlen], &hdr))

	// The prompt order the cache was written in: sorted global_index.
	indexes := map[string]struct{}{}
	for _, l := range labels {
		indexes[l.GlobalIndex] = struct{}{}
	}
	order := make([]string, 0, len(indexes))
	for idx := range indexes {
		order = append(order, idx)
	}
	sort.Strings(order)
	require.Len(t, order, hdr.N, "embedding cache rows must match the corpus prompt count")

	// The embedding row for a prompt is its index in the sorted order; the
	// scorer embeds the TAIL-TRUNCATED prompt text, so the embedder is keyed
	// by the same truncated text the scorer will pass.
	textByIndex := map[string]string{}
	for _, l := range labels {
		if _, ok := textByIndex[l.GlobalIndex]; !ok {
			textByIndex[l.GlobalIndex] = l.Question
		}
	}

	vecs := make([][]float32, hdr.N)
	base := 8 + hlen
	for i := 0; i < hdr.N; i++ {
		v := make([]float32, hdr.EmbedDim)
		for j := 0; j < hdr.EmbedDim; j++ {
			off := base + (i*hdr.EmbedDim+j)*4
			v[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
		}
		vecs[i] = v
	}

	embedder := &corpusEmbedder{id: hdr.EmbedderID, dim: hdr.EmbedDim, byText: map[string][]float32{}}
	for i, idx := range order {
		embedder.byText[TailTruncate(textByIndex[idx], DefaultConfig().MaxPromptChars)] = vecs[i]
	}
	return labels, order, embedder
}

// corpusReplayResult is one bundle's replay outcome.
type corpusReplayResult struct {
	Version         string             `json:"version"`
	Routed          int                `json:"routed"`
	Scored          int                `json:"scored"`
	Accuracy        float64            `json:"accuracy"`
	HonestCostTotal float64            `json:"honest_cost_total_usd"`
	HonestCostPer   float64            `json:"honest_cost_per_prompt_usd"`
	ModelsReached   int                `json:"models_reached"`
	Mix             map[string]int     `json:"mix"`
	// Held-out: the AIDER fair-lane grades, an independent re-measurement of
	// the same prompts taken AFTER the label corpus the cells were fitted to.
	// Scored only on the prompts where every graded lane has a grade, so two
	// bundles are always compared on the SAME prompt set.
	HeldOutAccuracy float64 `json:"held_out_accuracy"`
	HeldOutScored   int     `json:"held_out_scored"`
	PerPromptModel  map[string]string `json:"-"`
	PerPromptScore  map[string]float64 `json:"-"`
}

// loadHeldOutGrades reads the independent AIDER fair-lane re-measurement.
// These grades were produced after the label corpus the v0.84 cells were
// fitted to, so they are held out with respect to those cells.
//
//	.context/label-runs/aider-fair-lanes-grades.jsonl  - 5 lanes
//	.context/label-runs/aider-glm-effort-graded.jsonl  - 2 GLM lanes
func loadHeldOutGrades(t *testing.T) map[string]map[string]float64 {
	t.Helper()
	paths := []string{
		"../../../.context/label-runs/aider-fair-lanes-grades.jsonl",
		"../../../.context/label-runs/aider-glm-effort-graded.jsonl",
	}
	out := map[string]map[string]float64{}
	found := false
	for _, p := range paths {
		fh, err := os.Open(p)
		if err != nil {
			continue
		}
		found = true
		scanner := bufio.NewScanner(fh)
		scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var row struct {
				GlobalIndex string   `json:"global_index"`
				Model       string   `json:"model"`
				Score       *float64 `json:"score"`
			}
			require.NoError(t, json.Unmarshal(line, &row), "parse held-out grade")
			if row.Score == nil {
				continue
			}
			if out[row.GlobalIndex] == nil {
				out[row.GlobalIndex] = map[string]float64{}
			}
			out[row.GlobalIndex][row.Model] = *row.Score
		}
		require.NoError(t, scanner.Err())
		fh.Close()
	}
	if !found {
		return nil
	}
	return out
}

// heldOutSubset returns the AIDER prompts where EVERY graded lane has a
// grade, so every bundle is compared on the same prompts.
func heldOutSubset(held map[string]map[string]float64) (subset []string, lanes map[string]struct{}) {
	if len(held) == 0 {
		return nil, nil
	}
	lanes = map[string]struct{}{}
	for _, m := range held {
		for l := range m {
			lanes[l] = struct{}{}
		}
	}
	for idx, scores := range held {
		complete := true
		for l := range lanes {
			if _, ok := scores[l]; !ok {
				complete = false
				break
			}
		}
		if complete {
			subset = append(subset, idx)
		}
	}
	sort.Strings(subset)
	return subset, lanes
}

// replayCorpusWith drives every corpus prompt through the real scorer for an
// in-memory bundle (so a candidate can be measured before it is written to
// disk) and reports the outcome, including the held-out AIDER accuracy on
// the fixed subset.
func replayCorpusWith(t *testing.T, bundle *Bundle, labels []corpusLabel, order []string, embedder *corpusEmbedder, held map[string]map[string]float64) corpusReplayResult {
	t.Helper()
	cfg := DefaultConfig()
	cfg.CostCapMultiple = DefaultCostCapMultiple
	scorer, err := NewScorer(bundle, cfg, embedder, allProviders())
	require.NoError(t, err)

	byPrompt := map[string]map[string]corpusCell{}
	for _, l := range labels {
		if byPrompt[l.GlobalIndex] == nil {
			byPrompt[l.GlobalIndex] = map[string]corpusCell{}
		}
		byPrompt[l.GlobalIndex][l.Model] = corpusCell{score: l.Score, cost: l.Cost}
	}
	costsByModel := map[string][]float64{}
	for _, l := range labels {
		if l.Cost != nil {
			costsByModel[l.Model] = append(costsByModel[l.Model], *l.Cost)
		}
	}
	medianCost := map[string]float64{}
	for m, cs := range costsByModel {
		sort.Float64s(cs)
		medianCost[m] = cs[len(cs)/2]
	}
	promptTexts := map[string]string{}
	for _, l := range labels {
		if _, ok := promptTexts[l.GlobalIndex]; !ok {
			promptTexts[l.GlobalIndex] = l.Question
		}
	}

	res := corpusReplayResult{Version: bundle.Version, Mix: map[string]int{},
		PerPromptModel: map[string]string{}, PerPromptScore: map[string]float64{}}
	for _, idx := range order {
		dec, err := scorer.Route(context.Background(), router.Request{PromptText: promptTexts[idx]})
		require.NoError(t, err, "prompt %s must route", idx)
		res.Routed++
		res.Mix[dec.Model]++
		res.PerPromptModel[idx] = dec.Model

		c := byPrompt[idx][dec.Model]
		if c.score != nil {
			res.Scored++
			res.Accuracy += *c.score
			res.PerPromptScore[idx] = *c.score
		}
		switch {
		case c.cost != nil:
			res.HonestCostTotal += *c.cost
		case medianCost[dec.Model] > 0:
			res.HonestCostTotal += medianCost[dec.Model]
		}
	}
	if res.Scored > 0 {
		res.Accuracy /= float64(res.Scored)
	}
	res.HonestCostPer = res.HonestCostTotal / float64(res.Routed)
	res.ModelsReached = len(res.Mix)

	// Held-out AIDER accuracy on the fixed subset.
	subset, _ := heldOutSubset(held)
	sum := 0.0
	n := 0
	for _, idx := range subset {
		if s, ok := held[idx][res.PerPromptModel[idx]]; ok {
			sum += s
			n++
		}
	}
	if n > 0 {
		res.HeldOutAccuracy = sum / float64(n)
		res.HeldOutScored = n
	}
	return res
}

// corpusCell is one labelled cell's measured score and cost.
type corpusCell struct {
	score *float64
	cost  *float64
}

// replayCorpus drives every corpus prompt through the real scorer for one
// bundle version and reports the outcome.
func replayCorpus(t *testing.T, version string, labels []corpusLabel, order []string, embedder *corpusEmbedder) corpusReplayResult {
	t.Helper()
	bundle, err := LoadBundle(version)
	require.NoError(t, err)
	require.True(t, bundle.IsV2, "%s must load as a v2 bundle", version)

	cfg := DefaultConfig()
	cfg.CostCapMultiple = DefaultCostCapMultiple
	scorer, err := NewScorer(bundle, cfg, embedder, allProviders())
	require.NoError(t, err)

	// index the labels: prompt -> model -> (score, cost)
	type cell struct {
		score *float64
		cost  *float64
	}
	byPrompt := map[string]map[string]cell{}
	for _, l := range labels {
		if byPrompt[l.GlobalIndex] == nil {
			byPrompt[l.GlobalIndex] = map[string]cell{}
		}
		byPrompt[l.GlobalIndex][l.Model] = cell{score: l.Score, cost: l.Cost}
	}
	// per-model median measured cost, for a routed prompt whose chosen model
	// has no label on that prompt (the honest cost must still count it).
	costsByModel := map[string][]float64{}
	for _, l := range labels {
		if l.Cost != nil {
			costsByModel[l.Model] = append(costsByModel[l.Model], *l.Cost)
		}
	}
	medianCost := map[string]float64{}
	for m, cs := range costsByModel {
		sort.Float64s(cs)
		medianCost[m] = cs[len(cs)/2]
	}
	promptTexts := map[string]string{}
	for _, l := range labels {
		if _, ok := promptTexts[l.GlobalIndex]; !ok {
			promptTexts[l.GlobalIndex] = l.Question
		}
	}

	res := corpusReplayResult{Version: version, Mix: map[string]int{},
		PerPromptModel: map[string]string{}, PerPromptScore: map[string]float64{}}
	for _, idx := range order {
		dec, err := scorer.Route(context.Background(), router.Request{
			PromptText: promptTexts[idx],
		})
		require.NoError(t, err, "prompt %s must route", idx)
		res.Routed++
		res.Mix[dec.Model]++
		res.PerPromptModel[idx] = dec.Model

		c := byPrompt[idx][dec.Model]
		if c.score != nil {
			res.Scored++
			res.Accuracy += *c.score
			res.PerPromptScore[idx] = *c.score
		}
		switch {
		case c.cost != nil:
			res.HonestCostTotal += *c.cost
		case medianCost[dec.Model] > 0:
			res.HonestCostTotal += medianCost[dec.Model]
		}
	}
	if res.Scored > 0 {
		res.Accuracy /= float64(res.Scored)
	}
	res.HonestCostPer = res.HonestCostTotal / float64(res.Routed)
	res.ModelsReached = len(res.Mix)
	return res
}

// TestCorpusReplay compares the shipped bundles over the labelled corpus
// through the production scorer. It is a report, not a gate: the assertions
// are deliberately weak so it never blocks a bundle on a local-only fixture.
//
//	go test ./internal/router/cluster/ -run TestCorpusReplay -v
func TestCorpusReplay(t *testing.T) {
	labels, order, embedder := loadCorpus(t)
	versions := []string{"v0.83", "v0.84", "v0.85", "v0.86"}
	for _, v := range versions {
		if _, err := LoadBundle(v); err != nil {
			t.Logf("skip %s: %v", v, err)
			continue
		}
		res := replayCorpus(t, v, labels, order, embedder)
		mix := map[string]int{}
		for m, n := range res.Mix {
			mix[m] = n
		}
		out, _ := json.Marshal(map[string]any{
			"version":        res.Version,
			"routed":         res.Routed,
			"scored":         res.Scored,
			"accuracy":       res.Accuracy,
			"cost_per_prompt": res.HonestCostPer,
			"models_reached": res.ModelsReached,
			"mix":            mix,
		})
		fmt.Printf("CORPUS_REPLAY %s\n", out)
	}
}

// TestCorpusReplayBundleFilesResolve keeps the harness honest about the
// path it reads: the corpus lives under .context/, which is gitignored, so
// the test must skip rather than fail when it is absent.
func TestCorpusReplayBundleFilesResolve(t *testing.T) {
	if _, err := filepath.Abs(corpusLabelsPath); err != nil {
		t.Fatalf("corpus path does not resolve: %v", err)
	}
}
