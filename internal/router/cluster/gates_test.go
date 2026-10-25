package cluster

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
)

// gateFixture loads a named bundle with the precomputed register-probe
// embeddings, exposing a lookup by probe id and the centroid fallback vectors
// the gate tests route with.
type gateFixture struct {
	t        *testing.T
	bundle   *Bundle
	byProbe  map[string]string // probe id -> text
	byText   map[string][]float32
	embedder *gateEmbedder
	probes   []gateProbe // corpus order
}

// gateEmbedder serves precomputed probe vectors and, for unknown texts, a
// fixed fallback vector — the difficulty-gate tests pad probe text with long
// code content, which has no cached embedding.
type gateEmbedder struct {
	byText   map[string][]float32
	fallback []float32
	id       string
	dim      int
	lastText string
}

func (e *gateEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.lastText = text
	if v, ok := e.byText[text]; ok {
		return v, nil
	}
	return e.fallback, nil
}
func (e *gateEmbedder) ID() string { return e.id }
func (e *gateEmbedder) Dim() int   { return e.dim }

type gateProbe struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Register string `json:"register"`
}

// embHeader mirrors the JSON header written by scripts/embed_register_probes.py.
type embHeader struct {
	EmbedderID string `json:"embedder_id"`
	EmbedDim   int    `json:"embed_dim"`
	N          int    `json:"n"`
}

const gateMaxPromptChars = 1024

func loadGateFixture(t *testing.T, version string) *gateFixture {
	t.Helper()
	bundle, err := LoadBundle(version)
	if err != nil {
		t.Fatalf("load %s bundle: %v", version, err)
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "register_probes.jsonl"))
	if err != nil {
		t.Fatalf("read register_probes.jsonl: %v", err)
	}
	var probes []gateProbe
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p gateProbe
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatalf("parse probe line: %v", err)
		}
		probes = append(probes, p)
	}
	byProbe := map[string]string{}
	byText := map[string][]float32{}
	for _, p := range probes {
		byProbe[p.ID] = p.Text
	}

	embRaw, err := os.ReadFile(filepath.Join("testdata", "register_probes.emb"))
	if err != nil {
		t.Fatalf("read register_probes.emb: %v", err)
	}
	if len(embRaw) < 8 || string(embRaw[:4]) != "PEM1" {
		t.Fatal("bad magic in register_probes.emb")
	}
	hlen := binary.LittleEndian.Uint32(embRaw[4:8])
	var hdr embHeader
	if err := json.Unmarshal(embRaw[8:8+hlen], &hdr); err != nil {
		t.Fatalf("parse emb header: %v", err)
	}
	off := 8 + int(hlen)
	if off+hdr.N*hdr.EmbedDim*4 != len(embRaw) {
		t.Fatal("emb body size mismatch")
	}
	p := off
	for i := 0; i < hdr.N && i < len(probes); i++ {
		v := make([]float32, hdr.EmbedDim)
		for d := 0; d < hdr.EmbedDim; d++ {
			v[d] = math.Float32frombits(binary.LittleEndian.Uint32(embRaw[p : p+4]))
			p += 4
		}
		byText[TailTruncate(probes[i].Text, gateMaxPromptChars)] = v
	}

	return &gateFixture{
		t:       t,
		bundle:  bundle,
		byProbe: byProbe,
		byText:  byText,
		probes:  probes,
		embedder: &gateEmbedder{
			byText:   byText,
			fallback: bundle.Centroids.Row(0), // nearest cluster of a centroid is itself
			id:       hdr.EmbedderID,
			dim:      hdr.EmbedDim,
		},
	}
}

func gateTestConfig(enabled bool) Config {
	cfg := DefaultConfig()
	cfg.TrivialGate = GateConfig{
		Enabled:         enabled,
		PrimaryClusters: []int{2, 4, 9},
		Model:           catalog.ModelIDGLM53Flash.String(),
	}
	cfg.DifficultyGate = DifficultyGateConfig{
		Enabled:                 enabled,
		PrimaryClusters:         []int{0},
		Model:                   catalog.ModelIDGLM53.String(),
		HardMinChars:            2000,
		HardCodeMinChars:        1000,
		HardInstructionMinChars: 40,
	}
	return cfg
}

