package turntype

import (
	"weave-os/router/internal/translate"
)

// PremiumWorthyCapMultiple is the cost-cap multiple a premium-worthy turn is
// routed with: a widening above the deployment knee (cluster.DefaultCostCapMultiple,
// 3.0x) for the turns where a wrong pick costs the most to recover from.
//
// Measured consequence on the shipped v0.84 ladder (effective cost with
// output_cost_ratio=0.25, per-model verbosity): every lane at or below ~6x enters
// the candidate pool, which adds exactly one lane over the deployment cap —
// motif-3 at 4.70x. The premium lanes deeper into the ladder stay out and need
// far wider rungs: qwen3.8-27b 6.83x, deepseek-v4-pro 7.78x, glm-5.3 10.80x,
// kimi-k3 25.49x. They remain unreachable on merit until their measured cells
// improve or the deployment knob rises; this constant deliberately does not
// pretend otherwise.
//
// Same scale as cluster.DefaultCostCapMultiple, so the cluster scorer clamps
// to it rather than switching units.
const PremiumWorthyCapMultiple = 6.0

const (
	// premiumWorthyToolResultChars is the tool-result payload volume past
	// which a turn is an agentic continuation rather than a quick check: a 40KB
	// result set is a browse, a diff, or a test log being reasoned over.
	premiumWorthyToolResultChars = 40_000

	// premiumWorthyInputTokens is the estimated prompt-token budget past which
	// a turn carries agentic context (long history plus tool definitions)
	// instead of a short exchange.
	premiumWorthyInputTokens = 30_000

	// premiumWorthyToolCount is the tool-definition count that marks an
	// explicit multi-tool turn: two or more tools means the turn can act.
	premiumWorthyToolCount = 2

	// premiumWorthyMessageCount is the visible-history length past which a turn
	// is mid-conversation agentic work rather than a fresh question.
	premiumWorthyMessageCount = 20
)

// PremiumWorthyCap reports the cost-cap clamp this turn should route with, or
// false to keep the deployment default. Only the scored turn types qualify
// (MainLoop, ToolResult); every other type — compaction, probes, title
// generation, recaps, classifier calls, sub-agent dispatch — returns false
// whatever else it carries, because those turns have their own hard pins or
// their own purpose and must not widen the premium pool.
//
// Pure: inputs are the already-computed routing features, the request
// envelope, and the detected turn type. No clock, no I/O, no randomness, so
// the same turn always decodes to the same answer.
//
// Any one signal qualifies — an agentic turn is recognizable by its shape, and
// the cheap lane's misses on tool-heavy, large-context turns are the ones that
// cost the most downstream work:
//   - tool results large enough to be reasoned over,
//   - an agentic-scale prompt budget,
//   - an explicit multi-tool declaration,
//   - a long conversation.
func PremiumWorthyCap(turnType TurnType, feats translate.RoutingFeatures, env *translate.RequestEnvelope) (float64, bool) {
	if turnType != MainLoop && turnType != ToolResult {
		return 0, false
	}
	if toolResultChars(env) >= premiumWorthyToolResultChars {
		return PremiumWorthyCapMultiple, true
	}
	if feats.Tokens >= premiumWorthyInputTokens {
		return PremiumWorthyCapMultiple, true
	}
	if feats.HasTools && len(env.ToolDescriptors()) >= premiumWorthyToolCount {
		return PremiumWorthyCapMultiple, true
	}
	if feats.MessageCount >= premiumWorthyMessageCount {
		return PremiumWorthyCapMultiple, true
	}
	return 0, false
}

// toolResultChars sums tool-result payload characters across the envelope's
// visible history. Counts only what the conversation conversion already carries,
// so the decode costs no extra parse.
func toolResultChars(env *translate.RequestEnvelope) int {
	if env == nil {
		return 0
	}
	total := 0
	for _, msg := range env.ConversationMessages() {
		for _, result := range msg.ToolResults {
			total += len(result.Text)
		}
	}
	return total
}
