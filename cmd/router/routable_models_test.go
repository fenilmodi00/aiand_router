package main

import (
	"testing"

	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyRoutableModels_WithoutHMMLeavesGenericSet(t *testing.T) {
	generic := map[string]struct{}{"claude-opus-4-7": {}}
	got := proxyRoutableModels(generic, map[string]struct{}{"anthropic": {}}, false)
	assert.Equal(t, generic, got)
}

func TestProxyRoutableModels_WithHMMUnionsHMMOnlyTargets(t *testing.T) {
	providers := map[string]struct{}{}
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			providers[b.Provider] = struct{}{}
		}
	}
	generic := catalog.RoutingTargetSet(providers)
	hmm := catalog.HMMRoutingTargetSet(providers)
	// The AIand-only cut removed the HMM-only catalog rows (the OpenAI Pi
	// aliases), so on this roster the union currently equals the generic set.
	// The union itself is still pinned here: when an HMM-only row returns it
	// must widen the set, and this assertion catches a regression that
	// shrinks or mismatches it.
	require.GreaterOrEqual(t, len(hmm), len(generic))

	got := proxyRoutableModels(generic, providers, true)
	assert.Equal(t, len(hmm), len(got))
	for id := range hmm {
		assert.Contains(t, got, id)
	}
}
