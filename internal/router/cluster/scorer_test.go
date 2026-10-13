package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// fakeEmbedder returns a fixed vector or error; captures last text for
// tail-truncation assertions. Zero-value id/dim default to Jina.
type fakeEmbedder struct {
	vec      []float32
	err      error
	id       string
	dim      int
	lastText string
	calls    int
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.calls++
	f.lastText = text
	return f.vec, f.err
}

func (f *fakeEmbedder) ID() string {
	if f.id == "" {
		return EmbedderJinaV2
	}
	return f.id
}

func (f *fakeEmbedder) Dim() int {
	if f.dim == 0 {
		return EmbedDim
	}
	return f.dim
}

// l2norm normalizes v in place; test fixtures honor the L2-normed
// contract so dot product is cosine similarity.
func l2norm(v []float32) {
	var s float32
	for _, x := range v {
		s += x * x
	}
	if s == 0 {
		return
	}
	inv := 1.0 / float32Sqrt(s)
	for i := range v {
		v[i] *= inv
	}
}

// float32Sqrt avoids the float64 round-trip of math.Sqrt; production
// path uses float32 throughout.
func float32Sqrt(x float32) float32 {
	guess := x / 2
	for i := 0; i < 5; i++ {
		guess = 0.5 * (guess + x/guess)
	}
	return guess
}

// bundleFromBlobs runs real loaders against caller-built blobs.
func bundleFromBlobs(t *testing.T, version string, centroidsBlob, rankingsBlob, registryBlob []byte) *Bundle {
	t.Helper()
	c, err := loadCentroids(centroidsBlob)
	require.NoError(t, err)
	r, err := loadRankings(rankingsBlob)
	require.NoError(t, err)
	reg, err := loadRegistry(registryBlob)
	require.NoError(t, err)
	return &Bundle{
		Version:   version,
		Centroids: c,
		Rankings:  r,
		Registry:  reg,
	}
}

// twoClusterArtifacts: K=2 fixture. Cluster 0 (+e1) prefers Opus;
// cluster 1 (+e2) prefers Haiku.
func twoClusterArtifacts(t *testing.T) (centroidsBlob, rankingsBlob, registryBlob []byte) {
	t.Helper()
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[1] = 1
	full := append(append([]float32{}, c0...), c1...)
	centroidsBlob = buildCentroidsBlob(t, 2, dim, full)

	rankingsBlob = []byte(`{
		"rankings": {
			"0": {"claude-opus-4-7": 0.9, "claude-haiku-4-5": 0.1},
			"1": {"claude-opus-4-7": 0.1, "claude-haiku-4-5": 0.9}
		}
	}`)
	registryBlob = []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)
	return
}

func allProviders() map[string]struct{} {
	return map[string]struct{}{
		"anthropic":             {},
		"openai":                {},
		"google":                {},
		providers.ProviderAIAND: {},
	}
}

func makeOpusVec() []float32 {
	v := make([]float32, EmbedDim)
	v[0] = 1 // aligned with cluster 0 (Opus)
	return v
}

func makeHaikuVec() []float32 {
	v := make([]float32, EmbedDim)
	v[1] = 1 // aligned with cluster 1 (Haiku)
	return v
}

func newScorerForTest(t *testing.T, embedder Embedder, cfg Config) *Scorer {
	t.Helper()
	cb, rb, regb := twoClusterArtifacts(t)
	bundle := bundleFromBlobs(t, "v-test", cb, rb, regb)
	s, err := NewScorer(bundle, cfg, embedder, allProviders())
	require.NoError(t, err)
	return s
}

func cfgForTest() Config {
	c := DefaultConfig()
	// K=2 fixtures; default TopP=4 > K. Tighten.
	c.TopP = 1
	return c
}

func TestScorer_PicksClusterAlignedModel(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newScorerForTest(t, emb, cfgForTest())

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", got.Model)
	assert.Equal(t, "anthropic", got.Provider)
	assert.Contains(t, got.Reason, "cluster:v-test top_p=[0]")
	assert.Contains(t, got.Reason, "model=claude-opus-4-7")
}

// TestScorer_SubscriptionCostFactorNoop: on the V1 fallback bundle (no
// model_axes), a 1.0 subsidy factor must be a no-op on routing and score.
func TestScorer_SubscriptionCostFactorNoop(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newScorerForTest(t, emb, cfgForTest())
	req := router.Request{PromptText: strings.Repeat("x", 100)}

	base, err := s.Route(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, base.Metadata)

	noop := req
	noop.SubsidizedModelCostFactor = map[string]float64{base.Model: 1.0, "claude-haiku-4-5": 1.0}
	noopRes, err := s.Route(context.Background(), noop)
	require.NoError(t, err)
	assert.Equal(t, base.Model, noopRes.Model, "a 1.0 factor must not change the decision")
	assert.InDelta(t, base.Metadata.ChosenScore, noopRes.Metadata.ChosenScore, 1e-9,
		"a 1.0 factor must not change the score")
}

// subscriptionKnobs makes cluster 0 hard (quality-favored, alpha 0.9) and cluster
// 1 easy (cost-favored, alpha 0.2), so the V2 blend has a real margin to flip.
func subscriptionKnobs() *DefaultRoutingKnobs {
	return &DefaultRoutingKnobs{
		Alpha:                []float64{0.9, 0.2},
		SpeedWeight:          0.0,
		OutputCostRatio:      0.0,
		ExpectedOutputTokens: 2000,
	}
}

// TestScorer_SubscriptionPreferenceFlipsAndSpills: in the hard cluster 0
// (opus quality-favored), a slack plan factor (f≈epsilon) on haiku adds a
// headroom bonus that flips the pick to haiku; a bound factor (f=1) zeroes
// the bonus and it spills back to opus.
func TestScorer_SubscriptionPreferenceFlipsAndSpills(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newV2BundleForTest(t, emb, v2BundleOpts{defaultKnobs: subscriptionKnobs()})
	req := router.Request{PromptText: strings.Repeat("x", 100)}

	base, err := s.Route(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-7", base.Model, "no subsidy: quality-favored model wins the hard cluster")

	slack := req
	slack.SubsidizedModelCostFactor = map[string]float64{"claude-haiku-4-5": 0.05}
	slackRes, err := s.Route(context.Background(), slack)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", slackRes.Model,
		"a slack plan (f≈epsilon) lifts the covered model above the otherwise-preferred one")

	bound := req
	bound.SubsidizedModelCostFactor = map[string]float64{"claude-haiku-4-5": 1.0}
	boundRes, err := s.Route(context.Background(), bound)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", boundRes.Model,
		"a bound plan (f=1) zeroes the bonus; routing spills back to the cash model")
}

// TestScorer_SubscriptionPicksCheapestSufficientWithinFamily: with both
// covered models subsidized at the same factor, the uniform bonus can't
// reorder them, so the per-cluster quality/cost blend still picks the
// strong model on a hard cluster and the cheap one on an easy cluster.
func TestScorer_SubscriptionPicksCheapestSufficientWithinFamily(t *testing.T) {
	covered := map[string]float64{"claude-opus-4-7": 0.05, "claude-haiku-4-5": 0.05}

	hard := newV2BundleForTest(t, &fakeEmbedder{vec: makeOpusVec()}, v2BundleOpts{defaultKnobs: subscriptionKnobs()})
	hardRes, err := hard.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100), SubsidizedModelCostFactor: covered,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", hardRes.Model,
		"hard cluster: a subscribed family still routes to the strong model")

	easy := newV2BundleForTest(t, &fakeEmbedder{vec: makeHaikuVec()}, v2BundleOpts{defaultKnobs: subscriptionKnobs()})
	easyRes, err := easy.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("y", 100), SubsidizedModelCostFactor: covered,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", easyRes.Model,
		"easy cluster: a subscribed family routes to the cheap sufficient model (Haiku, not Opus)")
}

