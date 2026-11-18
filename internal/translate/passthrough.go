package translate

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"aiand-os/router/internal/providers"
)

// PrepareAnthropicPassthrough builds a PreparedRequest for non-routing
// Anthropic endpoints, stripping inference-time fields, unsupported tool
// schema constraints, and thinking betas.
func (e *RequestEnvelope) PrepareAnthropicPassthrough(in http.Header) (providers.PreparedRequest, error) {
	ov, _ := resolvePassthroughOverrides(e.body)
	ov.SanitizeAnthropicToolSchemas = true
	body, err := e.emitSameFormat(ov)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = normalizeAnthropicSystemOnlyContentBlocks(body)
	if err != nil {
		return providers.PreparedRequest{}, fmt.Errorf("normalize system-only content blocks: %w", err)
	}
	body, err = placeAnthropicSystemMessagesBytes(body)
	if err != nil {
		return providers.PreparedRequest{}, fmt.Errorf("place system messages: %w", err)
	}
	return providers.PreparedRequest{Body: body, Headers: AnthropicPassthroughHeadersForBody(in, body)}, nil
}

// AnthropicPassthroughHeaders returns header overrides for the passthrough path.
func AnthropicPassthroughHeaders(in http.Header) http.Header {
	return AnthropicPassthroughHeadersForBody(in, nil)
}

// AnthropicPassthroughHeadersForBody returns passthrough headers and enables
// Anthropic's mid-conversation tool-change beta when the body uses its blocks.
func AnthropicPassthroughHeadersForBody(in http.Header, body []byte) http.Header {
	h := make(http.Header)
	if v := in.Get("anthropic-version"); v != "" {
		h.Set("anthropic-version", v)
	} else {
		h.Set("anthropic-version", "2023-06-01")
	}
	beta := stripThinkingBetas(in.Get("anthropic-beta"))
	if containsAnthropicSystemOnlyContentBlocks(body) {
		beta = ensureBetaToken(beta, anthropicMidConversationToolChangesBeta)
	}
	if beta != "" {
		h.Set("anthropic-beta", beta)
	}
	return h
}

func stripThinkingBetas(beta string) string {
	return joinKept(beta, func(token string) bool {
		return !strings.Contains(token, "thinking")
	})
}

func (e *RequestEnvelope) PrepareAnthropicVerbatim(in http.Header) providers.PreparedRequest {
	h := make(http.Header)
	h.Set("anthropic-version", in.Get("anthropic-version"))
	if h.Get("anthropic-version") == "" {
		h.Set("anthropic-version", "2023-06-01")
	}
	if beta := in.Get("anthropic-beta"); beta != "" {
		h.Set("anthropic-beta", beta)
	}
	return providers.PreparedRequest{Body: e.body, Headers: h}
}

func (e *RequestEnvelope) PrepareOpenAIVerbatim(opts EmitOptions) (providers.PreparedRequest, bool) {
	if e.format != FormatOpenAI || opts.TargetModel != e.Model() || opts.StripPromptCacheKey && gjson.GetBytes(e.body, "prompt_cache_key").Exists() {
		return providers.PreparedRequest{}, false
	}
	if e.Stream() && opts.IncludeStreamUsage && !gjson.GetBytes(e.body, "stream_options.include_usage").Bool() {
		return providers.PreparedRequest{}, false
	}
	return providers.PreparedRequest{Body: e.body, Headers: make(http.Header)}, true
}
