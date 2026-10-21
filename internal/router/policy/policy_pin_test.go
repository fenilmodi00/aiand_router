package policy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

var (
	pinnedArtifactSHA = strings.Repeat("a", 64)
	pinnedRosterSHA   = strings.Repeat("b", 64)
	otherRosterSHA    = strings.Repeat("c", 64)
)

func honouredPinContext() context.Context {
	return router.WithPolicyPinRequest(context.Background(), router.PolicyPinRequest{
		Pin:        router.PolicyPin{ArtifactSHA256: pinnedArtifactSHA, RosterSHA256: pinnedRosterSHA},
		Authorized: true,
	})
}

func newPinnedAdapter(t *testing.T, servedArtifactSHA string) (*policy.SidecarRouter, *recordingPolicy) {
	t.Helper()
	result := classifierOnlyResult()
	result.PolicyArtifactSHA256 = servedArtifactSHA
	result.RosterVersion = "sidecar-roster"
	decider := &recordingPolicy{result: result}
	resolver := policy.NewResolver(
		set("claude-opus-4-8", "claude-sonnet-5"),
		set(providers.ProviderAIAND),
		func(model catalog.Model) string { return "anthropic/" + model.ID },
		policy.ManagedProviderPolicy(),
	)
	adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{
		Strategy:    router.StrategyHMM,
		Unavailable: errors.New("selection unavailable"),
	}, decider, resolver)
	return adapter, decider
}

// rosterSelector fakes a boot-loaded roster set holding exactly the digests
// given; the first is the default served when no roster is pinned.
func rosterSelector(loaded ...string) policy.ArmSelector {
	return func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		for _, sha := range loaded {
			if sha == input.RosterSHA256 || input.RosterSHA256 == "" {
				return policy.SelectionPick{Group: "maximum", Arm: "anthropic/claude-sonnet-5", RosterSHA256: sha}, nil
			}
		}
		return policy.SelectionPick{}, router.ErrPolicyPinUnavailable
	}
}

func TestPolicyPinPreviewCarriesArtifactAndRejectsMismatch(t *testing.T) {
	adapter, decider := newPinnedAdapter(t, pinnedArtifactSHA)
	decider.preview = policy.PreviewResult{SchemaVersion: policy.SchemaVersionV3, PolicyArtifactSHA256: strings.Repeat("d", 64)}

	_, err := adapter.PreviewRoute(honouredPinContext(), router.Request{})

	assert.Equal(t, pinnedArtifactSHA, decider.previewQuery.ArtifactSHA256)
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}
