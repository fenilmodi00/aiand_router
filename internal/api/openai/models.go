package openai

import (
	"net/http"

	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

type codexModelsResponse struct {
	Models []struct{} `json:"models"`
}

// ModelsHandler serves the Codex-shaped empty model list to Codex clients and
// delegates other clients to the existing provider passthrough. Codex merges an
// empty response with its bundled catalog, so its native picker stays usable.
func ModelsHandler(fallback gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := proxy.ClientIdentityFromHeaders(c.Request.Header)
		if id.ClientApp != proxy.ClientAppCodex {
			fallback(c)
			return
		}
		c.JSON(http.StatusOK, codexModelsResponse{Models: []struct{}{}})
	}
}
