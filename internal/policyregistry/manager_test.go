package policyregistry_test

import (
	"context"

	"aiand-os/router/internal/router"
)

type fixedRouter struct{ model string }

func (r fixedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Model: r.model}, nil
}
