package admin

import (
	"context"
	"net/http"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// displaySettingsResponse is the per-installation toggle set read by the installer and statusline.
type displaySettingsResponse struct {
	HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
}

// CallerResolver attributes a display-settings request to its router user, the same way inference requests are.
type CallerResolver func(c *gin.Context) context.Context

// InstallationExperimentChecker answers for callers that resolve to no router user; implementations
// should report true when they cannot tell, so an unidentified subject stays hidden.
type InstallationExperimentChecker interface {
	InstallationBlindExperimentEnabled(ctx context.Context, installationID string) bool
}

// DisplaySettingsHandler returns the calling installation's display toggles (scoped to the caller by the rk_ bearer key).
// It resolves no caller, so it can only honor the installation toggle; register
// DisplaySettingsHandlerFor to also hide surfaces for blind experiment subjects.
func DisplaySettingsHandler(c *gin.Context) {
	displaySettings(c, nil, nil)
}

// DisplaySettingsHandlerFor returns the display-settings handler with caller
// resolution and the experiment check bound. Blind experiment subjects always get hidden surfaces so
// neither arm learns it is being routed. A caller that cannot be attributed to a router user is hidden
// whenever the installation runs an experiment, because inference may still identify it as a subject
// from the request body.
func DisplaySettingsHandlerFor(resolveCaller CallerResolver, experiments InstallationExperimentChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		displaySettings(c, resolveCaller, experiments)
	}
}

func displaySettings(c *gin.Context, resolveCaller CallerResolver, experiments InstallationExperimentChecker) {
	installation := middleware.InstallationFrom(c)
	if installation == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
		return
	}
	hide := installation.HideTerminalSurfaces
	if !hide && resolveCaller != nil {
		callerCtx := resolveCaller(c)
		_, hide = auth.BlindExperimentFrom(callerCtx)
		if !hide && auth.UserIDFrom(callerCtx) == "" && experiments != nil {
			hide = experiments.InstallationBlindExperimentEnabled(callerCtx, installation.ID)
		}
	}
	c.JSON(http.StatusOK, displaySettingsResponse{
		HideTerminalSurfaces: hide,
	})
}
