package directfixedmodel

import (
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
)

func decision() router.Decision {
	return router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash"}
}
