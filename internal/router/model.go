package router

import (
	"regexp"
)

// ModelCapability identifies a feature that only a subset of models support.
type ModelCapability string

const (
	// CapReasoning marks models that accept a reasoning effort / budget.
	CapReasoning ModelCapability = "reasoning"
	// CapXhighEffort marks models supporting effort "xhigh"; emit clamps to
	// "max" when unsupported.
	CapXhighEffort ModelCapability = "xhigh_effort"
)

// ModelGemini4Argon is the canonical Google Gemini 4 Argon model ID.
const ModelGemini4Argon = "gemini-4-argon"

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
		Levels: levels, SupportsBudget: s.reasoning.SupportsBudget, AlwaysOn: s.reasoning.AlwaysOn,
	}
}

// dateSuffix matches Anthropic (-20251001) and OpenAI (-2024-08-06) trailing date stamps.
var dateSuffix = regexp.MustCompile(`-\d{4}-?\d{2}-?\d{2}$`)

// StripDateSuffix removes a trailing Anthropic-style (-20251001) or OpenAI-style
// (-2024-08-06) date stamp from a model ID. Returns the input unchanged when no suffix matches.
func StripDateSuffix(model string) string {
	return dateSuffix.ReplaceAllString(model, "")
}

// Lookup returns the capability spec for a model ID.
//
// The wired catalog serves models that declare no per-model capabilities, so
// every lookup yields the zero-value spec: callers read "no spec" as "no
// capability-gated fields are forwarded".
func Lookup(string) ModelSpec {
	return ModelSpec{}
}
