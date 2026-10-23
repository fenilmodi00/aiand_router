package openai

import (
	"net/http"

	"aiand-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

type codexModelsResponse struct {
	Models []struct{} `json:"models"`
}

// ModelsHandler gives Codex clients the empty-models shape Codex merges with
// its bundled catalog, and delegates every other client to the existing
// provider passthrough.
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
