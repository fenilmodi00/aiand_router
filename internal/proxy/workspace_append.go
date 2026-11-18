package proxy

import (
	"context"

	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
)

// workspaceSystemAppendApplies decides whether this turn's outgoing system
// prompt gets translate.WorkspaceSystemText appended. Provider gating is not
// decided here: the cross-format emitters apply the append and PrepareAnthropic
// ignores it, so a sibling failover onto Anthropic serves the client prompt
// untouched.
func (s *Service) workspaceSystemAppendApplies(ctx context.Context, body []byte, env *translate.RequestEnvelope, tt turntype.TurnType) bool {
	return ccSystemAppendApplies(ctx, body, tt, s.ResolveCCWorkspaceSystemAppend(ctx), env.HasWorkspaceSystemText())
}

// workspaceAppendFired reports whether the served attempt actually carried the
// workspace append.
func workspaceAppendFired(opts translate.EmitOptions, servedProvider string) bool {
	return systemAppendFired(opts.AppendWorkspaceSystem, servedProvider)
}
