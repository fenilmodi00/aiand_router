package proxy

import (
	"context"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
	"aiand-os/router/internal/websearch"
)

// ccSystemAppendApplies is the shared gate for the Claude Code system-prompt
// appends (autonomy and workspace). Only the turns a human would otherwise be
// expected to answer qualify: Claude Code (or its Agent SDK, which identifies
// the same way) main-loop and tool-result turns. Title generation, compaction,
// probes, classifiers, sub-agent dispatch and the self-contained native
// web-search sub-turn keep their prompts untouched, as does any request that
// already carries the instruction.
func ccSystemAppendApplies(ctx context.Context, body []byte, tt turntype.TurnType, enabled, alreadyPresent bool) bool {
	if !enabled {
		return false
	}
	if ClientIdentityFrom(ctx).ClientApp != ClientAppClaudeCode {
		return false
	}
	if tt != turntype.MainLoop && tt != turntype.ToolResult {
		return false
	}
	if _, searchTurn := websearch.DetectSearchTurn(body); searchTurn {
		return false
	}
	return !alreadyPresent
}

// autonomySystemAppendApplies decides whether this turn's outgoing system
// prompt gets translate.AutonomySystemText appended.
func (s *Service) autonomySystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	return ccSystemAppendApplies(ctx, body, tt, s.ResolveCCAutonomySystemAppend(ctx), env.HasAutonomySystemText())
}

// systemAppendFired reports whether the served attempt actually carried an
// append: only the cross-format emitters carry the option, so a turn that
// failed over onto an OpenAI-compat binding is recorded as not fired.
func systemAppendFired(requested bool, servedProvider string) bool {
	return requested && providers.FamilyFor(servedProvider) != providers.FamilyOpenAICompat
}

// autonomyAppendFired reports whether the served attempt actually carried the
// autonomy append.
func autonomyAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return systemAppendFired(opts.AppendAutonomySystem, servedProvider)
}
