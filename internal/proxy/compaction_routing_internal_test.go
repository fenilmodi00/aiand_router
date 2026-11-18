package proxy

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/cluster"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
)

const (
	compactionScoredModel = "z-ai/glm-5.3"
	codexCompactionBody   = `{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."}]}`
	claudeCompactionBody  = `{"model":"z-ai/glm-5.3","max_tokens":20000,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`
)

type compactionScorer struct {
	decision router.Decision
	calls    *atomic.Int32
}

func (r compactionScorer) Route(context.Context, router.Request) (router.Decision, error) {
	r.calls.Add(1)
	return r.decision, nil
}

type compactionRoutingFixture struct {
	svc            *Service
	store          *forceModelMapStore
	scorerCalls    *atomic.Int32
	installationID uuid.UUID
}

// The catalog utility hard pin stands in for the generic resolver;
// compaction must never reach it.
func newCompactionRoutingFixture(t *testing.T, scored router.Decision) compactionRoutingFixture {
	t.Helper()
	calls := &atomic.Int32{}
	store := newForceModelMapStore()
	svc := NewService(compactionScorer{decision: scored, calls: calls},
		map[string]providers.Client{providers.ProviderAIAND: nil},
		nil, false, nil, store, false, providers.ProviderAIAND, policy.PrecompactionDefaultModel, nil).
		WithHardPinResolver(CatalogUtilityHardPin)
	return compactionRoutingFixture{svc: svc, store: store, scorerCalls: calls, installationID: uuid.New()}
}

func (f compactionRoutingFixture) run(t *testing.T, ctx context.Context, env *translate.RequestEnvelope, req router.Request) (turnLoopResult, error) {
	t.Helper()
	features := env.RoutingFeatures(false)
	req.RequestedModel = features.Model
	return f.svc.runTurnLoop(ctx, env, features, "test-key", f.installationID, "", nil, req)
}

func (f compactionRoutingFixture) pinSession(t *testing.T, ctx context.Context, env *translate.RequestEnvelope, decision router.Decision) {
	t.Helper()
	role := roleForTier(catalog.TierFor(env.RoutingFeatures(false).Model))
	f.svc.writeNewPin(ctx, f.installationID, deriveSessionKeyForRequest(ctx, env, "test-key"), role, decision)
}

func openAIBundleModels(t *testing.T) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == providers.ProviderAIAND {
				out[m.ID] = struct{}{}
			}
		}
	}
	return out
}

// A resumed Codex thread whose first request is a compaction has no session
// pin yet. With the cluster bundle's OpenAI models excluded and only OpenAI
// enabled, compaction used to fail with a retryable "cluster scorer failed"
// 503 on every retry instead of asking the scorer.
func TestCompactionWithoutSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderAIAND, Model: compactionScoredModel, Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	env, err := translate.ParseOpenAI([]byte(codexCompactionBody))
	require.NoError(t, err)

	turn, err := f.run(t, ctx, env, router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		Exclusions:       router.RequestedExclusions(openAIBundleModels(t)),
	})

	require.NoError(t, err)
	require.NotErrorIs(t, err, cluster.ErrClusterUnavailable)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, compactionScoredModel, turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
	assert.False(t, turn.HardPinned)
}

// Claude Code compaction without a pin takes the scorer's pick rather than a
// deployment-configured summarizer.
func TestClaudeCodeCompactionWithoutSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String(), Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})
	env, err := translate.ParseAnthropic([]byte(claudeCompactionBody))
	require.NoError(t, err)

	turn, err := f.run(t, ctx, env, router.Request{})

	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, catalog.ModelIDGLM53.String(), turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
}

// Every client's compaction stays on the model serving the conversation.
func TestCompactionStaysOnSessionPin(t *testing.T) {
	for _, test := range []struct {
		name   string
		client string
		body   string
		parse  func([]byte) (*translate.RequestEnvelope, error)
		pin    router.Decision
	}{
		{"codex", ClientAppCodex, codexCompactionBody, translate.ParseOpenAI,
			router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String()}},
		{"codex on a low-tier model", ClientAppCodex, codexCompactionBody, translate.ParseOpenAI,
			router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53Flash.String()}},
		{"claude code", ClientAppClaudeCode, claudeCompactionBody, translate.ParseAnthropic,
			router.Decision{Provider: providers.ProviderAIAND, Model: policy.PrecompactionLargeWindowModel}},
		{"claude code on another vendor", ClientAppClaudeCode, claudeCompactionBody, translate.ParseAnthropic,
			router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCompactionRoutingFixture(t, router.Decision{Provider: providers.ProviderAIAND, Model: compactionScoredModel})
			ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: test.client})
			env, err := test.parse([]byte(test.body))
			require.NoError(t, err)
			f.pinSession(t, ctx, env, test.pin)

			turn, err := f.run(t, ctx, env, router.Request{})

			require.NoError(t, err)
			assert.Equal(t, turntype.Compaction, turn.TurnType)
			assert.Equal(t, test.pin.Model, turn.Decision.Model)
			assert.Equal(t, test.pin.Provider, turn.Decision.Provider)
			assert.Equal(t, compactionScoredModel, turn.Fresh.Model, "the scorer still supplies rescue candidates")
		})
	}
}

// A pin the request can no longer use falls through to the scorer, not to the
// cluster utility model.
func TestCompactionIneligibleSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderAIAND, Model: compactionScoredModel, Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	env, err := translate.ParseOpenAI([]byte(codexCompactionBody))
	require.NoError(t, err)
	f.pinSession(t, ctx, env, router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String()})

	turn, err := f.run(t, ctx, env, router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderAIAND: {}},
		Exclusions:       router.RequestedExclusions(map[string]struct{}{catalog.ModelIDGLM53.String(): {}}),
	})

	require.NoError(t, err)
	assert.Equal(t, compactionScoredModel, turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
}