func (f *gateFixture) scorer(t *testing.T, cfg Config) *Scorer {
	t.Helper()
	s, err := NewScorer(f.bundle, cfg, f.embedder, map[string]struct{}{providers.ProviderAIAND: {}})
	if err != nil {
		t.Fatalf("NewScorer: %v", err)
	}
	return s
}

func gateRoute(t *testing.T, s *Scorer, text string) (router.Decision, error) {
	t.Helper()
	return s.Route(context.Background(), router.Request{PromptText: text})
}

// gateBlendWinner returns what plain argmax would pick for an embedding, so
// assertions compare the gate override against a real computed baseline.
func gateBlendWinner(s *Scorer, vec []float32) string {
	top := topPNearest(vec, s.centroids, s.cfg.TopP)
	scores := s.blendScoresV2(top, s.defaultActiveKnobs(), s.models, nil)
	best, _ := argmax(scores, s.models)
	return best
}

// nearestCluster returns the single centroid closest to vec by cosine
// similarity (dot product on L2-normed vectors), tie-breaking by lower index.
// topPNearest returns its result sorted ascending by cluster id, so a
// fixture's nearest cluster cannot be read off topClusters[0]. Test-only:
// production gates match on top-P membership, not the nearest centroid.
func nearestCluster(vec []float32, c *Centroids) int {
	best, bestSim := 0, float32(-1e30)
	for k := 0; k < c.K; k++ {
		row := c.Row(k)
		var sim float32
		for i, v := range row {
			sim += v * vec[i]
		}
		if sim > bestSim {
			best, bestSim = k, sim
		}
	}
	return best
}

// TestGatePrecedenceDifficultyOverTrivial: when a request's top clusters span
// both lanes, the hard signal decides — a hard-looking request escalates, a
// plain one trivializes.
func TestGatePrecedenceDifficultyOverTrivial(t *testing.T) {
	cfg := gateTestConfig(true)
	trivial, difficulty := cfg.TrivialGate, cfg.DifficultyGate
	spanning := []int{0, 2, 4, 9}
	eligible := []string{trivial.Model, difficulty.Model}

	m, g := applyGates(trivial, difficulty, spanning, len("Implement idempotent retry handling for the checkout worker so double-submits are deduped"), "Implement idempotent retry handling for the checkout worker so double-submits are deduped", eligible)
	if m != difficulty.Model || g != GateDifficulty {
		t.Fatalf("hard-signal request must take the difficulty gate, got %s/%s", m, g)
	}

	m, g = applyGates(trivial, difficulty, spanning, len("hey what is up, how did the deploy go?"), "hey what is up, how did the deploy go?", eligible)
	if m != trivial.Model || g != GateTrivial {
		t.Fatalf("signal-free request must take the trivial gate, got %s/%s", m, g)
	}

	if m, g := applyGates(trivial, difficulty, []int{7, 10}, 40, "hey what is up, how did the deploy go?", eligible); m != "" || g != "" {
		t.Fatalf("request outside both lanes must not gate, got %s/%s", m, g)
	}
}

// findProbeByPrimary locates a corpus probe whose primary cluster passes pred.
// failWhenMissing keeps the test honest: the fixture must keep containing
// probes in the gated lanes.
func (f *gateFixture) findProbeByPrimary(s *Scorer, failWhenMissing bool, pred func(primary int) bool) (id, text string, vec []float32) {
	f.t.Helper()
	for id, text := range f.byProbe {
		vec := f.byText[TailTruncate(text, gateMaxPromptChars)]
		if vec == nil {
			continue
		}
		if pred(nearestCluster(vec, s.centroids)) {
			return id, text, vec
		}
	}
	if failWhenMissing {
		f.t.Fatal("fixture has no probe matching the requested primary-cluster predicate")
	}
	return "", "", nil
}

// TestTrivialGateRoutesCheapModel: a trivial-lane probe routes to glm-5.3-flash
// even when the blend argmax would pick a different (higher-scoring) model.
func TestTrivialGateRoutesCheapModel(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	gated := f.scorer(t, gateTestConfig(true))

	id, text, vec := f.findProbeByPrimary(gated, true, func(p int) bool {
		return p == 2 || p == 4 || p == 9
	})
	if base := gateBlendWinner(gated, vec); base == catalog.ModelIDGLM53Flash.String() {
		t.Fatalf("probe %s: blend already picks the gate model; fixture no longer exercises the override", id)
	}

	dec, err := gateRoute(t, gated, text)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Model != catalog.ModelIDGLM53Flash.String() {
		t.Fatalf("primary on trivial lane routed %q, want %q", dec.Model, catalog.ModelIDGLM53Flash)
	}
}

