package proxy

import (
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveForceModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantID       string
		wantProvider string
		wantKnown    bool
	}{
		// Catalog matches: provider comes from the primary binding, even
		// when the model name doesn't follow the bare-prefix heuristic. These
		// resolve to a real catalog entry, so known is true.
		{
			name:         "catalog aiand — deepseek pro",
			input:        "deepseek-ai/deepseek-v4-pro",
			wantID:       "deepseek-ai/deepseek-v4-pro",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			// The Bedrock-bound qwen rows went with the provider cut; this is
			// an AIAND-served slash-form row, so the vendor-prefix path is
			// still exercised.
			name:         "catalog aiand — slash form",
			input:        "qwen/qwen3.8-27b",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			name:         "catalog aiand — bare suffix match",
			input:        "qwen3.8-27b",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		// The gpt/sol aliases went with their catalog rows in the AIand-only
		// cut; the names no longer resolve a servable model, so the pin is
		// rejected rather than served by something else.
		{
			name:         "retired alias gpt is not known",
			input:        "gpt",
			wantID:       "gpt",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "retired alias gpt hyphen minor version is not known",
			input:        "gpt-5-5",
			wantID:       "gpt-5-5",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "retired pinned sol is not known",
			input:        "gpt6sol",
			wantID:       "gpt6sol",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "retired alias sol is not known",
			input:        "sol",
			wantID:       "sol",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "native openai prefix on a retired model is not known",
			input:        "openai/gpt-5.6-luna",
			wantID:       "gpt-5.6-luna",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "native openai prefix with retired version alias is not known",
			input:        "openai/gpt-5.6",
			wantID:       "gpt-5.6",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "native openai prefix with retired model alias is not known",
			input:        "openai/luna",
			wantID:       "luna",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "native openai prefix rejects cross-provider alias",
			input:        "openai/claude",
			wantID:       "claude",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		// The claude/opus aliases were deleted with their rows, so the names
		// fall to the heuristic default arm and resolve no servable model.
		{
			name:         "retired alias claude is not known",
			input:        "claude",
			wantID:       "claude",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "retired alias opus is not known",
			input:        "opus",
			wantID:       "opus",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "retired alias opus dotted version is not known",
			input:        "opus-4.8",
			wantID:       "opus-4.8",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			// The gemini aliases were deleted with their rows, so a gemini-*
			// pin has no alias and no catalog match — it falls to the
			// heuristic default arm.
			name:         "alias mixed case and whitespace",
			input:        "  Gemini  ",
			wantID:       "gemini",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		// qwen aliases target the single surviving AIand roster row.
		{
			name:         "alias qwen",
			input:        "qwen",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen/qwen-3.8-max",
			input:        "qwen/qwen-3.8-max",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen-3.8-max",
			input:        "qwen-3.8-max",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			name:         "dash spelling qwen-3.8",
			input:        "qwen-3.8",
			wantID:       "qwen/qwen3.8-27b",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
		{
			name:         "gpt-6 alias is no longer known",
			input:        "gpt-6",
			wantID:       "gpt-6",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		// Heuristic fallback: not in the catalog, so known is false. The
		// provider is a best-effort guess for logging only; the handler rejects
		// these rather than pinning a model with no known tier.
		{
			name:         "heuristic openai — o3",
			input:        "o3",
			wantID:       "o3",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "heuristic aiand — unknown slash model",
			input:        "unknown-vendor/unknown-model",
			wantID:       "unknown-vendor/unknown-model",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "native openai gpt-6 alias is no longer known",
			input:        "openai/gpt-6",
			wantID:       "gpt-6",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "heuristic aiand — unknown bareword",
			input:        "totally-not-a-model",
			wantID:       "totally-not-a-model",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		// Truncated command (the bug this guard closes): "/force-model gpt-"
		// parses to "gpt-", which matches no catalog entry.
		{
			name:         "truncated gpt- is not known",
			input:        "gpt-",
			wantID:       "gpt-",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		// Matching is exact: a name that merely contains, or is contained by, a
		// real one is unknown. "qwen 3.8" is the reported bug — it used to
		// resolve through the bare "qwen" alias and silently serve qwen3-coder.
		{
			name:         "spaced model name is not known",
			input:        "qwen 3.8",
			wantID:       "qwen 3.8",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "spaced alias is not known",
			input:        "qwen max",
			wantID:       "qwen max",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			name:         "model name with a trailing prompt is not known",
			input:        "gpt-5 help me debug this",
			wantID:       "gpt-5 help me debug this",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			// A prefix of a real ID must not resolve to it. (Contrast
			// "claude-opus", which resolves only because it's an explicit
			// alias — deliberate shorthands still work, guesses don't.)
			name:         "prefix of a real id is not known",
			input:        "claude-sonnet-4",
			wantID:       "claude-sonnet-4",
			wantProvider: providers.ProviderAnthropic,
			wantKnown:    false,
		},
		{
			// The bare-name table is exact too: a tail fragment is not a tail.
			name:         "fragment of a bare name is not known",
			input:        "motif",
			wantID:       "motif",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    false,
		},
		{
			// The vendor prefix stays optional via an exact bare-name entry.
			name:         "bare name of a slash-form model",
			input:        "deepseek-v4.1-flash",
			wantID:       "deepseek-ai/deepseek-v4.1-flash",
			wantProvider: providers.ProviderAIAND,
			wantKnown:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotProvider, gotKnown := resolveForceModel(tt.input)
			assert.Equal(t, tt.wantID, gotID, "canonical id")
			assert.Equal(t, tt.wantProvider, gotProvider, "provider")
			assert.Equal(t, tt.wantKnown, gotKnown, "known")
		})
	}
}

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

// The grok/xai family aliases were deleted with the grok rows in the
// AIand-only cut, so the names now fall to the heuristic default arm and
// resolve no servable model — the pin is rejected.
func TestResolveForceModel_GrokFamilyAlias(t *testing.T) {
	for _, input := range []string{"grok", "xai"} {
		t.Run(input, func(t *testing.T) {
			gotID, gotProvider, gotKnown := resolveForceModel(input)
			assert.Equal(t, input, gotID, "canonical id")
			assert.Equal(t, providers.ProviderAIAND, gotProvider, "provider")
			assert.False(t, gotKnown, "known")
		})
	}
}

// An explicit :level suffix must survive resolution to its catalog model.
func TestResolveForceModel_EffortSuffixPreserved(t *testing.T) {
	gotID, _, gotKnown, gotEffort := resolveForceModelWithEffort("zai-org/glm-5.3:medium")
	assert.Equal(t, "zai-org/glm-5.3", gotID, "canonical id")
	assert.True(t, gotKnown, "known")
	assert.Equal(t, "medium", gotEffort, "effort")
}
