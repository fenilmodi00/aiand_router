package proxy

import (
	"context"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"
)

func switchedAttemptOptions(ctx context.Context, base translate.EmitOptions, target router.Decision, escalate bool,
	resolveEffort func(context.Context, router.Decision, router.ModelSpec, bool) effortResolution,
) (translate.EmitOptions, effortResolution) {
	base.TargetModel = target.Model
	base.TargetProvider = target.Provider
	base.Capabilities = router.Lookup(target.Model)
	base.ModelSwitched = true
	effort := resolveEffort(ctx, target, base.Capabilities, escalate)
	effort.apply(&base)
	return base, effort
}
