package translate_test

import "weave-os/router/internal/router"

// The router registry carries only the served AIand roster. The emitters still
// branch on capabilities, so tests that exercise the Anthropic/OpenAI/Gemini
// branches supply the spec directly through EmitOptions.Capabilities instead of
// depending on a registry row for a model the router no longer serves.
//
// prunedCaps mirrors the pre-AIand-only registry entries for those models; the
// specs are the literals the registry used to hold.
var (
	prunedAnthropicAdaptive = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max"}, AlwaysOn: true},
		router.CapAdaptiveThinking, router.CapExtendedContext)
	prunedAnthropicAdaptiveXhigh = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true},
		router.CapAdaptiveThinking, router.CapExtendedContext, router.CapXhighEffort)
	prunedAnthropicAdaptiveFallback = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true},
		router.CapAdaptiveThinking, router.CapExtendedContext, router.CapXhighEffort, router.CapServerSideFallback)
	prunedAnthropicAdaptiveFallbackAutoTools = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true},
		router.CapAdaptiveThinking, router.CapExtendedContext, router.CapXhighEffort, router.CapServerSideFallback, router.CapAutoToolChoiceOnly)
	// prunedFable51 mirrors the upstream registry row for claude-fable-5-1
	// after it gained CapTemperatureUnsupported (it 400s on temperature
	// while still accepting the other sampling controls).
	prunedFable51 = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true},
		router.CapAdaptiveThinking, router.CapExtendedContext, router.CapXhighEffort, router.CapServerSideFallback,
		router.CapAutoToolChoiceOnly, router.CapTemperatureUnsupported)
	prunedAnthropicExtended = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true},
		router.CapExtendedThinking)

	prunedOpenAIReasoning = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true},
		router.CapReasoning)
	prunedOpenAI56 = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true},
		router.CapReasoning, router.CapXhighEffort)
	prunedOpenAIAstra = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true, AlwaysOn: true},
		router.CapReasoning, router.CapXhighEffort)
	prunedOpenAI6 = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true},
		router.CapReasoning, router.CapXhighEffort)
	prunedOpenAI61 = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true, AlwaysOn: true},
		router.CapReasoning, router.CapXhighEffort)
	prunedGrokXhigh = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true},
		router.CapReasoning, router.CapXhighEffort)
	prunedMuseSpark = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true, AlwaysOn: true},
		router.CapReasoning, router.CapXhighEffort)

	prunedGoogleBase = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true})
	prunedGoogle3Base = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, AlwaysOn: true})
)

// Rows that carried no capabilities (gpt-4.x, gpt-4o, the qwen-compat ids) are
// absent on purpose: the live registry returns the same zero spec for them, so
// capsFor falls through.
var prunedCaps = map[string]router.ModelSpec{
	"claude-sonnet-5":   prunedAnthropicAdaptive,
	"claude-sonnet-4-6": prunedAnthropicAdaptive,
	"claude-opus-4-6":   prunedAnthropicAdaptive,

	"claude-opus-4-8": prunedAnthropicAdaptiveXhigh,
	"claude-opus-4-7": prunedAnthropicAdaptiveXhigh,

	"claude-fable-5": prunedAnthropicAdaptiveFallback,
	"claude-opus-5":  prunedAnthropicAdaptiveFallback,

	"claude-fable-5-1":  prunedFable51,
	"claude-opus-5-5":   prunedAnthropicAdaptiveFallbackAutoTools,
	"claude-sonnet-5-5": prunedAnthropicAdaptiveFallbackAutoTools,

	"claude-haiku-4-5": prunedAnthropicExtended,
	"claude-opus-4-5":  prunedAnthropicExtended,
	"claude-opus-4-1":  prunedAnthropicExtended,
	"claude-opus-4-0":  prunedAnthropicExtended,

	"gpt-6-astra": prunedOpenAIAstra,
	"gpt-6-sol":   prunedOpenAI6,
	"gpt-6-luna":  prunedOpenAI6,
	"gpt-6.1-sol": prunedOpenAI61,

	"gpt-5.6-sol":      prunedOpenAI56,
	"gpt-5.6-sol-pro":  prunedOpenAI56,
	"gpt-5.6-terra":    prunedOpenAI56,
	"gpt-5.6-luna":     prunedOpenAI56,
	"gpt-5.6-luna-pro": prunedOpenAI56,

	"gpt-5.5":      prunedOpenAIReasoning,
	"gpt-5.5-pro":  prunedOpenAIReasoning,
	"gpt-5.5-mini": prunedOpenAIReasoning,
	"gpt-5.5-nano": prunedOpenAIReasoning,
	"gpt-5.4":      prunedOpenAIReasoning,
	"gpt-5.4-pro":  prunedOpenAIReasoning,
	"gpt-5.4-mini": prunedOpenAIReasoning,
	"gpt-5.4-nano": prunedOpenAIReasoning,
	"gpt-5.3":      prunedOpenAIReasoning,
	"gpt-5.2":      prunedOpenAIReasoning,
	"gpt-5.2-pro":  prunedOpenAIReasoning,
	"gpt-5.1":      prunedOpenAIReasoning,
	"gpt-5":        prunedOpenAIReasoning,
	"gpt-5-chat":   prunedOpenAIReasoning,
	"gpt-5-pro":    prunedOpenAIReasoning,
	"gpt-5-mini":   prunedOpenAIReasoning,
	"gpt-5-nano":   prunedOpenAIReasoning,
	"o3":           prunedOpenAIReasoning,
	"o3-pro":       prunedOpenAIReasoning,
	"o3-mini":      prunedOpenAIReasoning,
	"o4-mini":      prunedOpenAIReasoning,
	"o1":           prunedOpenAIReasoning,
	"o1-pro":       prunedOpenAIReasoning,
	"o1-mini":      prunedOpenAIReasoning,
	"grok-4.5":     prunedOpenAIReasoning,

	"grok-4.6": prunedGrokXhigh,
	"grok-4.7": prunedGrokXhigh,

	"muse-spark-1.3": prunedMuseSpark,

	"gemini-3-pro-preview":          prunedGoogle3Base,
	"gemini-3.1-pro-preview":        prunedGoogle3Base,
	"gemini-3-flash-preview":        prunedGoogle3Base,
	"gemini-3.1-flash-lite-preview": prunedGoogle3Base,
	"gemini-3.1-flash-live-preview": prunedGoogle3Base,
	"gemini-3.5-flash":              prunedGoogle3Base,
	"gemini-3.5-flash-lite":         prunedGoogle3Base,
	"gemini-3.6-flash":              prunedGoogle3Base,
	"gemini-3.7-flash":              prunedGoogle3Base,
	"gemini-3.8-flash":              prunedGoogle3Base,

	"gemini-2.5-pro":        prunedGoogleBase,
	"gemini-2.5-flash":      prunedGoogleBase,
	"gemini-2.5-flash-lite": prunedGoogleBase,
	"gemini-2.0-flash":      prunedGoogleBase,
	"gemini-2.0-flash-lite": prunedGoogleBase,
}

// capsFor returns the capability spec a test wants for model: the pre-cut spec
// for models the registry no longer serves, else the live registry spec.
func capsFor(model string) router.ModelSpec {
	if spec, ok := prunedCaps[model]; ok {
		return spec
	}
	return router.Lookup(model)
}
