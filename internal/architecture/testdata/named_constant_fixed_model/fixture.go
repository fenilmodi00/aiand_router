package namedconstantfixedmodel

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const fixedModel = "z-ai/glm-5.3-flash"

func decision() router.Decision {
	return router.Decision{Provider: providers.ProviderAIAND, Model: fixedModel}
}