// TestScorer_PreferredModelSoftNudge: a preferred model wins a close call
// but never overrides a clearly-better model, and a stale/unknown
// preference is a no-op.
func TestScorer_PreferredModelSoftNudge(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newV2BundleForTest(t, emb, v2BundleOpts{})
	ctx := context.Background()
	prompt := strings.Repeat("x", 100)

	alpha := func(a float64) *router.Overrides { return &router.Overrides{Alpha: &a} }

	// Close call (alpha 0.55): Opus leads Haiku by only ~0.10 in blended score.
	closeReq := router.Request{PromptText: prompt, RoutingKnobs: alpha(0.55)}
	base, err := s.Route(ctx, closeReq)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-7", base.Model, "no preference: quality-favored model wins the close call")

	// Preferring the trailing model flips the close call — the rank-0 bonus
	// (~0.15) exceeds the ~0.10 margin.
	prefHaiku := closeReq
	prefHaiku.PreferredModels = []string{"claude-haiku-4-5"}
	flipped, err := s.Route(ctx, prefHaiku)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", flipped.Model, "a preferred model wins a close call")

	// Clear win (alpha 0.9): Opus leads by ~0.80. The same preference must NOT
	// override a clearly-better model — the soft-nudge quality floor.
	clearReq := router.Request{PromptText: prompt, RoutingKnobs: alpha(0.9), PreferredModels: []string{"claude-haiku-4-5"}}
	clear, err := s.Route(ctx, clearReq)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", clear.Model, "preference must not override a clearly-better model")

	// Stale / unknown preference is ignored: routes identically to no preference.
	staleReq := closeReq
	staleReq.PreferredModels = []string{"does-not-exist"}
	stale, err := s.Route(ctx, staleReq)
	require.NoError(t, err)
	assert.Equal(t, base.Model, stale.Model, "an unknown preferred model is a no-op")
}

// Removing any populated metadata field breaks routing telemetry rows.
func TestScorer_PopulatesRoutingMetadata(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newScorerForTest(t, emb, cfgForTest())

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
	})
	require.NoError(t, err)
	require.NotNil(t, got.Metadata, "scorer must populate Metadata for cluster-routed decisions")
	assert.Equal(t, "v-test", got.Metadata.ClusterRouterVersion)
	assert.ElementsMatch(t, []string{"claude-opus-4-7", "claude-haiku-4-5"}, got.Metadata.CandidateModels,
		"candidate_models must mirror the eligible argmax set")
	assert.NotZero(t, got.Metadata.ChosenScore, "chosen_score must be non-zero for a real decision")
	assert.Equal(t, []int{0}, got.Metadata.ClusterIDs,
		"with cfgForTest TopP=1, only the closest cluster (Opus-aligned) is summed")
}

// The scorer must surface the runner-up alongside the chosen model so the
// turn loop can freeze the {chosen, paired} pair into the session pin.
func TestScorer_PopulatesBandPair(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newScorerForTest(t, emb, cfgForTest())

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
	})
	require.NoError(t, err)
	require.NotNil(t, got.Metadata)
	require.Equal(t, "claude-opus-4-7", got.Model, "chosen must be the cluster-preferred model")
	assert.Equal(t, "claude-haiku-4-5", got.Metadata.PairedModel,
		"paired must be the runner-up, not the chosen model")
	assert.Equal(t, "anthropic", got.Metadata.PairedProvider)
	assert.NotEqual(t, got.Model, got.Metadata.PairedModel, "the pair must be two distinct models")
	assert.Less(t, got.Metadata.PairedScore, got.Metadata.ChosenScore,
		"the runner-up must score below the chosen model")
}

// A single-model eligible pool has no runner-up: PairedModel must be empty so
// the turn loop persists no pair rather than duplicating the chosen model.
func TestScorer_BandPairEmptyForSingleModel(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	rb := []byte(`{"rankings": {"0": {"claude-opus-4-7": 0.9}}}`)
	regb := []byte(`{"deployed_models": [{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true}]}`)
	s, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfgForTest(), &fakeEmbedder{vec: makeOpusVec()}, map[string]struct{}{"anthropic": {}})
	require.NoError(t, err)

	got, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.NoError(t, err)
	require.NotNil(t, got.Metadata)
	assert.Equal(t, "claude-opus-4-7", got.Model)
	assert.Empty(t, got.Metadata.PairedModel, "single-model pool must surface no pair")
	assert.Empty(t, got.Metadata.PairedProvider)
}

func TestScorer_PicksOtherClusterWhenAligned(t *testing.T) {
	emb := &fakeEmbedder{vec: makeHaikuVec()}
	s := newScorerForTest(t, emb, cfgForTest())

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("y", 100),
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", got.Model)
}

// imageFilterArtifacts: K=1 fixture ranking text-only glm-5.3 above
// image-capable opus; an image in the request must drop glm-5.3.
func imageFilterArtifacts(t *testing.T) (centroidsBlob, rankingsBlob, registryBlob []byte) {
	t.Helper()
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	centroidsBlob = buildCentroidsBlob(t, 1, dim, c0)
	rankingsBlob = []byte(`{
		"rankings": {
			"0": {"zai-org/glm-5.3": 0.9, "claude-opus-4-7": 0.1}
		}
	}`)
	registryBlob = []byte(`{
		"deployed_models": [
			{"model": "zai-org/glm-5.3", "provider": "aiand", "bench_column": "x", "proxy": true},
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "y", "proxy": true}
		]
	}`)
	return
}

func TestScorer_DropsTextOnlyModelOnImageTurn(t *testing.T) {
	cb, rb, regb := imageFilterArtifacts(t)
	bundle := bundleFromBlobs(t, "v-test", cb, rb, regb)
	available := map[string]struct{}{"anthropic": {}, providers.ProviderAIAND: {}}
	s, err := NewScorer(bundle, cfgForTest(), &fakeEmbedder{vec: makeOpusVec()}, available)
	require.NoError(t, err)

	// No image: the higher-ranked text-only model wins.
	textTurn, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.NoError(t, err)
	assert.Equal(t, "zai-org/glm-5.3", textTurn.Model, "text turn routes to the cluster-preferred model")

	// Image present: glm-5.3 is dropped, opus is the only image-capable candidate.
	imageTurn, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100), HasImages: true})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", imageTurn.Model, "image turn must skip the text-only model")
	assert.NotContains(t, imageTurn.Metadata.CandidateModels, "zai-org/glm-5.3",
		"text-only model must be absent from the image-turn candidate set")
	assert.NotContains(t, imageTurn.Metadata.ScorerRescuePool, "zai-org/glm-5.3")
	assert.Contains(t, imageTurn.Metadata.ScorerRescuePool, "claude-opus-4-7")
}

func TestScorer_KeepsTextOnlyPoolWhenNoImageCapableCandidate(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	rb := []byte(`{"rankings": {"0": {"zai-org/glm-5.3": 0.9}}}`)
	regb := []byte(`{"deployed_models": [{"model": "zai-org/glm-5.3", "provider": "aiand", "bench_column": "x", "proxy": true}]}`)
	bundle := bundleFromBlobs(t, "v-test", cb, rb, regb)
	s, err := NewScorer(bundle, cfgForTest(), &fakeEmbedder{vec: makeOpusVec()}, map[string]struct{}{providers.ProviderAIAND: {}})
	require.NoError(t, err)

	// No image-capable candidate deployed: the scorer still returns a
	// decision rather than erroring; upstream reports the rejection.
	got, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100), HasImages: true})
	require.NoError(t, err)
	assert.Equal(t, "zai-org/glm-5.3", got.Model)
	assert.Empty(t, got.Metadata.ScorerRescuePool)
}

func TestScorer_ReturnsErrOnEmbedderError(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("ort exploded")}
	s := newScorerForTest(t, emb, cfgForTest())

	_, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClusterUnavailable))
}

// panicEmbedder panics inside Embed to simulate a crash in the native
// ONNX/hugot pipeline, exercising the goroutine's recover().
type panicEmbedder struct{}

func (panicEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	panic("simulated onnx/hugot panic")
}

func (panicEmbedder) ID() string { return EmbedderJinaV2 }

func (panicEmbedder) Dim() int { return EmbedDim }

func TestScorer_RecoversFromEmbedPanic(t *testing.T) {
	s := newScorerForTest(t, panicEmbedder{}, cfgForTest())

	_, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClusterUnavailable))
	assert.Contains(t, err.Error(), "embed panic")
}

