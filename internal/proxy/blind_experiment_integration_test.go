package proxy_test

import (
	"context"

	"weave-os/router/internal/auth"
)

func blindExperimentPassthroughContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 auth.BlindExperimentArmPassthrough,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
}
