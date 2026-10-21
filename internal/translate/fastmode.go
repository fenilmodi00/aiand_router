package translate

import (
	"fmt"


	"github.com/tidwall/sjson"
)

// fastModeBeta unlocks Anthropic's speed:"fast" Messages field.
const fastModeBeta = "fast-mode-2026-02-01"

// openAIPriorityServiceTier is OpenAI's paid fast tier for chat/completions
// and Responses.
const openAIPriorityServiceTier = "priority"

// ApplyOpenAIFastMode sets service_tier:"priority" on a body when opts.FastMode
// is on. The single upstream accepts the field, so it is always sent.
func ApplyOpenAIFastMode(body []byte, opts EmitOptions) ([]byte, error) {
	if !opts.FastMode {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "service_tier", openAIPriorityServiceTier)
	if err != nil {
		return nil, fmt.Errorf("set service_tier: %w", err)
	}
	return out, nil
}

// applyAnthropicFastMode sets speed:"fast" on a body bound for an
// Anthropic-format upstream; deriveAnthropicHeaders adds the beta token
// whenever the field is present.
func applyAnthropicFastMode(body []byte, opts EmitOptions) ([]byte, error) {
	if !opts.FastMode {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "speed", "fast")
	if err != nil {
		return nil, fmt.Errorf("set speed: %w", err)
	}
	return out, nil
}