func TestScorer_ReturnsErrOnDimMismatch(t *testing.T) {
	emb := &fakeEmbedder{vec: make([]float32, 7)} // wrong size
	s := newScorerForTest(t, emb, cfgForTest())

	_, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClusterUnavailable))
}

func TestScorer_TailTruncatesBeforeEmbed(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	cfg := cfgForTest()
	cfg.MaxPromptChars = 32

	s := newScorerForTest(t, emb, cfg)

	prompt := strings.Repeat("HEAD-", 50) + "TAIL_END"
	_, err := s.Route(context.Background(), router.Request{PromptText: prompt})
	require.NoError(t, err)
	require.LessOrEqual(t, len(emb.lastText), cfg.MaxPromptChars)
	assert.True(t, strings.HasSuffix(prompt, emb.lastText), "tail-truncate must preserve suffix")
}

func TestScorer_TopPSumsAcrossClusters(t *testing.T) {
	// 3 clusters; cluster 2 has overwhelming Haiku preference. TopP=2
	// → Opus wins; TopP=3 → Haiku wins once cluster 2 is summed.
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[1] = 1
	c2 := make([]float32, dim)
	c2[2] = 1
	full := append(append(append([]float32{}, c0...), c1...), c2...)
	centroidsBlob := buildCentroidsBlob(t, 3, dim, full)

	rankingsBlob := []byte(`{
		"rankings": {
			"0": {"claude-opus-4-7": 0.6, "claude-haiku-4-5": 0.0},
			"1": {"claude-opus-4-7": 0.6, "claude-haiku-4-5": 0.0},
			"2": {"claude-opus-4-7": 0.0, "claude-haiku-4-5": 5.0}
		}
	}`)
	registryBlob := []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)

	vec := make([]float32, dim)
	vec[0] = 1
	vec[1] = 1
	vec[2] = 1
	l2norm(vec)
	emb := &fakeEmbedder{vec: vec}

	for _, tc := range []struct {
		topP    int
		want    string
		comment string
	}{
		{1, "claude-opus-4-7", "top-1 lands on cluster 0 (sorted-ascending tie-break), prefers Opus"},
		{2, "claude-opus-4-7", "top-2 sums clusters 0+1, both prefer Opus"},
		{3, "claude-haiku-4-5", "top-3 includes cluster 2 with 5.0 Haiku score; sum overwhelms 1.2 Opus"},
	} {
		t.Run(tc.comment, func(t *testing.T) {
			cfg := cfgForTest()
			cfg.TopP = tc.topP
			emb.calls = 0
			bundle := bundleFromBlobs(t, "v-test", centroidsBlob, rankingsBlob, registryBlob)
			s, err := NewScorer(bundle, cfg, emb, allProviders())
			require.NoError(t, err)
			got, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("z", 100)})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Model)
		})
	}
}

func TestNewScorer_RejectsTopPExceedingK(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t) // K=2
	cfg := cfgForTest()
	cfg.TopP = 5
	_, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfg, &fakeEmbedder{}, allProviders())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TopP")
}

func TestNewScorer_RejectsNilBundle(t *testing.T) {
	_, err := NewScorer(nil, cfgForTest(), &fakeEmbedder{}, allProviders())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle")
}

func TestNewScorer_RejectsNilEmbedder(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	_, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfgForTest(), nil, allProviders())
	require.Error(t, err)
}

func TestNewScorer_RejectsEmptyAvailableProviders(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	_, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfgForTest(), &fakeEmbedder{}, map[string]struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "availableProviders")
}

// The identity guard: a bundle trained in one embedding space must never
// be scored with a different embedder, even at a matching dim.
func TestNewScorer_RejectsEmbedderIDMismatch(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	bundle := bundleFromBlobs(t, "v-test", cb, rb, regb)
	// Legacy bundle (no metadata) declares Jina; a Qwen embedder at the
	// same dim must be refused.
	_, err := NewScorer(bundle, cfgForTest(), &fakeEmbedder{id: EmbedderQwen3, dim: EmbedDim}, allProviders())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares embedder")
}

func TestNewScorer_RejectsEmbedderDimMismatch(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	bundle := bundleFromBlobs(t, "v-test", cb, rb, regb)
	_, err := NewScorer(bundle, cfgForTest(), &fakeEmbedder{dim: 1024}, allProviders())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "centroids dim")
}

// A 1024d Qwen bundle loads and routes end-to-end with a matching
// embedder — the dual-embedder path is not Jina-special-cased.
func TestScorer_QwenBundleRoutes(t *testing.T) {
	dim := 1024
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[1] = 1
	full := append(append([]float32{}, c0...), c1...)
	cb := buildCentroidsBlob(t, 2, dim, full)
	rb := []byte(`{
		"rankings": {
			"0": {"claude-opus-4-7": 0.9, "claude-haiku-4-5": 0.1},
			"1": {"claude-opus-4-7": 0.1, "claude-haiku-4-5": 0.9}
		}
	}`)
	regb := []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)
	bundle := bundleFromBlobs(t, "v-qwen-test", cb, rb, regb)
	bundle.Metadata = &ArtifactMetadata{
		Embedder: ArtifactEmbedder{Model: EmbedderQwen3, EmbedDim: dim},
	}

	vec := make([]float32, dim)
	vec[0] = 1 // aligned with cluster 0 (Opus)
	emb := &fakeEmbedder{id: EmbedderQwen3, dim: dim, vec: vec}
	s, err := NewScorer(bundle, cfgForTest(), emb, allProviders())
	require.NoError(t, err)

	d, err := s.Route(context.Background(), router.Request{PromptText: "design a distributed system"})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", d.Model)
	assert.Equal(t, "anthropic", d.Provider)
}

func TestNewScorer_RejectsRankingsMissingDeployedModel(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	rb := []byte(`{"rankings": {"0": {"claude-opus-4-7": 1.0}}}`)
	regb := []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)
	cfg := cfgForTest()
	cfg.TopP = 1
	_, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfg, &fakeEmbedder{}, allProviders())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing model")
}

func TestScorer_BootFailsWhenNoCandidatesMatchProviders(t *testing.T) {
	cb, rb, regb := twoClusterArtifacts(t)
	_, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfgForTest(), &fakeEmbedder{},
		map[string]struct{}{"openai": {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no deployed entry matches")
}

func TestScorer_FiltersOutUnregisteredProvider(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	rb := []byte(`{"rankings": {"0": {
		"claude-haiku-4-5": 0.1,
		"gpt-5": 0.9,
		"gemini-2.5-pro": 0.5
	}}}`)
	regb := []byte(`{
		"deployed_models": [
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true},
			{"model": "gpt-5", "provider": "openai", "bench_column": "gpt-5"},
			{"model": "gemini-2.5-pro", "provider": "google", "bench_column": "gemini-2.5-pro"}
		]
	}`)
	cfg := cfgForTest()
	cfg.TopP = 1

	// Anthropic only: must pick Anthropic despite gpt-5 scoring higher.
	s, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfg, &fakeEmbedder{vec: makeOpusVec()},
		map[string]struct{}{"anthropic": {}})
	require.NoError(t, err)
	got, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", got.Model)
	assert.Equal(t, "anthropic", got.Provider)

	// Anthropic + OpenAI: gpt-5 wins on score.
	s, err = NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfg, &fakeEmbedder{vec: makeOpusVec()},
		map[string]struct{}{"anthropic": {}, "openai": {}})
	require.NoError(t, err)
	got, err = s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", got.Model)
	assert.Equal(t, "openai", got.Provider)
	assert.Contains(t, got.Reason, "provider=openai")
}

// NOTE: multi-binding fallback coverage (primary vs trailing provider) now
// lives in the catalog package (ResolveBinding/EnumerateBindings tests). No
// AIand-only catalog row carries more than one binding, so a scorer-level
// fixture can no longer exercise it.

