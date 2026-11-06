package admin

import (
	"net/http"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// resolveInstallation returns the installation admin operations should act on:
// the one resolved from the caller's identity — dashboard session cookie or
// rk_ bearer — which the auth middleware stashed on the request context.
// Writes a 401 when no identity is present; callers return when ok == false.
func resolveInstallation(c *gin.Context, _ *auth.Service) (*auth.Installation, bool) {
	if installation := middleware.InstallationFrom(c); installation != nil {
		return installation, true
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
	return nil, false
}
