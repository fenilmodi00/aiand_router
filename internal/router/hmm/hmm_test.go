package hmm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

type fakeDecider struct {
	query Query
	res   Result
	err   error
	calls int
}

func (f *fakeDecider) Decide(_ context.Context, q Query) (Result, error) {
	f.calls++
	f.query = q
	return f.res, f.err
}

func TestRouterMapsSidecarRosterModelBackToCatalogDecision(t *testing.T) {
	decider := &fakeDecider{res: Result{
		RouteID:              "route-1",
		Model:                "moonshotai/kimi-k2.7-code",
		Provider:             providers.ProviderAIAND,
		Score:                0.8,
		CandidateScores:      map[string]float32{"moonshotai/kimi-k2.7-code": 0.8},
		Reason:               "policy",
		Propensity:           0.9,
		DisplayMarker:        "display marker",
		PolicyLabel:          "short_turn",
		PolicyGroup:          "standard",
		PolicyRouteKey:       "standard|open",
		PolicyArtifactID:     "hmm-prod",
		PolicyArtifactSHA256: "sha256:abc",
		RosterVersion:        "roster-v2",
		SchemaVersion:        "policy_router_v1",
		DebugRef:             "debug-1",
	}}
	deployed := map[string]struct{}{"moonshotai/kimi-k2.7": {}}
	available := map[string]struct{}{providers.ProviderAIAND: {}}
	r := newWithRoutingTargets(router.StrategyHMM, decider, deployed, available)

	decision, err := r.Route(context.Background(), router.Request{
		OrganizationID: "org-1",
		InstallationID: "installation-1",
		ClientApp:      "codex",
		RolloutID:      "rollout-1",
		PromptText:     "hello",
		ConversationMessages: []router.ConversationMessage{{
			Role: "user",
			Text: "latest hello",
		}},
		EstimatedInputTokens: 10,
		DebugEnabled:         true,
		FeedbackKey:          "feedback-session",
		FeedbackRole:         "default",
		ClientSessionID:      "client-session-abc",
	})

	require.NoError(t, err)
	assert.Equal(t, "moonshotai/kimi-k2.7", decision.Model)
	assert.NotNil(t, decision.Metadata)
	assert.Equal(t, "display marker", decision.Metadata.DisplayMarker)
	assert.Equal(t, "route-1", decision.Metadata.RouteID)
	assert.Equal(t, "hmm", decision.Metadata.Strategy)
	assert.Equal(t, float32(0.9), decision.Metadata.Propensity)
	assert.Equal(t, "standard|open", decision.Metadata.PolicyRouteKey)
	assert.Equal(t, "hmm-prod", decision.Metadata.PolicyArtifactID)
	assert.Equal(t, "sha256:abc", decision.Metadata.PolicyArtifactSHA256)
	assert.Equal(t, "roster-v2", decision.Metadata.RosterVersion)
	assert.Equal(t, "policy_router_v1", decision.Metadata.SidecarSchemaVersion)
	assert.Equal(t, "debug-1", decision.Metadata.DebugRef)
	assert.Equal(t, map[string]float32{"moonshotai/kimi-k2.7": 0.8}, decision.Metadata.CandidateScores)
	assert.Equal(t, "hello", decider.query.PromptText)
	assert.Equal(t, router.StrategyHMM, decider.query.Strategy)
	assert.Equal(t, "org-1", decider.query.OrganizationID)
	assert.Equal(t, "installation-1", decider.query.InstallationID)
	assert.Equal(t, "codex", decider.query.ClientApp)
	assert.Equal(t, "rollout-1", decider.query.RolloutID)
	assert.Equal(t, "feedback-session", decider.query.FeedbackKey)
	assert.Equal(t, "default", decider.query.FeedbackRole)
	assert.Equal(t, "client-session-abc", decider.query.ClientSessionID)
	assert.Equal(t, []router.ConversationMessage{{Role: "user", Text: "latest hello"}}, decider.query.ConversationMessages)
	require.Len(t, decider.query.Candidates, 1)
	candidate := decider.query.Candidates[0]
	assert.Equal(t, "moonshotai/kimi-k2.7-code", candidate.RosterID)
	assert.Equal(t, "moonshotai/kimi-k2.7", candidate.CatalogID)
	assert.Equal(t, providers.ProviderAIAND, candidate.Provider)
	assert.Equal(t, 0.75, candidate.InputUSDPer1M)
	assert.Equal(t, 3.5, candidate.OutputUSDPer1M)
	assert.InDelta(t, 0.0000075, candidate.EstimatedCostUSD, 1e-12)
	assert.Equal(t, 262144, candidate.Capabilities.ContextWindow)
	assert.Equal(t, "high", candidate.Capabilities.Tier)
	assert.True(t, candidate.Capabilities.SupportsTools)
	assert.False(t, candidate.Capabilities.SupportsImages)
}

