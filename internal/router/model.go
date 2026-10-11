package router

import (
	"fmt"
	"regexp"
)

// ModelCapability identifies a feature that only a subset of models support.
type ModelCapability string

const (
	CapAdaptiveThinking ModelCapability = "adaptive_thinking"
	CapExtendedThinking ModelCapability = "extended_thinking"
	CapReasoning        ModelCapability = "reasoning"
	CapExtendedContext  ModelCapability = "extended_context"
	// CapXhighEffort marks models supporting effort "xhigh" (opus-4-7+ only;
	// sonnet-4-6 400s on it). Emit clamps to "max" when unsupported.
	CapXhighEffort ModelCapability = "xhigh_effort"
	// CapServerSideFallback marks models accepting the top-level "fallbacks"
	// field (opus-5 and fable-5 today — the models whose safety classifiers can
	// return stop_reason "refusal"). Every other Claude 400s on the field.
	CapServerSideFallback ModelCapability = "server_side_fallback"
	// CapAutoToolChoiceOnly marks models that 400 on a forced tool_choice
	// ({"type":"any"} / {"type":"tool"}); emit downgrades those to auto.
	CapAutoToolChoiceOnly ModelCapability = "auto_tool_choice_only"
	// CapTemperatureUnsupported marks Anthropic models that reject the
	// temperature parameter even though they accept other sampling controls.
	CapTemperatureUnsupported ModelCapability = "temperature_unsupported"
)

// ModelSpec describes what a model supports. Zero value is safe: provider
// adapters strip all capability-gated fields when Supports reports false.
type ModelSpec struct {
	capabilities map[ModelCapability]struct{}
	reasoning    ReasoningCapabilities
}

// ReasoningCapabilities declares the reasoning semantics a model can preserve.
// Levels are ordered from least to most expensive for deterministic clamping.
type ReasoningCapabilities struct {
	Levels         []string
	SupportsAuto   bool
	SupportsBudget bool
	AlwaysOn       bool
}

// Supports reports whether the model advertises cap.
func (s ModelSpec) Supports(cap ModelCapability) bool {
	_, ok := s.capabilities[cap]
	return ok
}

func NewSpec(caps ...ModelCapability) ModelSpec {
	s := ModelSpec{capabilities: make(map[ModelCapability]struct{}, len(caps))}
	for _, c := range caps {
		s.capabilities[c] = struct{}{}
	}
	return s
}

// NewSpecWithReasoning creates a model spec with explicit reasoning support.
func NewSpecWithReasoning(reasoning ReasoningCapabilities, caps ...ModelCapability) ModelSpec {
	s := NewSpec(caps...)
	s.reasoning = reasoning
	return s
}

// Reasoning returns a copy of the model's declared reasoning capabilities.
func (s ModelSpec) Reasoning() ReasoningCapabilities {
	levels := append([]string(nil), s.reasoning.Levels...)
	return ReasoningCapabilities{
		Levels: levels, SupportsAuto: s.reasoning.SupportsAuto,
		SupportsBudget: s.reasoning.SupportsBudget, AlwaysOn: s.reasoning.AlwaysOn,
	}
}

// ValidateReasoningCapabilities verifies that catalog reasoning declarations
// are internally coherent.
func (s ModelSpec) ValidateReasoningCapabilities() error {
	if len(s.reasoning.Levels) == 0 {
		if s.reasoning.SupportsAuto || s.reasoning.SupportsBudget || s.reasoning.AlwaysOn {
			return fmt.Errorf("reasoning modifiers require at least one supported level")
		}
		return nil
	}
	seen := make(map[string]struct{}, len(s.reasoning.Levels))
	for _, level := range s.reasoning.Levels {
		if level == "" {
			return fmt.Errorf("reasoning level cannot be empty")
		}
		if _, exists := seen[level]; exists {
			return fmt.Errorf("duplicate reasoning level %q", level)
		}
		seen[level] = struct{}{}
	}
	return nil
}

// dateSuffix matches Anthropic (-20251001) and OpenAI (-2024-08-06) trailing date stamps.
var dateSuffix = regexp.MustCompile(`-\d{4}-?\d{2}-?\d{2}$`)

// StripDateSuffix removes a trailing Anthropic-style (-20251001) or OpenAI-style
// (-2024-08-06) date stamp from a model ID. Returns the input unchanged when no suffix matches.
func StripDateSuffix(model string) string {
	return dateSuffix.ReplaceAllString(model, "")
}

// Lookup returns the spec for a known model ID. Dated variants (e.g.
// "-20251001") fall back to the base model; unknown models get zero-value.
func Lookup(model string) ModelSpec {
	if s, ok := registry[model]; ok {
		return s
	}
	if base := StripDateSuffix(model); base != model {
		if s, ok := registry[base]; ok {
			return s
		}
	}
	return ModelSpec{}
}

// AIand roster: always-reasoning open-weights models behind the OpenAI-compat
// surface. Effort menu is the conservative low/medium/high — no roster model
// documents an xhigh/max tier on AIand, so the router must not pin one.
var aiandReasoning = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, AlwaysOn: true}, CapReasoning)

var registry = map[string]ModelSpec{
	"zai-org/glm-5.3":                 aiandReasoning,
	"zai-org/glm-5.3-flash":           aiandReasoning,
	"moonshotai/kimi-k3":              aiandReasoning,
	"deepseek-ai/deepseek-v4-flash":   aiandReasoning,
	"deepseek-ai/deepseek-v4.1-flash": aiandReasoning,
	"deepseek-ai/deepseek-v4-pro":     aiandReasoning,
	"qwen/qwen3.8-27b":                aiandReasoning,
	"motif-technologies/motif-3":      aiandReasoning,
}

// ValidateCatalogReasoningCapabilities validates every declared model at
// startup composition time or in catalog tests.
func ValidateCatalogReasoningCapabilities() error {
	for model, spec := range registry {
		if err := spec.ValidateReasoningCapabilities(); err != nil {
			return fmt.Errorf("model %q: %w", model, err)
		}
	}
	return nil
}