func TestScorer_DedupesDuplicateRegistryEntries(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	rb := []byte(`{"rankings": {"0": {
		"claude-opus-4-7": 0.35,
		"claude-haiku-4-5": 0.6
	}}}`)
	regb := []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "claude-opus-4-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)
	cfg := cfgForTest()
	cfg.TopP = 1
	s, err := NewScorer(bundleFromBlobs(t, "v-test", cb, rb, regb), cfg, &fakeEmbedder{vec: makeOpusVec()}, allProviders())
	require.NoError(t, err)

	counts := make(map[string]int, len(s.models))
	for _, m := range s.models {
		counts[m]++
	}
	for m, n := range counts {
		assert.Equalf(t, 1, n, "model %q appears %d times in s.models — duplicate registry entries must dedupe", m, n)
	}
	assert.ElementsMatch(t, []string{"claude-opus-4-7", "claude-haiku-4-5"}, s.models)

	got, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", got.Model, "haiku should win at 0.6 vs opus 0.35; if opus wins, the scoring loop is double-counting its two registry entries")
}

// Per-request EmbedTimeout must cause ErrClusterUnavailable, not a
// silent fallback.
func TestScorer_ReturnsErrOnEmbedTimeout(t *testing.T) {
	slow := &slowEmbedder{delay: 100 * time.Millisecond, vec: makeOpusVec()}
	cfg := cfgForTest()
	cfg.EmbedTimeout = 10 * time.Millisecond
	s := newScorerForTest(t, slow, cfg)

	_, err := s.Route(context.Background(), router.Request{PromptText: strings.Repeat("x", 100)})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrClusterUnavailable))
}

type slowEmbedder struct {
	vec   []float32
	delay time.Duration
}