// TestDifficultyGateRoutesFrontierModel: c0-primary with a long, code-register
// prompt escalates to glm-5.3 over the blend winner.
func TestDifficultyGateRoutesFrontierModel(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	gated := f.scorer(t, gateTestConfig(true))

	// Fallback vector is centroid 0, so any unknown text lands primary on c0.
	code := strings.Repeat("```go\nfunc main() { panic: runtime error: index out of range }\n```\n", 60)
	if len(code) < gateTestConfig(true).DifficultyGate.HardMinChars {
		t.Fatal("test prompt shorter than HardMinChars; would not exercise the length signal")
	}
	dec, err := gateRoute(t, gated, code)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if dec.Model != catalog.ModelIDGLM53.String() {
		t.Fatalf("long code-register c0 prompt routed %q, want %q", dec.Model, catalog.ModelIDGLM53)
	}
}

// TestDifficultyGateShortTrivialUnchanged: c0-primary short conversational text
// carries no hard signal, so the gated decision matches the gates-disabled one.
func TestDifficultyGateShortTrivialUnchanged(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	gated := f.scorer(t, gateTestConfig(true))
	disabled := f.scorer(t, gateTestConfig(false))

	text := "hey, quick question about my order"
	decGated, err := gateRoute(t, gated, text)
	if err != nil {
		t.Fatalf("Route gated: %v", err)
	}
	decPlain, err := gateRoute(t, disabled, text)
	if err != nil {
		t.Fatalf("Route disabled: %v", err)
	}
	if decGated.Model != decPlain.Model {
		t.Fatalf("short c0 text changed under gates: %q vs plain %q", decGated.Model, decPlain.Model)
	}
	if decGated.Metadata == nil || decGated.Metadata.Gate != "" {
		t.Fatalf("expected no gate on unchanged decision, got %+v", decGated.Metadata)
	}
}

// TestGateModelOutOfRosterIsNoOp: excluding the gate model from the request's
// eligible pool leaves the decision on plain argmax.
func TestGateModelOutOfRosterIsNoOp(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	gated := f.scorer(t, gateTestConfig(true))

	_, text, _ := f.findProbeByPrimary(gated, true, func(p int) bool { return p == 2 || p == 4 || p == 9 })

	req := router.Request{
		PromptText:     text,
		ExcludedModels: map[string]struct{}{catalog.ModelIDGLM53Flash.String(): {}},
	}
	decGated, err := gated.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route gated: %v", err)
	}
	decPlain, err := gateRoute(t, f.scorer(t, gateTestConfig(false)), text)
	if err != nil {
		t.Fatalf("Route plain: %v", err)
	}
	if decGated.Model != decPlain.Model {
		t.Fatalf("excluded gate model still overrode: %q vs plain %q", decGated.Model, decPlain.Model)
	}
	if decGated.Metadata.Gate != "" {
		t.Fatalf("gate metadata set despite out-of-roster model: %q", decGated.Metadata.Gate)
	}
}

// TestGateMetadataRecordsOverride: Gate/GateFromModel identify the override
// and the argmax model it displaced; ChosenScore tracks the gated model's
// blend score.
func TestGateMetadataRecordsOverride(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	gated := f.scorer(t, gateTestConfig(true))

	id, text, vec := f.findProbeByPrimary(gated, true, func(p int) bool { return p == 2 || p == 4 || p == 9 })
	argmaxModel := gateBlendWinner(gated, vec)
	if argmaxModel == catalog.ModelIDGLM53Flash.String() {
		t.Fatalf("probe %s: blend already picks the gate model", id)
	}

	dec, err := gateRoute(t, gated, text)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	md := dec.Metadata
	if md == nil {
		t.Fatal("decision has no metadata")
	}
	if md.Gate != GateTrivial {
		t.Fatalf("Gate = %q, want %q", md.Gate, GateTrivial)
	}
	if md.GateFromModel != argmaxModel {
		t.Fatalf("GateFromModel = %q, want blend argmax %q", md.GateFromModel, argmaxModel)
	}
	if dec.Model != catalog.ModelIDGLM53Flash.String() {
		t.Fatalf("decision model %q, want gate model", dec.Model)
	}
	if want := md.CandidateScores[dec.Model]; md.ChosenScore != want {
		t.Fatalf("ChosenScore %v != gated model's candidate score %v", md.ChosenScore, want)
	}
}