func TestRouterUsesSeparatelySelectableHMMStrategies(t *testing.T) {
	for _, strategy := range []router.Strategy{router.StrategyHMMEmbedding, router.StrategyHMMBeta} {
		t.Run(string(strategy), func(t *testing.T) {
			decider := &fakeDecider{res: Result{Model: "moonshotai/kimi-k2.7-code"}}
			r := newWithRoutingTargets(
				strategy,
				decider,
				map[string]struct{}{"moonshotai/kimi-k2.7": {}},
				map[string]struct{}{providers.ProviderAIAND: {}},
			)

			decision, err := r.Route(context.Background(), router.Request{PromptText: "hello"})

			require.NoError(t, err)
			assert.Equal(t, strategy, decider.query.Strategy)
			require.NotNil(t, decision.Metadata)
			assert.Equal(t, string(strategy), decision.Metadata.Strategy)
			assert.Contains(t, decision.Reason, "hmm_policy")
		})
	}
}

func TestRouterKeepsGeneratedRouteIDWhenSidecarOmitsIt(t *testing.T) {
	decider := &fakeDecider{res: Result{
		Model: "moonshotai/kimi-k2.7-code",
	}}
	r := newWithRoutingTargets(router.StrategyHMM, decider, map[string]struct{}{"moonshotai/kimi-k2.7": {}}, map[string]struct{}{providers.ProviderAIAND: {}})

	decision, err := r.Route(context.Background(), router.Request{PromptText: "hello"})

	require.NoError(t, err)
	require.NotNil(t, decision.Metadata)
	assert.NotEmpty(t, decider.query.RouteID)
	assert.Equal(t, decider.query.RouteID, decision.Metadata.RouteID)
}

func TestRouterFailsClosedOnUnknownReturnedModel(t *testing.T) {
	decider := &fakeDecider{res: Result{Model: "unknown/model"}}
	r := newWithRoutingTargets(router.StrategyHMM, decider, map[string]struct{}{"moonshotai/kimi-k2.7": {}}, map[string]struct{}{providers.ProviderAIAND: {}})

	_, err := r.Route(context.Background(), router.Request{PromptText: "hello"})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHMMUnavailable)
}

func TestRouterFailsClosedOnUnknownRosterModel(t *testing.T) {
	// The policy names a roster arm that is not in the candidate set: the
	// router must fail closed rather than dispatch it.
	decider := &fakeDecider{res: Result{Model: "not/a-real-roster-id", Provider: providers.ProviderAIAND}}
	r := newWithRoutingTargets(router.StrategyHMM, decider, map[string]struct{}{"moonshotai/kimi-k2.7": {}}, map[string]struct{}{providers.ProviderAIAND: {}})

	_, err := r.Route(context.Background(), router.Request{PromptText: "hello"})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHMMUnavailable)
}

func TestRouterDoesNotDispatchModelsOutsideTheCatalog(t *testing.T) {
	// The policy names a model with no catalog row: the router must not
	// dispatch it, and must fail closed.
	decider := &fakeDecider{res: Result{Model: "minimax/minimax-m3"}}
	r := New(
		decider,
		map[string]struct{}{providers.ProviderAIAND: {}},
	)

	for _, candidate := range r.resolver.Resolve(router.Request{}).Candidates {
		assert.NotEqual(t, "minimax/minimax-m3", candidate.CatalogID)
	}

	_, err := r.Route(context.Background(), router.Request{PromptText: "hello"})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHMMUnavailable)
}

func TestCatalogRoutingTargetsResolveCurrentHMMRosterArmsToProviders(t *testing.T) {
	available := map[string]struct{}{
		providers.ProviderAIAND: {},
	}
	r := New(&fakeDecider{}, available)

	candidates := r.resolver.Resolve(router.Request{}).Candidates

	gotRosterIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		assert.Equal(t, providers.ProviderAIAND, candidate.Provider, candidate.RosterID)
		gotRosterIDs = append(gotRosterIDs, candidate.RosterID)
	}
	for _, rosterID := range []string{
		"deepseek/deepseek-v4.1-flash",
		"deepseek/deepseek-v4-pro-0813",
		"moonshotai/kimi-k2.7-code",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.2",
		"z-ai/glm-5.3",
		"z-ai/glm-5.3-flash",
	} {
		assert.Contains(t, gotRosterIDs, rosterID)
	}
}

