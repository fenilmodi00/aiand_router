package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestContextWindowConstraintDefersToOverflowAdmission(t *testing.T) {
	binding := plannedBinding{Binding: Binding{CatalogID: "moonshotai/kimi-k3"}}
	request := router.Request{EstimatedInputTokens: catalog.ContextWindowFor("moonshotai/kimi-k3") + 1}

	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding))

	request.OverflowAdmittedModels = map[string]struct{}{"moonshotai/kimi-k3": {}}
	assert.Empty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding),
		"the plan's proof must agree with the resolver that kept the admitted model")
}