func (s *slowEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	select {
	case <-time.After(s.delay):
		return s.vec, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *slowEmbedder) ID() string { return EmbedderJinaV2 }

func (s *slowEmbedder) Dim() int { return EmbedDim }

func TestTailTruncate(t *testing.T) {
	got := TailTruncate("abcdef", 3)
	assert.Equal(t, "def", got)

	assert.Equal(t, "ab", TailTruncate("ab", 5))

	in := "héllo"
	got = TailTruncate(in, 4)
	assert.True(t, len(got) <= 4)
	assert.True(t, strings.HasSuffix(in, got))
	for _, r := range got {
		assert.NotEqual(t, '�', r, "result must be valid UTF-8")
	}
}

func TestTopPNearest_DeterministicOnTies(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[0] = 1
	full := append(append([]float32{}, c0...), c1...)
	c, err := loadCentroids(buildCentroidsBlob(t, 2, dim, full))
	require.NoError(t, err)

	v := make([]float32, dim)
	v[0] = 1
	got := topPNearest(v, c, 1)
	assert.Equal(t, []int{0}, got)
}

func TestArgmax_TiebreakUsesOrderSlice(t *testing.T) {
	scores := map[string]float32{"A": 1.0, "B": 1.0}
	gotA, _ := argmax(scores, []string{"A", "B"})
	gotB, _ := argmax(scores, []string{"B", "A"})
	assert.Equal(t, "A", gotA)
	assert.Equal(t, "B", gotB)
}

// twoProviderArtifacts: 2 clusters, one candidate per provider; OpenAI
// outscores Anthropic on the aligned cluster.
func twoProviderArtifacts(t *testing.T) (centroidsBlob, rankingsBlob, registryBlob []byte) {
	t.Helper()
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[1] = 1
	full := append(append([]float32{}, c0...), c1...)
	centroidsBlob = buildCentroidsBlob(t, 2, dim, full)

	rankingsBlob = []byte(`{
		"rankings": {
			"0": {"claude-opus-4-7": 0.5, "gpt-5": 0.9},
			"1": {"claude-opus-4-7": 0.1, "gpt-5": 0.05}
		}
	}`)
	registryBlob = []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "gpt-5", "provider": "openai", "bench_column": "gpt-5"}
		]
	}`)
	return
}

func newTwoProviderScorer(t *testing.T, emb Embedder) *Scorer {
	t.Helper()
	cb, rb, regb := twoProviderArtifacts(t)
	bundle := bundleFromBlobs(t, "v-test-2p", cb, rb, regb)
	available := map[string]struct{}{"anthropic": {}, "openai": {}}
	s, err := NewScorer(bundle, cfgForTest(), emb, available)
	require.NoError(t, err)
	return s
}

// Regression guard for gating opt-in: nil EnabledProviders → argmax
// runs unrestricted over boot-time candidates.
func TestScorer_NilEnabledProvidersPreservesBootBehavior(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
	})
	require.NoError(t, err)
	// Cluster 0 ranks gpt-5 above opus; without gating, gpt-5 wins.
	assert.Equal(t, "gpt-5", got.Model)
	assert.Equal(t, "openai", got.Provider)
}

// Load-bearing: restricting EnabledProviders to {anthropic} forces
// argmax onto anthropic even when openai would otherwise win.
func TestScorer_EnabledProvidersGatesArgmax(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got, err := s.Route(context.Background(), router.Request{
		PromptText:       strings.Repeat("x", 100),
		EnabledProviders: map[string]struct{}{"anthropic": {}},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", got.Model)
	assert.Equal(t, "anthropic", got.Provider)
}

// An exclusive binding set must route only the aliased model, so the
// unaliased model never reaches an upstream it was not declared for.
func TestScorer_GatewayExclusiveRoutesOnlyAliasedModel(t *testing.T) {
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	cb := buildCentroidsBlob(t, 1, dim, c0)
	// The unaliased model outranks the aliased one, so only the gateway gate
	// can produce the expected pick.
	rb := []byte(`{"rankings": {"0": {
		"zai-org/glm-5.3": 0.9,
		"deepseek-ai/deepseek-v4-pro": 0.1
	}}}`)
	regb := []byte(`{
		"deployed_models": [
			{"model": "zai-org/glm-5.3", "provider": "aiand", "bench_column": "x", "proxy": true},
			{"model": "deepseek-ai/deepseek-v4-pro", "provider": "aiand", "bench_column": "y", "proxy": true}
		]
	}`)
	cfg := cfgForTest()
	cfg.TopP = 1
	s, err := NewScorer(bundleFromBlobs(t, "v-test-gw", cb, rb, regb), cfg, &fakeEmbedder{vec: makeOpusVec()},
		map[string]struct{}{providers.ProviderAIAND: {}})
	require.NoError(t, err)

	got, err := s.Route(context.Background(), router.Request{
		PromptText:       strings.Repeat("x", 100),
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		GatewayProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		CustomBindings:   map[string][]string{"deepseek-ai/deepseek-v4-pro": {providers.ProviderAIAND}},
	})
	require.NoError(t, err)
	// Ungated, glm-5.3 wins cluster 0 and would ship an unaliased name upstream.
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", got.Model)
	assert.Equal(t, providers.ProviderAIAND, got.Provider)
}

// A gateway key aliasing nothing is a customer configuration to fix, so the
// scorer must not report the router as unavailable.
func TestScorer_GatewayWithoutAliasesReturnsErrGatewayServesNoDeployedModel(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	_, err := s.Route(context.Background(), router.Request{
		PromptText:       strings.Repeat("x", 100),
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		GatewayProviders: map[string]struct{}{providers.ProviderAIAND: {}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, policy.ErrGatewayServesNoDeployedModel))
	assert.False(t, errors.Is(err, ErrClusterUnavailable))
}

// Installation with no resolvable provider keys must surface a
// 4xx-mappable error, not pick a model that 401s upstream.
func TestScorer_EmptyEnabledProvidersReturnsErrNoEligibleProvider(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	_, err := s.Route(context.Background(), router.Request{
		PromptText:       strings.Repeat("x", 100),
		EnabledProviders: map[string]struct{}{"google": {}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoEligibleProvider))
	// Must not also surface as ErrClusterUnavailable; API maps these
	// sentinels to different status codes (400 vs 503).
	assert.False(t, errors.Is(err, ErrClusterUnavailable))
}

// Per-installation model exclusion drops the named model from argmax even
// when its provider is otherwise eligible.
func TestScorer_ExcludedModelsRemovesFromArgmax(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got, err := s.Route(context.Background(), router.Request{
		PromptText:     strings.Repeat("x", 100),
		ExcludedModels: map[string]struct{}{"gpt-5": {}},
	})
	require.NoError(t, err)
	// Without exclusion gpt-5 wins; excluding it forces argmax onto claude.
	assert.Equal(t, "claude-opus-4-7", got.Model)
	assert.Equal(t, "anthropic", got.Provider)
	assert.NotContains(t, got.Metadata.ScorerRescuePool, "gpt-5")
}

func TestScorer_AutomaticExcludedModelsRemovesFromArgmax(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got, err := s.Route(context.Background(), router.Request{
		PromptText:              strings.Repeat("x", 100),
		AutomaticExcludedModels: map[string]struct{}{"gpt-5": {}},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", got.Model)
	assert.Equal(t, "anthropic", got.Provider)
	assert.NotContains(t, got.Metadata.CandidateModels, "gpt-5")
	assert.Contains(t, got.Metadata.ScorerRescuePool, "gpt-5")
}

// The deployment-wide set is soft: unlike ExcludedModels it must never fail a
// turn, because the same models stay reachable through an explicit user pin.
func TestScorer_AutomaticExcludedModelsEmptyingPoolStillRoutes(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
		AutomaticExcludedModels: map[string]struct{}{
			"gpt-5":           {},
			"claude-opus-4-7": {},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", got.Model)
}

// Excluding every deployed model must surface ErrNoEligibleProvider (no
// silent fallback to a default).
func TestScorer_ExcludedModelsEmptyingPoolReturnsErrNoEligibleProvider(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	_, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
		ExcludedModels: map[string]struct{}{
			"gpt-5":           {},
			"claude-opus-4-7": {},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoEligibleProvider))
	assert.False(t, errors.Is(err, ErrClusterUnavailable))
}

// ErrAllowlistEmptiesPool must wrap ErrNoEligibleProvider (existing checks keep
// matching) and name the allowlist, not the desugared exclusion list.
func TestScorer_AllowlistEmptyingPoolReturnsErrAllowlistEmptiesPool(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	_, err := s.Route(context.Background(), router.Request{
		PromptText: strings.Repeat("x", 100),
		// The proxy desugars the allowlist into ExcludedModels; AllowedModels
		// only names the cause.
		ExcludedModels: map[string]struct{}{
			"gpt-5":           {},
			"claude-opus-4-7": {},
		},
		AllowedModels: map[string]struct{}{"some-retired-model": {}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAllowlistEmptiesPool))
	assert.True(t, errors.Is(err, ErrNoEligibleProvider), "must still satisfy existing ErrNoEligibleProvider checks")
	assert.Contains(t, err.Error(), "some-retired-model", "the error must name the allowlist, not the desugared exclusions")
	assert.False(t, errors.Is(err, ErrClusterUnavailable))
}

// NOTE: the ToolUseLow/AgenticLow argmax filters are inert on the AIand-only
// catalog — no surviving row carries either flag, so the soft empty-pool
// fallback is unreachable through real catalog rows and its fixtures are gone.

// DeployedModels returns the full provider-filtered candidate list, not the
// per-request eligible subset.
func TestScorer_DeployedModelsReturnsBootCandidates(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	s := newTwoProviderScorer(t, emb)

	got := s.DeployedModels()
	models := make([]string, 0, len(got))
	for _, e := range got {
		models = append(models, e.Model)
	}
	// Two-provider scorer fixture has gpt-5 + claude-opus-4-7.
	assert.Contains(t, models, "gpt-5")
	assert.Contains(t, models, "claude-opus-4-7")
}

// NOTE: the catalog-retired-registry drop is inert on the AIand-only catalog —
// every surviving row is tiered, and catalog.ByID no longer resolves retired
// slugs, so the untiered-passthrough branch is unreachable through real data
// and its fixture is gone. Off-catalog registry rows still resolve via their
// recorded provider (defense in depth, covered by the boot-candidate tests).

func TestScorer_V2DynamicScoring(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	regBlob := []byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`)
	dim := EmbedDim
	c0 := make([]float32, dim)
	c0[0] = 1
	c1 := make([]float32, dim)
	c1[1] = 1
	full := append(append([]float32{}, c0...), c1...)
	centroidsBlob := buildCentroidsBlob(t, 2, dim, full)

	centroids, err := loadCentroids(centroidsBlob)
	require.NoError(t, err)
	registry, err := loadRegistry(regBlob)
	require.NoError(t, err)

	qualityMeans := Rankings{
		0: map[string]float32{"claude-opus-4-7": 0.9, "claude-haiku-4-5": 0.1},
		1: map[string]float32{"claude-opus-4-7": 0.1, "claude-haiku-4-5": 0.9},
	}

	opusInputPrice := 0.015
	opusOutputPrice := 0.075
	haikuInputPrice := 0.00025
	haikuOutputPrice := 0.00125

	opusTTFT := 0.8
	opusTPS := 60.0
	haikuTTFT := 0.2
	haikuTPS := 120.0

	opusVerbosity := 4000.0
	haikuVerbosity := 1000.0

	modelAxes := map[string]ModelAxis{
		"claude-opus-4-7": {
			InputPer1KUSD:   &opusInputPrice,
			OutputPer1KUSD:  &opusOutputPrice,
			TTFTSeconds:     &opusTTFT,
			TPS:             &opusTPS,
			VerbosityTokens: &opusVerbosity,
		},
		"claude-haiku-4-5": {
			InputPer1KUSD:   &haikuInputPrice,
			OutputPer1KUSD:  &haikuOutputPrice,
			TTFTSeconds:     &haikuTTFT,
			TPS:             &haikuTPS,
			VerbosityTokens: &haikuVerbosity,
		},
	}

	meta := &ArtifactMetadata{
		FormatVersion: 2,
		Training: ArtifactTraining{
			K:    2,
			TopP: 2,
			DefaultRoutingKnobs: &DefaultRoutingKnobs{
				Alpha:                []float64{0.5, 0.5},
				SpeedWeight:          0.0,
				OutputCostRatio:      0.0,
				ExpectedOutputTokens: 2000,
				PerModelVerbosity:    false,
			},
		},
	}

	bundle := &Bundle{
		Version:         "v2-test",
		Centroids:       centroids,
		Registry:        registry,
		Metadata:        meta,
		IsV2:            true,
		QualityMeans:    qualityMeans,
		ModelAxes:       modelAxes,
		MedianVerbosity: 2500.0,
	}

	cfg := DefaultConfig()
	cfg.TopP = 1
	// Case 2 asserts the blend's own quality argmax; the cost cap (a separate
	// post-blend policy layer) is off so the case tests the blend alone.
	cfg.CostCapMultiple = 0
	scorer, err := NewScorer(bundle, cfg, emb, allProviders())
	require.NoError(t, err)

	// Test case 1: Default knobs (alpha=0.5, speed_weight=0, output_cost_ratio=0).
	dec, err := scorer.Route(context.Background(), router.Request{
		RequestedModel: "auto",
		PromptText:     "test",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, dec.Model)
	assert.NotNil(t, dec.Metadata)
	assert.NotZero(t, dec.Metadata.EffectiveKnobsHash)

	// Test case 2: Override alpha to 1.0 (extreme quality-preferring).
	alphaVal := 1.0
	decQuality, err := scorer.Route(context.Background(), router.Request{
		RequestedModel: "auto",
		PromptText:     "test",
		RoutingKnobs: &router.Overrides{
			Alpha: &alphaVal,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", decQuality.Model)

	// Test case 3: Override alpha to 0.0 (extreme cost-preferring).
	alphaValZero := 0.0
	decCost, err := scorer.Route(context.Background(), router.Request{
		RequestedModel: "auto",
		PromptText:     "test",
		RoutingKnobs: &router.Overrides{
			Alpha: &alphaValZero,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", decCost.Model)

	// Test case 4: Out-of-bounds validation.
	badAlpha := -0.1
	_, err = scorer.Route(context.Background(), router.Request{
		RequestedModel: "auto",
		PromptText:     "test",
		RoutingKnobs: &router.Overrides{
			Alpha: &badAlpha,
		},
	})
	assert.ErrorIs(t, err, ErrInvalidRoutingKnobs)
}

// v2BundleOpts is the optional knob configuration for newV2BundleForTest.
type v2BundleOpts struct {
	qualityMeans    Rankings // overrides the default opus/haiku table
	modelAxes       map[string]ModelAxis
	medianVerbosity float64
	defaultKnobs    *DefaultRoutingKnobs
	// costCapMultiple overrides the Config's cost cap for the built Scorer.
	// 0 disables the cap; unset keeps DefaultConfig's measured default.
	costCapMultiple *float64
	// costCapMode overrides Config.CostCapMode; unset keeps DefaultConfig's
	// CostCapModeGlobal (the historical deployment-wide budget).
	costCapMode string
	// clusterCount overrides the synthetic bundle's cluster count (centroids are
	// one-hot in dims 0..count-1, so an embed vector summing those dims makes all
	// of them the prompt's top-P). Unset keeps the harness's two-cluster default.
	clusterCount int
	// topP overrides the Scorer's TopP; unset keeps the harness default of 1,
	// so tests that only exercise one cluster are unaffected.
	topP *int
}

// newV2BundleForTest builds a synthetic two-cluster v2 bundle (Opus +
// Haiku across clusters 0/1) with per-axis overrides via opts.
func newV2BundleForTest(t *testing.T, emb Embedder, opts v2BundleOpts) *Scorer {
	t.Helper()
	dim := EmbedDim
	k := opts.clusterCount
	if k <= 0 {
		k = 2
	}
	require.LessOrEqual(t, k, dim, "synthetic cluster count must fit the embedding dim")
	full := make([]float32, 0, k*dim)
	for c := range k {
		row := make([]float32, dim)
		row[c] = 1
		full = append(full, row...)
	}
	centroidsBlob := buildCentroidsBlob(t, k, dim, full)
	centroids, err := loadCentroids(centroidsBlob)
	require.NoError(t, err)

	registry, err := loadRegistry([]byte(`{
		"deployed_models": [
			{"model": "claude-opus-4-7", "provider": "anthropic", "bench_column": "gpt-5", "proxy": true},
			{"model": "claude-haiku-4-5", "provider": "anthropic", "bench_column": "gemini-2.5-flash", "proxy": true}
		]
	}`))
	require.NoError(t, err)

	qm := opts.qualityMeans
	if qm == nil {
		qm = Rankings{
			0: {"claude-opus-4-7": 0.9, "claude-haiku-4-5": 0.1},
			1: {"claude-opus-4-7": 0.1, "claude-haiku-4-5": 0.9},
		}
	}

	axes := opts.modelAxes
	if axes == nil {
		opusInputP := 0.015
		opusOutputP := 0.075
		haikuInputP := 0.00025
		haikuOutputP := 0.00125
		opusTTFT := 0.8
		opusTPS := 60.0
		haikuTTFT := 0.2
		haikuTPS := 120.0
		axes = map[string]ModelAxis{
			"claude-opus-4-7": {
				InputPer1KUSD:  &opusInputP,
				OutputPer1KUSD: &opusOutputP,
				TTFTSeconds:    &opusTTFT,
				TPS:            &opusTPS,
			},
			"claude-haiku-4-5": {
				InputPer1KUSD:  &haikuInputP,
				OutputPer1KUSD: &haikuOutputP,
				TTFTSeconds:    &haikuTTFT,
				TPS:            &haikuTPS,
			},
		}
	}

	median := opts.medianVerbosity
	if median == 0 {
		median = 1.0
	}

	defaults := opts.defaultKnobs
	if defaults == nil {
		defaults = &DefaultRoutingKnobs{
			Alpha:                []float64{0.5, 0.5},
			SpeedWeight:          0.0,
			OutputCostRatio:      0.0,
			ExpectedOutputTokens: 2000,
			PerModelVerbosity:    false,
		}
	}

	meta := &ArtifactMetadata{
		FormatVersion: 2,
		Training: ArtifactTraining{
			K:                   k,
			TopP:                2,
			DefaultRoutingKnobs: defaults,
		},
	}

	bundle := &Bundle{
		Version:         "v2-test",
		Centroids:       centroids,
		Registry:        registry,
		Metadata:        meta,
		IsV2:            true,
		QualityMeans:    qm,
		ModelAxes:       axes,
		MedianVerbosity: median,
	}

	cfg := DefaultConfig()
	cfg.TopP = 1
	// Cap off by default: existing tests assert the blend's own argmax over
	// both models. The deployment knob's own coverage sets costCapMultiple
	// explicitly (see TestScorer_CostCapRestrictsToCheapestLane).
	cfg.CostCapMultiple = 0
	if opts.costCapMultiple != nil {
		cfg.CostCapMultiple = *opts.costCapMultiple
	}
	if opts.costCapMode != "" {
		cfg.CostCapMode = opts.costCapMode
	}
	if opts.topP != nil {
		cfg.TopP = *opts.topP
	}
	scorer, err := NewScorer(bundle, cfg, emb, allProviders())
	require.NoError(t, err)
	return scorer
}

// When every model ties on quality (q_range==0), the blend must collapse
// to cost (+ speed) only, matching the trainer's behavior.
func TestDegenerateRangeFallthrough(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	tied := Rankings{
		0: {"claude-opus-4-7": 0.5, "claude-haiku-4-5": 0.5},
		1: {"claude-opus-4-7": 0.5, "claude-haiku-4-5": 0.5},
	}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		qualityMeans: tied,
		defaultKnobs: &DefaultRoutingKnobs{
			Alpha:                []float64{0.5, 0.5},
			SpeedWeight:          0.0,
			OutputCostRatio:      1.0, // cost matters
			ExpectedOutputTokens: 2000,
		},
	})
	dec, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	// With quality tied and cost-aware blend, Haiku (cheaper) must win.
	assert.Equal(t, "claude-haiku-4-5", dec.Model, "tied quality should let cost decide")
}

// When exactly one deployed model has AA timing data (s_range==0), the
// blend must fall back to (quality + cost) with w_s redistributed.
func TestSpeedRangeZeroFallsBackToTwoAxis(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	opusInputP := 0.015
	opusOutputP := 0.075
	haikuInputP := 0.00025
	haikuOutputP := 0.00125
	opusTTFT := 0.8
	opusTPS := 60.0
	axes := map[string]ModelAxis{
		"claude-opus-4-7": {
			InputPer1KUSD:  &opusInputP,
			OutputPer1KUSD: &opusOutputP,
			TTFTSeconds:    &opusTTFT,
			TPS:            &opusTPS,
		},
		"claude-haiku-4-5": {
			// No AA timing data — sPtr will be nil.
			InputPer1KUSD:  &haikuInputP,
			OutputPer1KUSD: &haikuOutputP,
		},
	}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		modelAxes: axes,
		defaultKnobs: &DefaultRoutingKnobs{
			Alpha:                []float64{0.5, 0.5},
			SpeedWeight:          0.3,
			OutputCostRatio:      0.0,
			ExpectedOutputTokens: 2000,
		},
	})
	dec, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	// With s_range==0, both models hit redistribution; Opus wins on
	// quality (cluster 0, opus=0.9 vs haiku=0.1).
	assert.Equal(t, "claude-opus-4-7", dec.Model, "s_range==0 must not crash; quality still decides")
}

// Overrides equal to the bundle defaults must yield the same decision as
// no overrides at all.
func TestKnobOverrideOnlyReweights(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{})

	decDefault, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)

	a := 0.5
	w := 0.0
	r := 0.0
	tk := 2000
	v := false
	decOverride, err := scorer.Route(context.Background(), router.Request{
		PromptText: "test",
		RoutingKnobs: &router.Overrides{
			Alpha:                &a,
			SpeedWeight:          &w,
			OutputCostRatio:      &r,
			ExpectedOutputTokens: &tk,
			PerModelVerbosity:    &v,
		},
	})
	require.NoError(t, err)

	assert.Equal(t, decDefault.Model, decOverride.Model, "override matching defaults must yield identical decision")
	assert.Equal(t, decDefault.Metadata.EffectiveKnobsHash, decOverride.Metadata.EffectiveKnobsHash, "matching knobs must hash identically")
}

