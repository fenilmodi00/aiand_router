package translate_test

import (
	"net/http"
	"strings"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicFinalAssistantRejectsInvalidCacheOnEmptySuffix(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"text","text":" \n","cache_control":{"type":"ephemeral","ttl":"2h"}}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3"})
	assert.ErrorIs(t, err, translate.ErrAnthropicCacheControlInvalid)
}

func TestAnthropicFinalAssistantRejectsOriginalCacheOverflow(t *testing.T) {
	block := `{"type":"text","text":"History","cache_control":{"type":"ephemeral"}}`
	body := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":[` + strings.Join([]string{block, block, block, block}, ",") + `]},{"role":"assistant","content":[{"type":"text","text":" \n","cache_control":{"type":"ephemeral"}}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3"})
	assert.ErrorIs(t, err, translate.ErrAnthropicCacheControlOverflow)
}

func TestAnthropicFinalAssistantPreservesExplicitCacheOnTrimmedText(t *testing.T) {
	for _, ttl := range []string{"1h", "5m"} {
		t.Run(ttl, func(t *testing.T) {
			body := []byte(`{"model":"z-ai/glm-5.3","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"text","text":"  Answer\n \t","cache_control":{"type":"ephemeral","ttl":"` + ttl + `"}}]}]}`)
			prepared := prepareFinalAssistantAnthropic(t, body)
			assert.Equal(t, "  Answer", gjson.GetBytes(prepared, "messages.1.content.0.text").String())
			assert.Equal(t, "ephemeral", gjson.GetBytes(prepared, "messages.1.content.0.cache_control.type").String())
			assert.Equal(t, ttl, gjson.GetBytes(prepared, "messages.1.content.0.cache_control.ttl").String())
		})
	}
}

// The fork serves a single OpenAI-compat upstream, so same-format Anthropic
// emit always strips thinking/redacted_thinking (no target speaks Anthropic
// inference-time controls; see resolveAnthropicOverrides). The upstream
// #2058 regression these tests come from is the backward scan itself: when
// the latest assistant message ends in immutable blocks, the breakpoint must
// land on the last mutable block instead of attaching to (and mutating) a
// thinking block. The fork's observable: thinking blocks are stripped before
// cache placement, and the breakpoint still lands on the last surviving text
// block of the latest assistant message.
func TestAnthropicCacheSkipsImmutableLatestAssistantThinkingBlocks(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","thinking":"reasoning","signature":"sig"}`,
		`{"type":"redacted_thinking","data":"opaque"}`,
	} {
		t.Run(gjson.Get(block, "type").String(), func(t *testing.T) {
			body := []byte(`{"model":"z-ai/glm-5.3","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"text","text":"Answer"},` + block + `]}]}`)
			envelope, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3")})
			require.NoError(t, err)
			require.Len(t, gjson.GetBytes(prepared.Body, "messages.1.content").Array(), 1)
			assert.Equal(t, "text", gjson.GetBytes(prepared.Body, "messages.1.content.0.type").String())
			assert.Equal(t, "ephemeral", gjson.GetBytes(prepared.Body, "messages.1.content.0.cache_control.type").String())
		})
	}
}

func TestAnthropicCacheSkipsLatestAssistantThinkingOnlyMessage(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.3","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":"sig"}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3")})
	require.NoError(t, err)
	// The thinking-only assistant message is stripped entirely; the scan falls
	// back to the trailing user prompt for the breakpoint.
	assert.False(t, gjson.GetBytes(prepared.Body, "messages.1").Exists())
	assert.Equal(t, "ephemeral", gjson.GetBytes(prepared.Body, "messages.0.content.0.cache_control.type").String())
}

func TestAnthropicCacheSkipsLatestAssistantThinkingBeforeEmptyTrailingMessage(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.3","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":"sig"}]},{"role":"user","content":[]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3")})
	require.NoError(t, err)
	// Thinking-only assistant message stripped; empty trailing user message
	// removed; breakpoint falls back to the opening user prompt.
	assert.False(t, gjson.GetBytes(prepared.Body, "messages.2").Exists())
	assert.Equal(t, "ephemeral", gjson.GetBytes(prepared.Body, "messages.0.content.0.cache_control.type").String())
}
