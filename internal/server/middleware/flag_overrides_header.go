package middleware

import (
	"net/http"
	"strings"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/flags"
	"aiand-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

const FlagOverridesHeader = "x-weave-flag-overrides"

// WithFlagOverridesHeader layers a per-request flag override set over the
// installation's stored overrides. Only installations authorized for policy
// headers get the layer; for everyone else the header is ignored.
func WithFlagOverridesHeader(svc *auth.Service) gin.HandlerFunc {
	orgOverridesDisabled := svc != nil && svc.FlagOverridesDisabled()
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader(FlagOverridesHeader))
		if raw == "" {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if orgOverridesDisabled || installation == nil || !installation.PolicyHeaderOverridesEnabled {
			observability.FromGin(c).Debug("Flag overrides header ignored: installation is not authorized for policy headers")
			c.Next()
			return
		}
		requested, err := flags.DecodeOverrides([]byte(raw))
		if err != nil {
			abortFlagOverridesMalformed(c, err)
			return
		}
		ctx := c.Request.Context()
		stored, _ := flags.OverridesFromContext(ctx)
		effective := stored.Overlay(requested)
		if err := flags.ValidateOverrides(effective); err != nil {
			abortFlagOverridesMalformed(c, err)
			return
		}
		observability.FromGin(c).Debug("Flag overrides header applied", "installation_id", installation.ID, "flag_keys", requested.Keys())
		c.Request = c.Request.WithContext(flags.WithOverrides(ctx, effective))
		c.Next()
	}
}

func abortFlagOverridesMalformed(c *gin.Context, err error) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "flag_overrides_malformed", "message": err.Error()})
}