// A scalar alpha override uniformly overwrites every per-cluster alpha,
// discarding calibration.
func TestAlphaScalarReplacesVector(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}

	// Per-cluster calibrated alphas: cluster 0 cares about quality more.
	calibrated := &DefaultRoutingKnobs{
		Alpha:                []float64{0.9, 0.1},
		SpeedWeight:          0.0,
		OutputCostRatio:      1.0, // cost active
		ExpectedOutputTokens: 2000,
	}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{defaultKnobs: calibrated})

	// At default knobs, vec aligned with cluster 0, alpha[0]=0.9 → quality dominates → Opus.
	decDefault, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", decDefault.Model, "alpha[0]=0.9 + cluster 0 should pick Opus")

	// Override alpha=0.0 (scalar). Both clusters get alpha=0 → cost dominates → Haiku.
	zero := 0.0
	decOverride, err := scorer.Route(context.Background(), router.Request{
		PromptText:   "test",
		RoutingKnobs: &router.Overrides{Alpha: &zero},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", decOverride.Model, "scalar alpha=0 must overwrite per-cluster 0.9, letting cost decide")
}

// When w_s > 0 but no model has AA timing data, the blend must fall back
// to (quality + cost) with w_s redistributed.
func TestZeroAATimingFallback(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	opusInputP := 0.015
	opusOutputP := 0.075
	haikuInputP := 0.00025
	haikuOutputP := 0.00125
	axes := map[string]ModelAxis{
		"claude-opus-4-7": {
			InputPer1KUSD:  &opusInputP,
			OutputPer1KUSD: &opusOutputP,
			// No TTFT or TPS.
		},
		"claude-haiku-4-5": {
			InputPer1KUSD:  &haikuInputP,
			OutputPer1KUSD: &haikuOutputP,
		},
	}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		modelAxes: axes,
		defaultKnobs: &DefaultRoutingKnobs{
			Alpha:                []float64{0.5, 0.5},
			SpeedWeight:          0.5, // active, but no model has timing
			OutputCostRatio:      0.0,
			ExpectedOutputTokens: 2000,
		},
	})
	dec, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	// No timing data, w_s redistributed; quality dominates redistribution → Opus.
	assert.Equal(t, "claude-opus-4-7", dec.Model)
}

