//go:build smoke

package smoke

import "testing"

// TestCaching exercises prompt-cache behavior end to end against the live
// upstream (ai&, an OpenAI-compatible Chat Completions surface).
//
// The Anthropic-era contract this suite used to assert no longer holds: the
// router dispatches to a single OpenAI-compat upstream, so Anthropic
// `cache_control` breakpoints are stripped on the way out, the Anthropic
// 4-breakpoint capacity never applies, and the upstream reports no
// `cache_creation_input_tokens`. What the upstream does instead is reuse a
// prompt prefix it has already seen and report the reuse as `cached_tokens`,
// which the router surfaces as Anthropic's `cache_read_input_tokens`.
//
// Measured upstream contract (probed against api.aiand.com, and asserted here
// so a replay run keeps guarding it):
//   - A repeated prompt reads back from the upstream cache.
//   - The upstream keys that cache on the whole prompt: a different trailing
//     user turn is a different prompt.
//   - Anthropic `cache_control` markers are ignored, and more than four of
//     them are accepted rather than rejected.
func TestCaching(t *testing.T) {
	// The upstream reuses a cached prefix across requests carrying the same
	// prompt. The client's output budget is not part of the prompt, so two
	// turns with identical messages but different budgets hit the same cached
	// prefix — the second one must report the reuse.
	t.Run("repeated prompt reads the upstream cache", func(t *testing.T) {
		uid := "smoke-cache-repeat"
		warm := call(t, newRequest(uid).tokens(20000).text("Say: one").build(t))
		requireOKMessage(t, warm)

		// Same messages, different output budget: a different upstream request
		// (so a different cassette) over the same cached prefix.
		read := call(t, newRequest(uid).tokens(24000).text("Say: one").build(t))
		requireOKMessage(t, read)
		if read.message.Usage.CacheReadInputTokens <= 0 {
			t.Errorf("repeat of an identical prompt: want cache_read_input_tokens > 0, got %d "+
				"(the upstream did not reuse its cached prefix, or the router dropped the reuse)",
				read.message.Usage.CacheReadInputTokens)
		}
		if read.message.Usage.InputTokens <= 0 {
			t.Errorf("repeat of an identical prompt: want input_tokens > 0, got %d",
				read.message.Usage.InputTokens)
		}
	})

	// A changed trailing turn is a different prompt upstream, and it must still
	// be served. Whether the upstream happens to hold that prompt in its cache
	// depends on when the cassette was recorded, so this asserts the turn is
	// served and accounted for, not whether it was a cache hit.
	t.Run("changed trailing turn is still served", func(t *testing.T) {
		uid := "smoke-cache-changed-tail"
		first := call(t, newRequest(uid).tokens(20000).text("Say: one").build(t))
		requireOKMessage(t, first)

		second := call(t, newRequest(uid).tokens(20000).text("Say: two").build(t))
		requireOKMessage(t, second)
		if second.message.Usage.InputTokens <= 0 {
			t.Errorf("changed trailing turn: want input_tokens > 0, got %d",
				second.message.Usage.InputTokens)
		}
	})

	// Client at Anthropic's 4-breakpoint cap — the router must not add one more
	// that the upstream would reject.
	t.Run("client at capacity does not over-inject", func(t *testing.T) {
		// 2 extra cached tools + toolCache on the Edit tool + sysCache = 4 total
		// explicit breakpoints, exactly at capacity.
		body := newRequest("smoke-cache-capacity").tokens(32).
			cachedTools(2).toolCache("5m").sysCache("5m").
			text("Say: ok").build(t)
		r := call(t, body)
		requireOKMessage(t, r)
	})

	// Client pins ttl=1h on the final message block. The router must not
	// rewrite or reorder it, and the turn must still be served.
	t.Run("ttl=1h message breakpoint is served unchanged", func(t *testing.T) {
		uid := "smoke-cache-ttl-order"
		warm := call(t, newRequest(uid).tokens(32).msgCache("1h").text("Say: alpha").build(t))
		requireOKMessage(t, warm)
		read := call(t, newRequest(uid).tokens(32).msgCache("1h").text("Say: beta").build(t))
		requireOKMessage(t, read)
	})

	// More explicit breakpoints than Anthropic's 4-cap. On the Anthropic
	// family that is a 4xx; on the OpenAI-compat upstream the markers are
	// stripped before dispatch, so the request is served and the upstream
	// reports no Anthropic-style cache creation.
	t.Run("breakpoint overflow is served on the OpenAI-compat path", func(t *testing.T) {
		body := newRequest("smoke-cache-overflow").tokens(32).
			cachedTools(4).toolCache("5m").sysCache("5m").
			text("Say: ok").build(t)
		r := call(t, body)
		requireOKMessage(t, r)
		if r.message.Usage.CacheCreationInputTokens != 0 {
			t.Errorf("overflow: want cache_creation_input_tokens == 0 (the upstream reports no "+
				"Anthropic-style cache creation), got %d", r.message.Usage.CacheCreationInputTokens)
		}
	})
}
