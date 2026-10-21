package rl_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/rl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDecider records the query it received and returns a canned result/error.
type fakeDecider struct {
	got    rl.Query
	result rl.Result
	err    error
}

func (f *fakeDecider) Decide(_ context.Context, q rl.Query) (rl.Result, error) {
	f.got = q
	return f.result, f.err
}

func deployed(ids ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func enabled(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// allProviders is the deployment's keyed-provider set used to resolve dispatch
// bindings on the unrestricted (nil EnabledProviders) path.
var allProviders = enabled(providers.ProviderAIAND)

func TestRouteMapsRosterChoiceBackToCatalogModel(t *testing.T) {
	// The policy picks by OpenRouter-style roster ID; the router must dispatch
	// the corresponding catalog model via its own provider.
	dec := &fakeDecider{result: rl.Result{Model: "moonshotai/kimi-k3", Score: 1.5, ScoreLabel: "DPO score", StateLabel: "implementing"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "deepseek/deepseek-v4.1-flash"), allProviders)

	decision, err := r.Route(context.Background(), router.Request{
		PromptText:       "refactor the auth module",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.NoError(t, err)
	assert.Equal(t, "moonshotai/kimi-k3", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.Contains(t, decision.Reason, "DPO score")
	assert.Contains(t, decision.Reason, "implementing")

	// The deepseek slash-form id passes through unchanged; the dotted/dashed
	// first-party slug is what the policy was offered for opus.
	rosterIDs := make(map[string]string, len(dec.got.Candidates))
	for _, c := range dec.got.Candidates {
		rosterIDs[c.RosterID] = c.Provider
	}
	assert.Equal(t, providers.ProviderAIAND, rosterIDs["moonshotai/kimi-k3"])
	assert.Equal(t, providers.ProviderAIAND, rosterIDs["deepseek/deepseek-v4.1-flash"])
}

func TestRouteOmitsModelsWithNoEnabledProvider(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "moonshotai/kimi-k3"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "deepseek/deepseek-v4.1-flash"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.NoError(t, err)
	// ai& is the only upstream, so every deployed model resolves through it
	// and the policy is offered all of them.
	assert.Len(t, dec.got.Candidates, 2)
}

func TestRouteExcludesRequestedExclusions(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "deepseek/deepseek-v4.1-flash"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "deepseek/deepseek-v4.1-flash"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
		ExcludedModels:   map[string]struct{}{"moonshotai/kimi-k3": {}},
	})
	require.NoError(t, err)
	for _, c := range dec.got.Candidates {
		assert.NotEqual(t, "moonshotai/kimi-k3", c.RosterID)
	}
}

func TestRouteNoEligibleCandidatesIsUnavailable(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "moonshotai/kimi-k3"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled("other-provider"), // no binding resolves
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
}

func TestRouteDeciderErrorIsUnavailable(t *testing.T) {
	dec := &fakeDecider{err: errors.New("sidecar down")}
	r := rl.New(dec, deployed("moonshotai/kimi-k3"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
}

func TestRouteNilEnabledProvidersIsUnrestricted(t *testing.T) {
	// nil EnabledProviders means "unrestricted" (router.Request contract); the
	// policy must still be offered the deployed models via their primary
	// provider, not an empty set.
	dec := &fakeDecider{result: rl.Result{Model: "moonshotai/kimi-k3"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "deepseek/deepseek-v4.1-flash"), allProviders)

	decision, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: nil,
	})
	require.NoError(t, err)
	assert.Equal(t, "moonshotai/kimi-k3", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.NotEmpty(t, dec.got.Candidates, "nil providers must not empty the candidate set")
}

func TestRouteToolTurnDropsToolUseLowFromCandidatesAndIndex(t *testing.T) {
	// qwen/qwen3-235b-a22b-2507 is ToolUseLow. On a tool turn it must be absent
	// from BOTH the offered candidates and the response-mapping index — a
	// sidecar that names it anyway is rejected, not dispatched.
	dec := &fakeDecider{result: rl.Result{Model: "qwen/qwen3-235b-a22b-2507"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "qwen/qwen3-235b-a22b-2507"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "use a tool",
		HasTools:         true,
		EnabledProviders: nil,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
	for _, c := range dec.got.Candidates {
		assert.NotEqual(t, "qwen/qwen3-235b-a22b-2507", c.RosterID,
			"ToolUseLow model must not be offered on a tool turn")
	}
}

func TestRouteImageTurnDropsImageUnsupported(t *testing.T) {
	// Qwen Coder Next is text-only; Opus is vision-capable.
	// An image turn must drop the text-only model when a capable one survives.
	dec := &fakeDecider{result: rl.Result{Model: "moonshotai/kimi-k3"}}
	r := rl.New(dec, deployed("moonshotai/kimi-k3", "qwen/qwen3-coder-next"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "what is in this image",
		HasImages:        true,
		EnabledProviders: nil,
	})
	require.NoError(t, err)
	for _, c := range dec.got.Candidates {
		assert.NotEqual(t, "qwen/qwen3-coder-next", c.RosterID,
			"image-unsupported model must not be offered on an image turn")
	}
}

func TestRouteUnknownReturnedModelIsUnavailable(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "openai/gpt-5.5"}} // never offered
	r := rl.New(dec, deployed("moonshotai/kimi-k3"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
}