// With alpha=0, w_s=1 (pure speed) and no AA timing data, total weight
// is 0; the fallback must return raw qNorm rather than NaN/panic.
func TestPureSpeedMissingTimingFallsBackToQuality(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	haikuInputP := 0.00025
	haikuOutputP := 0.00125
	opusInputP := 0.015
	opusOutputP := 0.075
	axes := map[string]ModelAxis{
		"claude-opus-4-7": {
			// No AA timing data.
			InputPer1KUSD:  &opusInputP,
			OutputPer1KUSD: &opusOutputP,
		},
		"claude-haiku-4-5": {
			// No AA timing data.
			InputPer1KUSD:  &haikuInputP,
			OutputPer1KUSD: &haikuOutputP,
		},
	}
	scorer := newV2BundleForTest(t, emb, v2BundleOpts{
		modelAxes: axes,
		defaultKnobs: &DefaultRoutingKnobs{
			Alpha:                []float64{0.0, 0.0},
			SpeedWeight:          1.0,
			OutputCostRatio:      0.0,
			ExpectedOutputTokens: 2000,
		},
	})
	dec, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	// With alpha=0, w_s=1, total fallback kicks in; raw qNorm wins. Opus
	// has qNorm=1 in cluster 0; Haiku has 0. Opus must win.
	assert.Equal(t, "claude-opus-4-7", dec.Model)
}

// TestInvalidEffectiveKnobs covers the validation paths that must return
// ErrInvalidRoutingKnobs.
func TestInvalidEffectiveKnobs(t *testing.T) {
	cases := []struct {
		name  string
		knobs router.Overrides
	}{
		{
			name: "alpha out of range",
			knobs: func() router.Overrides {
				a := 1.5
				return router.Overrides{Alpha: &a}
			}(),
		},
		{
			name: "alpha+speed_weight exceeds 1",
			knobs: func() router.Overrides {
				a := 0.8
				w := 0.5
				return router.Overrides{Alpha: &a, SpeedWeight: &w}
			}(),
		},
		{
			name: "speed_weight out of range",
			knobs: func() router.Overrides {
				w := 1.5
				return router.Overrides{SpeedWeight: &w}
			}(),
		},
		{
			name: "output_cost_ratio out of range",
			knobs: func() router.Overrides {
				r := 11.0
				return router.Overrides{OutputCostRatio: &r}
			}(),
		},
		{
			name: "expected_output_tokens out of range",
			knobs: func() router.Overrides {
				tk := 200000
				return router.Overrides{ExpectedOutputTokens: &tk}
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emb := &fakeEmbedder{vec: makeOpusVec()}
			scorer := newV2BundleForTest(t, emb, v2BundleOpts{})
			_, err := scorer.Route(context.Background(), router.Request{
				PromptText:   "test",
				RoutingKnobs: &tc.knobs,
			})
			assert.ErrorIs(t, err, ErrInvalidRoutingKnobs, "%s must return ErrInvalidRoutingKnobs", tc.name)
		})
	}
}

// capturingHandler is a minimal slog.Handler that records every emitted
// record so tests can assert on WARN volume without parsing stderr.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingHandler) warnRecords() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