// TestRosterIDForKeepsSlashFormCatalogIDs: every surviving catalog model is
// slash-form, so its roster identity is the model ID itself.
func TestRosterIDForKeepsSlashFormCatalogIDs(t *testing.T) {
	for _, id := range []string{
		"deepseek/deepseek-v4.1-flash",
		"deepseek/deepseek-v4-pro-0813",
		"moonshotai/kimi-k2.7",
		"moonshotai/kimi-k3",
		"z-ai/glm-5.2",
		"z-ai/glm-5.3",
		"z-ai/glm-5.3-flash",
	} {
		m, ok := catalog.ByID(id)
		require.True(t, ok, id)
		if id == "moonshotai/kimi-k2.7" {
			// The policy roster knows Kimi K2.7 under its "-code" build.
			assert.Equal(t, "moonshotai/kimi-k2.7-code", rosterIDFor(m), id)
			assert.Equal(t, "moonshotai/kimi-k2.7", CatalogIDForRoster("moonshotai/kimi-k2.7-code"), id)
			continue
		}
		assert.Equal(t, id, rosterIDFor(m), id)
		assert.Equal(t, id, CatalogIDForRoster(id), id)
	}

	// Kimi K2.7 is the one divergence: the policy roster knows the "-code"
	// build, and the reverse mapping must land back on the catalog ID the
	// dispatch path consumes.
	kimi, ok := catalog.ByID("moonshotai/kimi-k2.7")
	require.True(t, ok)
	assert.Equal(t, "moonshotai/kimi-k2.7-code", rosterIDFor(kimi))
	assert.Equal(t, "moonshotai/kimi-k2.7", CatalogIDForRoster("moonshotai/kimi-k2.7-code"))
}

func TestRouterOffersAndSelectsKimiK3WithoutLegacyDeployedSet(t *testing.T) {
	decider := &fakeDecider{res: Result{
		Model:    "moonshotai/kimi-k3",
		Provider: providers.ProviderAIAND,
	}}
	r := New(decider, map[string]struct{}{providers.ProviderAIAND: {}})

	decision, err := r.Route(context.Background(), router.Request{PromptText: "solve this"})

	require.NoError(t, err)
	assert.Equal(t, "moonshotai/kimi-k3", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.Contains(t, candidateRosterIDs(decider.query.Candidates), "moonshotai/kimi-k3")
}



func TestRouterDoesNotOfferTerraWithoutRegisteredOpenAIProvider(t *testing.T) {
	r := New(&fakeDecider{}, map[string]struct{}{providers.ProviderAIAND: {}})

	candidates := r.resolver.Resolve(router.Request{}).Candidates

	assert.NotContains(t, candidateRosterIDs(candidates), "openai/gpt-5.6-terra")
}

func candidateRosterIDs(candidates []Candidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.RosterID)
	}
	return ids
}

func TestRosterIDForKeepsBareIDsVerbatim(t *testing.T) {
	got := rosterIDFor(catalog.Model{
		ID: "bare-provider-model",
		Providers: []catalog.ProviderBinding{{
			Provider: providers.ProviderAIAND,
		}},
	})

	assert.Equal(t, "bare-provider-model", got)
}

func TestIsToolExecutionResultRecognizesBothTaxonomies(t *testing.T) {
	// "explore" is the retired five-class label (roster_v2, still the pinned
	// prod package); "low" is its four-class successor (roster_v4) that the
	// retired explore cluster was merged into. Both must route as tool
	// execution during the migration window where either package can be
	// deployed.
	assert.True(t, isToolExecutionResult(Result{PolicyGroup: "explore"}))
	assert.True(t, isToolExecutionResult(Result{PolicyGroup: "low"}))
	assert.True(t, isToolExecutionResult(Result{PolicyGroup: "EXPLORE"}))
	assert.True(t, isToolExecutionResult(Result{PolicyGroup: " Low "}))
	assert.True(t, isToolExecutionResult(Result{PolicyLabel: "spawn_explore"}))
	assert.True(t, isToolExecutionResult(Result{PolicyLabel: "prefix_tool_call_suffix"}))
	assert.False(t, isToolExecutionResult(Result{PolicyGroup: "fast"}))
	assert.False(t, isToolExecutionResult(Result{PolicyGroup: "balanced"}))
	assert.False(t, isToolExecutionResult(Result{PolicyGroup: "medium"}))
	assert.False(t, isToolExecutionResult(Result{}))
}

func TestReasonForTagsToolExecutionPrefixForBothTaxonomies(t *testing.T) {
	assert.Equal(t, "hmm_policy:tool_execution(label=explore)", reasonFor(Result{PolicyGroup: "explore", PolicyLabel: "explore"}))
	assert.Equal(t, "hmm_policy:tool_execution(label=low)", reasonFor(Result{PolicyGroup: "low", PolicyLabel: "low"}))
	assert.Equal(t, "hmm_policy(label=balanced)", reasonFor(Result{PolicyGroup: "balanced", PolicyLabel: "balanced"}))
}
