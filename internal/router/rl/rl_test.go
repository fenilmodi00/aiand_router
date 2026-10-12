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
var allProviders = enabled(
	providers.ProviderAIAND,
)

func TestRouteMapsRosterChoiceBackToCatalogModel(t *testing.T) {
	// The policy picks by roster ID; the router must dispatch the
	// corresponding catalog model via its own provider.
	dec := &fakeDecider{result: rl.Result{Model: "deepseek-ai/deepseek-v4-pro", Score: 1.5, ScoreLabel: "DPO score", StateLabel: "implementing"}}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4.1-flash"), allProviders)

	decision, err := r.Route(context.Background(), router.Request{
		PromptText:       "refactor the auth module",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.NoError(t, err)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.Contains(t, decision.Reason, "DPO score")
	assert.Contains(t, decision.Reason, "implementing")

	// AIand's catalog IDs are already slash-form: the roster ID is the bare
	// catalog ID, resolved back to the AIand dispatch binding.
	rosterIDs := make(map[string]string, len(dec.got.Candidates))
	for _, c := range dec.got.Candidates {
		rosterIDs[c.RosterID] = c.Provider
	}
	assert.Equal(t, providers.ProviderAIAND, rosterIDs["deepseek-ai/deepseek-v4-pro"])
	assert.Equal(t, providers.ProviderAIAND, rosterIDs["deepseek-ai/deepseek-v4.1-flash"])
}

func TestRouteExcludesRequestedExclusions(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "deepseek-ai/deepseek-v4.1-flash"}}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4.1-flash"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
		ExcludedModels:   map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}},
	})
	require.NoError(t, err)
	for _, c := range dec.got.Candidates {
		assert.NotEqual(t, "deepseek-ai/deepseek-v4-pro", c.RosterID)
	}
}

func TestRouteNoEligibleCandidatesIsUnavailable(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "deepseek-ai/deepseek-v4-pro"}}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAnthropic), // no binding for the AIand model
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
}

func TestRouteDeciderErrorIsUnavailable(t *testing.T) {
	dec := &fakeDecider{err: errors.New("sidecar down")}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro"), allProviders)

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
	dec := &fakeDecider{result: rl.Result{Model: "deepseek-ai/deepseek-v4-pro"}}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4.1-flash"), allProviders)

	decision, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: nil,
	})
	require.NoError(t, err)
	assert.Equal(t, "deepseek-ai/deepseek-v4-pro", decision.Model)
	assert.Equal(t, providers.ProviderAIAND, decision.Provider)
	assert.NotEmpty(t, dec.got.Candidates, "nil providers must not empty the candidate set")
}

func TestRouteImageTurnDropsImageUnsupported(t *testing.T) {
	// motif-3 is text-only; deepseek-v4.1-flash is vision-capable.
	// An image turn must drop the text-only model when a capable one survives.
	dec := &fakeDecider{result: rl.Result{Model: "deepseek-ai/deepseek-v4.1-flash"}}
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4.1-flash", "motif-technologies/motif-3"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "what is in this image",
		HasImages:        true,
		EnabledProviders: nil,
	})
	require.NoError(t, err)
	for _, c := range dec.got.Candidates {
		assert.NotEqual(t, "motif-technologies/motif-3", c.RosterID,
			"image-unsupported model must not be offered on an image turn")
	}
}

func TestRouteUnknownReturnedModelIsUnavailable(t *testing.T) {
	dec := &fakeDecider{result: rl.Result{Model: "openai/gpt-5.5"}} // never offered
	r := rl.New(dec, deployed("deepseek-ai/deepseek-v4-pro"), allProviders)

	_, err := r.Route(context.Background(), router.Request{
		PromptText:       "hi",
		EnabledProviders: enabled(providers.ProviderAIAND),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, rl.ErrPolicyUnavailable))
}
