package turntype

import (
	"weave-os/router/internal/translate"
)

// AgenticRepairModels are the catalogue models that measurably lead the
// agentic-repair traffic class, in preference order.
//
// Measured on the AIDER polyglot corpus (150 prompts, every score from the
// arena's own metrics.aider_accuracy, which runs the task's real test_command
// against the model's edit), regenerated at a fair token budget:
//
//	glm-5.3        44 cells  0.364  CI95 (0.238, 0.511)
//	glm-5.3-flash  67 cells  0.269  CI95 (0.177, 0.385)
//
// against the deepseek lanes' fair-budget labels on the same corpus
// (v4-flash 0.153, v4-pro 0.160, v4.1-flash 0.235). GLM separates from the
// cheaper lanes; it overlaps v4.1-flash, so this is a preference and not a
// mandate — the scorer's rank-decaying bonus still loses to a clearly better
// model.
//
// This is deliberately NOT written into the cluster quality cells: AIDER and
// LCBv5 prompts land in the same clusters, and on fair LCBv5 labels glm-5.3
// scores 0.783 against deepseek-v4-pro's 0.897. A cell-level override would
// hand GLM the live-coding traffic it loses to buy the repair traffic it wins.
var AgenticRepairModels = []string{
	"zai-org/glm-5.3",
	"zai-org/glm-5.3-flash",
}

const (
	// agenticRepairToolResultChars is the tool-result volume that marks a turn
	// as repair work being reasoned over: a failing test log, a diff being
	// reworked, or a stack trace. Agent loops producing test output produce
	// these on the second turn of a fix, which is where the model choice
	// actually costs accuracy.
	agenticRepairToolResultChars = 20_000

	// agenticRepairMessageCount is the mid-conversation depth that marks an
	// established repair loop rather than a first question. Below this the
	// turn is a fresh ask with no in-flight repair to earn the premium lane.
	agenticRepairMessageCount = 6
)

// AgenticRepairPreference reports the models this turn should prefer, or nil
// when the turn is not agentic-repair shaped.
//
// Only scored turn types qualify (MainLoop, ToolResult); every other type —
// compaction, probes, title generation, recaps, classifier calls, sub-agent
// dispatch — returns nil, because those turns have their own hard pins or their
// own purpose.
//
// Pure: inputs are the already-computed routing features, the request envelope
// and the detected turn type. No clock, no I/O, no randomness.
//
// The shape it decodes is narrower than PremiumWorthyCap's: a repair loop in
// progress — repeated tool results (test output, diffs) over a conversation
// deep enough to have failed a first attempt. One signal alone is enough here
// because the pair (deep history AND substantial tool output) is what
// distinguishes repair from a fresh question.
func AgenticRepairPreference(turnType TurnType, feats translate.RoutingFeatures, env *translate.RequestEnvelope) []string {
	if turnType != MainLoop && turnType != ToolResult {
		return nil
	}
	deep := feats.MessageCount >= agenticRepairMessageCount
	substance := toolResultChars(env) >= agenticRepairToolResultChars
	if deep && substance {
		return AgenticRepairModels
	}
	return nil
}

// ToolResultCharCount exposes the tool-result payload volume AgenticRepairPreference
// (and PremiumWorthyCap) decode against, so a caller logging why a turn qualified
// need not recompute the conversion. Pure and cheap: it walks what the
// envelope conversion already produced.
func ToolResultCharCount(env *translate.RequestEnvelope) int {
	return toolResultChars(env)
}
