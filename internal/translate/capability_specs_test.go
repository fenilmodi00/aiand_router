package translate_test

import (
	"weave-os/router/internal/router"
)

// The wired catalog declares no per-model capabilities: router.Lookup yields
// the zero-value spec, so a production target never carries a reasoning menu.
// The emit paths still honor a caller-supplied spec, and these fixtures
// exercise those branches the same way the post-cut proxy tests do.
var (
	// reasoningSpec is a target that accepts a reasoning effort and a numeric
	// reasoning budget.
	reasoningSpec = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true},
		router.CapReasoning,
	)
	// alwaysOnSpec is a target that always reasons: a disable clamps to its
	// floor level and it accepts named levels but no budget (Gemini 3.x shape).
	alwaysOnSpec = router.NewSpecWithReasoning(
		router.ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, AlwaysOn: true},
		router.CapReasoning,
	)
)