// TestGatesDisabledMatchesPlainArgmax: with both gates disabled the decision
// equals the computed blend argmax and carries no gate metadata — the
// regression guard for pre-gate behavior.
func TestGatesDisabledMatchesPlainArgmax(t *testing.T) {
	f := loadGateFixture(t, "v0.88-relabel")
	disabled := f.scorer(t, gateTestConfig(false))

	for id, text := range f.byProbe {
		vec := f.byText[TailTruncate(text, gateMaxPromptChars)]
		if vec == nil {
			continue
		}
		dec, err := gateRoute(t, disabled, text)
		if err != nil {
			t.Fatalf("probe %s: Route: %v", id, err)
		}
		want := gateBlendWinner(disabled, vec)
		if dec.Model != want {
			t.Fatalf("probe %s: gates-disabled decision %q != blend argmax %q", id, dec.Model, want)
		}
		if dec.Metadata.Gate != "" || dec.Metadata.GateFromModel != "" {
			t.Fatalf("probe %s: gate metadata set with gates disabled", id)
		}
	}
}

// TestHardSignalHeuristic unit-checks the length + code-register +
// instruction-register thresholds.
func TestHardSignalHeuristic(t *testing.T) {
	long := strings.Repeat("x", 2000)
	cases := []struct {
		name                         string
		promptChars                  int
		text                         string
		min, codeMin, instructionMin int
		want                         bool
	}{
		{"short plain text", 40, "what is 2+2", 2000, 1000, 40, false},
		{"long plain text over full bar", 2000, long, 2000, 1000, 40, true},
		{"code register under code bar", 900, "```go\nmain()\n```", 2000, 1000, 40, false},
		{"code register over code bar", 1200, "Traceback (most recent call last)", 2000, 1000, 40, true},
		{"long text but zero thresholds", 5000, long, 0, 0, 0, false},
		{"instruction register over floor", 45, "refactor the config loading to read from a single source", 2000, 1000, 40, true},
		{"instruction register under floor", 30, "fix the migration", 2000, 1000, 40, false},
		{"instruction floor disabled", 90, "implement exponential backoff with jitter", 2000, 1000, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hardSignal(tc.promptChars, tc.text, tc.min, tc.codeMin, tc.instructionMin)
			if got != tc.want {
				t.Fatalf("hardSignal = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHasInstructionRegisterMarkerClasses pins the marker-class semantics:
// each class fires on representative phrasing, and conversational /
// copywriting / definitional-knowledge phrasing stays negative.
func TestHasInstructionRegisterMarkerClasses(t *testing.T) {
	positive := map[string]string{
		"imperative dev verb":          "refactor the sync engine to be resumable from a checkpoint",
		"imperative verb mid-sentence": "we should implement optimistic concurrency control here",
		"defect noun":                  "the websocket gateway has a memory leak we cannot explain",
		"defect adjective":             "our integration tests are flaky and fail under load",
		"reliability concept":          "design an outbox with exactly-once semantics for webhooks",
		"hyphenated concept":           "add multi-tenancy with row level security to the schema",
		"dev-workflow noun":            "run the linter across the repo and commit the result",
		"ops noun":                     "the cron job stopped running, check the schedule",
		"camelCase identifier":         "look at how UserCard renders its children props",
		"SCREAMING_SNAKE identifier":   "find where we read the OPENAI_API_KEY env var",
	}
	negative := map[string]string{
		"greeting":               "yo",
		"gratitude":              "thanks for the help earlier, really appreciate it",
		"small talk":             "any fun plans this weekend?",
		"copywriting imperative": "write a tweet announcing our new app launch",
		"rewrite request":        "rephrase this to sound friendlier: your request was denied",
		"definitional knowledge": "whats a race condition and why is it hard to debug",
		"concept question":       "what does eventual consistency actually mean",
		"easy how-to":            "how do i read a file line by line in python",
		"subword no boundary":    "let's rebuild the deck and iterate on the copy tomorrow",
		"casual leak word short": "my water bottle leaks",
	}
	for name, text := range positive {
		if !hasInstructionRegister(text) {
			t.Errorf("%s: %q should fire", name, text)
		}
	}
	for name, text := range negative {
		if hasInstructionRegister(text) {
			t.Errorf("%s: %q should not fire", name, text)
		}
	}
}