// withCapturedLogs swaps in a capturingHandler as the slog default for the
// duration of the test (observability.Get() reads slog.Default()) and
// restores the previous default on cleanup.
func withCapturedLogs(t *testing.T) *capturingHandler {
	t.Helper()
	h := &capturingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// TestExtremeRoutingKnobWarnOnlyOnOverride is the regression test for the
// "extreme routing knob" WARN spam finding: a bundle is allowed to ship an
// alpha > 0.9 on some clusters by design (e.g. v0.70), and that must never
// WARN on a default request -- only a request that actually overrides a
// value into extreme territory should.
func TestExtremeRoutingKnobWarnOnlyOnOverride(t *testing.T) {
	// Bundle default alpha is already > 0.9 on both clusters, mirroring the
	// deployed v0.70 bundle shape that triggered the original spam.
	extremeDefaults := &DefaultRoutingKnobs{
		Alpha:                []float64{0.95, 0.95},
		SpeedWeight:          0.0,
		OutputCostRatio:      0.0,
		ExpectedOutputTokens: 2000,
		PerModelVerbosity:    false,
	}

	t.Run("bundle's own extreme defaults never warn", func(t *testing.T) {
		emb := &fakeEmbedder{vec: makeOpusVec()}
		scorer := newV2BundleForTest(t, emb, v2BundleOpts{defaultKnobs: extremeDefaults})
		handler := withCapturedLogs(t)

		_, err := scorer.Route(context.Background(), router.Request{PromptText: "test"})
		require.NoError(t, err)

		assert.Empty(t, handler.warnRecords(), "bundle's own baked-in defaults must not produce an extreme-knob WARN")
	})

	t.Run("request override into extreme territory still warns, aggregated", func(t *testing.T) {
		emb := &fakeEmbedder{vec: makeOpusVec()}
		// Start from a non-extreme default so the override is unambiguously
		// the thing that pushes both clusters over the threshold.
		scorer := newV2BundleForTest(t, emb, v2BundleOpts{})
		handler := withCapturedLogs(t)

		alphaVal := 0.99
		_, err := scorer.Route(context.Background(), router.Request{
			PromptText: "test",
			RoutingKnobs: &router.Overrides{
				Alpha: &alphaVal,
			},
		})
		require.NoError(t, err)

		// alpha=0.99 trips both the alpha>0.9 check and the
		// alpha+speed_weight>0.95 check, but each check aggregates across
		// clusters into a single line -- so 2 clusters overridden into
		// extreme territory produces exactly 2 WARN lines (one per check),
		// not 4 (one per cluster per check).
		warns := handler.warnRecords()
		require.Len(t, warns, 2, "each extreme-knob check must aggregate across clusters into a single WARN line")
		for _, w := range warns {
			assert.Contains(t, w.Message, "Extreme routing knob override")
		}
	})
}

// The deployment cost cap is a policy layer over the calibrated blend: a tight
// cap routes the cheapest deployed lane, an uncapped run reproduces the
// uncapped blend argmax, and a cap that would strand every candidate falls
// back to the full candidate set instead of failing the turn.
func TestScorer_CostCapRestrictsToCheapestLane(t *testing.T) {
	emb := &fakeEmbedder{vec: makeOpusVec()}
	hardClusterKnobs := &DefaultRoutingKnobs{
		Alpha:           []float64{0.9, 0.9},
		SpeedWeight:     0.0,
		OutputCostRatio: 0.0,
	}
	capNone := 0.0
	capHaikuOnly := 1.0000001 // Haiku's cost exactly; Opus (60x) excluded
	capUnlimited := 1e9

	// Uncapped baseline: cluster-0-aligned Opus wins on a quality-leaning blend.
	uncapped, err := newV2BundleForTest(t, emb, v2BundleOpts{
		defaultKnobs:    hardClusterKnobs,
		costCapMultiple: &capNone,
	}).Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-7", uncapped.Model, "precondition: uncapped blend picks Opus")

	// Tiny cap: only Haiku (input $0.00025/1K vs Opus $0.015/1K) is within budget.
	tiny, err := newV2BundleForTest(t, emb, v2BundleOpts{
		defaultKnobs:    hardClusterKnobs,
		costCapMultiple: &capHaikuOnly,
	}).Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", tiny.Model, "a tiny cap must force the cheapest lane")
	require.NotNil(t, tiny.Metadata)
	assert.NotEmpty(t, tiny.Metadata.CandidateModels, "candidate set must never be empty")

	// Huge cap: the cap admits every candidate, so the decision is identical to
	// the uncapped blend argmax.
	huge, err := newV2BundleForTest(t, emb, v2BundleOpts{
		defaultKnobs:    hardClusterKnobs,
		costCapMultiple: &capUnlimited,
	}).Route(context.Background(), router.Request{PromptText: "test"})
	require.NoError(t, err)
	assert.Equal(t, uncapped.Model, huge.Model, "a huge cap must reproduce the uncapped blend argmax")
	assert.Equal(t, uncapped.Metadata.ChosenScore, huge.Metadata.ChosenScore, "a huge cap must not change scores")

	// Excluding every model the cap keeps must still route on what remains,
	// never fail.
	excluded := &router.Request{PromptText: "test"}
	excluded.ExcludedModels = map[string]struct{}{"claude-haiku-4-5": {}}
	afterExclusion, err := newV2BundleForTest(t, emb, v2BundleOpts{
		defaultKnobs:    hardClusterKnobs,
		costCapMultiple: &capHaikuOnly,
	}).Route(context.Background(), *excluded)
	require.NoError(t, err)
	require.NotEmpty(t, afterExclusion.Metadata.CandidateModels, "candidate set must never be empty")
	assert.Equal(t, "claude-opus-4-7", afterExclusion.Model, "excluding the cheap lane routes on the remaining candidate")
}

// CostCapModeCluster vs CostCapModeGlobal: the same cap multiple, priced two
// different ways, routes differently on the roster shape the mode exists for —
// a premium lane that wins a cluster's uncapped blend while the cheap lane wins
// the roster's other clusters.
//
// Scenario (two lanes, three clusters, TopP=2 so the prompt's blend covers two
// premium-electing clusters):
//
//	premium $0.012/1k — leads clusters 0 and 1 on quality (12x the cheap lane)
//	cheap   $0.001/1k — leads cluster 2 on quality
//
// The prompt vector is equidistant from all three one-hot centroids, so TopP=2
// picks clusters 0 and 1 (ties broken by the lower cluster index) — the two the
// premium lane wins — and the uncapped blend therefore elects premium.
//
// At the production knee capMultiple=3.0 the two modes disagree:
//   - global prices ONE deployment-wide budget against the cheapest eligible
//     lane, so the premium lane is out of budget on every cluster and the cheap
//     lane routes instead;
//   - cluster prices each cluster's budget against the cheapest model THAT
//     cluster elects — clusters 0 and 1 elect premium, so premium sits inside
//     their budgets, and the blend's own winner routes.
//
// Cluster mode is a strict widening: the global set is the union's first term,
// so the cheap lane stays routable in both modes and the premium lane is the
// only thing the budget shape decides.
func TestScorer_CostCapClusterModeAdmitsClusterElectedLanes(t *testing.T) {
	const (
		premiumModel = "claude-opus-4-7"  // $0.012/1k
		cheapModel   = "claude-haiku-4-5" // $0.001/1k
		clusters     = 3
	)
	topP := 2
	premiumIn, cheapIn := 0.012, 0.001
	ttft, tps := 0.2, 100.0
	axes := map[string]ModelAxis{
		premiumModel: {InputPer1KUSD: &premiumIn, TTFTSeconds: &ttft, TPS: &tps},
		cheapModel:   {InputPer1KUSD: &cheapIn, TTFTSeconds: &ttft, TPS: &tps},
	}
	// The premium lane owns clusters 0 and 1; the cheap lane owns cluster 2.
	// Cluster 2 is still a legitimate cluster of the bundle — it is simply not
	// in this prompt's top-P, which is what makes the cheap lane routable only
	// via the shared deployment-wide budget term.
	quality := Rankings{
		0: {premiumModel: 0.9, cheapModel: 0.1},
		1: {premiumModel: 0.9, cheapModel: 0.1},
		2: {cheapModel: 0.9, premiumModel: 0.1},
	}
	// Quality-led blend (alpha 0.9) with the cost term priced at input price
	// only (output_cost_ratio 0, verbosity off), so the 12x cost relationship is
	// exact and the 3x budget math is exact.
	knobs := &DefaultRoutingKnobs{
		Alpha:                []float64{0.9, 0.9, 0.9},
		SpeedWeight:          0.0,
		OutputCostRatio:      0.0,
		ExpectedOutputTokens: 2000,
		PerModelVerbosity:    false,
	}

	emb := &fakeEmbedder{vec: weightedVec(EmbedDim, clusters, 1.0, 0.0)}
	capKnee := DefaultCostCapMultiple
	build := func(mode string, cap float64) *Scorer {
		c := cap
		return newV2BundleForTest(t, emb, v2BundleOpts{
			qualityMeans:    quality,
			modelAxes:       axes,
			defaultKnobs:    knobs,
			clusterCount:    clusters,
			topP:            &topP,
			costCapMode:     mode,
			costCapMultiple: &c,
		})
	}

	req := router.Request{PromptText: "test"}

	// Uncapped baseline: the blend elects the premium lane on these two clusters.
	uncapped, err := build("", 0).Route(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, premiumModel, uncapped.Model, "precondition: the uncapped blend elects the premium lane")

	global, err := build(CostCapModeGlobal, capKnee).Route(context.Background(), req)
	require.NoError(t, err)
	clusterMode, err := build(CostCapModeCluster, capKnee).Route(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, global.Metadata)
	require.NotNil(t, clusterMode.Metadata)

	// The observable difference at the SAME cap multiple: the deployment-wide
	// budget compares the premium lane against the cheapest lane on the roster
	// and drops it; the per-cluster budgets compare it against the lane each
	// cluster elects, which IS the premium lane.
	assert.Equal(t, []string{cheapModel}, global.Metadata.CandidateModels,
		"global budget: only the cheapest lane is within 3x of it")
	assert.ElementsMatch(t, []string{cheapModel, premiumModel}, clusterMode.Metadata.CandidateModels,
		"cluster budget: the cheap lane stays in via the shared budget term, the premium lane re-enters on its own cluster")
	assert.Equal(t, cheapModel, global.Model, "global mode routes the cheap lane the budget admits")
	assert.Equal(t, premiumModel, clusterMode.Model, "cluster mode routes the blend's own premium winner")

	// The cap is a candidate-set policy: it never touches the scores behind it,
	// so the cluster-mode decision carries the uncapped blend's score.
	assert.Equal(t, uncapped.Metadata.ChosenScore, clusterMode.Metadata.ChosenScore,
		"the cap widens the candidate set, never the scores behind it")
}

// weightedVec builds an embed vector with first on the first count dims and
// rest on the rest, so a bundle whose centroids are one-hot in those dims sees
// equal similarity and topPNearest's tie-break (lowest cluster index) decides
// which clusters make the prompt's top-P.
func weightedVec(dim, count int, first, rest float32) []float32 {
	v := make([]float32, dim)
	for i := range dim {
		if i < count {
			v[i] = first
			continue
		}
		v[i] = rest
	}
	return v
}
