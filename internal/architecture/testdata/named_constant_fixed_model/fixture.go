package namedconstantfixedmodel

import (
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
)

const fixedModel = "z-ai/glm-5.3-flash"

func decision() router.Decision {
	return router.Decision{Provider: providers.ProviderAIAND, Model: fixedModel}
}
