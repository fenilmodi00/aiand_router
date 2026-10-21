package proxy

import (
	"strings"
	"testing"

	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bare-name table must stay unambiguous as models are added: a tail shared
// by two models, or one that collides with a full ID or an alias, would make a
// bare name resolve to an arbitrary winner — the silent-wrong-model failure
// exact matching exists to prevent. Such tails are dropped, not guessed.
func TestBareCatalogNames_Unambiguous(t *testing.T) {
	tails := make(map[string][]string)
	for _, m := range catalog.Models {
		if _, tail, ok := strings.Cut(m.ID, "/"); ok && len(m.Providers) > 0 {
			tails[tail] = append(tails[tail], m.ID)
		}
	}

	for tail, owners := range tails {
		mapped, listed := bareCatalogNames[tail]
		_, isFullID := catalog.ByID(tail)
		_, aliased := forceModelAliases[tail]

		if len(owners) > 1 || isFullID || aliased {
			assert.False(t, listed,
				"ambiguous tail %q (owners=%v full_id=%v aliased=%v) must not be a bare name",
				tail, owners, isFullID, aliased)
			continue
		}
		require.True(t, listed, "unambiguous tail %q must be reachable without its vendor prefix", tail)
		assert.Equal(t, owners[0], mapped)
	}

	// Every entry must name a real, servable model.
	for tail, id := range bareCatalogNames {
		m, ok := catalog.ByID(id)
		require.True(t, ok, "bare name %q maps to unknown model %q", tail, id)
		assert.NotEmpty(t, m.Providers, "bare name %q maps to unservable model %q", tail, id)
	}
}

// An alias must win over a bare catalog name, so a deliberate alias can never
// be shadowed by an incidental tail collision.
func TestBareCatalogNames_AliasesTakePrecedence(t *testing.T) {
	for alias := range forceModelAliases {
		_, shadowed := bareCatalogNames[alias]
		assert.False(t, shadowed, "alias %q must not also be a bare-name entry", alias)
	}
}

// Rejected prod inputs that name no model must stay rejected: folding fixes
// spelling, it never guesses.
func TestResolveForceModel_ObservedUnknowns(t *testing.T) {
	for _, input := range []string{
		"opsu", "opuis", "opues-5.5", "gtp-6-sol", "sool", "sokl", "astar",
		"gpt-61.-sol", "sol.", "opus-5-", "kimi-k", "gpt-6-1", "gpt6-1",
		"gpt-5.6-astra", "codex-sol-6", "sol-6", "gpt-sol-6", "/force-model sol",
		"openai/gpt-oss-120b", "qwen/qwen3-32b", "gpt-", "gpt-5 help me debug this",
		"opus high help",
	} {
		t.Run(input, func(t *testing.T) {
			_, _, gotKnown, _ := resolveForceModelWithEffort(input)
			assert.False(t, gotKnown, "known")
		})
	}
}

// Folding must never let one spelling reach two models. Every accepted name
// whose folded spelling is shared only with names for the same model stays
// reachable; a fold shared across models is dropped.
func TestFoldedForceModelNames_Unambiguous(t *testing.T) {
	targets := make(map[string]map[string]bool)
	for name, id := range forceModelNames() {
		key := foldModelSpelling(name)
		if targets[key] == nil {
			targets[key] = make(map[string]bool)
		}
		targets[key][id] = true
	}
	for key, ids := range targets {
		got, listed := foldedForceModelNames[key]
		if len(ids) > 1 {
			assert.False(t, listed, "folded key %q is shared by %v and must not resolve", key, ids)
			continue
		}
		require.True(t, listed, "folded key %q must resolve", key)
		assert.True(t, ids[got], "folded key %q resolves to %q", key, got)
	}
}

// Every name an exact lookup accepts must resolve to the same model through
// its folded spelling, so folding can only add reach, never redirect it.
func TestFoldedForceModelNames_AgreeWithExactNames(t *testing.T) {
	for name, id := range forceModelNames() {
		if folded, ok := foldedForceModelNames[foldModelSpelling(name)]; ok {
			assert.Equal(t, id, folded, "name %q", name)
		}
	}
}
