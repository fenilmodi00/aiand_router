package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// Aiand auth handlers: the dashboard's "Sign in with Aiand" surface. Routes
// live under /admin/v1/aiand/* (self-hosted block). All responses are
// dashboard-facing summaries; the raw key, device code, and refresh token
// never appear in any response or log line.

type startAiandDeviceLoginResponse struct {
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartAiandDeviceLoginHandler opens a device-login session. The dashboard
// shows user_code + verification_uri, then polls the poll endpoint.
func StartAiandDeviceLoginHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		login, err := authSvc.StartAiandDeviceLogin(c.Request.Context(), installation)
		if err != nil {
			writeAiandAuthError(c, err)
			return
		}
		c.JSON(http.StatusOK, startAiandDeviceLoginResponse{
			UserCode:                login.UserCode,
			VerificationURI:         login.VerificationURI,
			VerificationURIComplete: login.VerificationURIComplete,
			ExpiresIn:               login.ExpiresIn,
			Interval:                login.Interval,
		})
	}
}

type pollAiandDeviceLoginResponse struct {
	Status    string     `json:"status"`
	Email     string     `json:"email,omitempty"`
	OrgName   string     `json:"org_name,omitempty"`
	KeyMask   string     `json:"key_masked,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// PollAiandDeviceLoginHandler exchanges one poll against the gateway. The
// dashboard owns the waiting loop: pending responses keep it polling and
// 428 responses (slow_down) carry the raised interval.
func PollAiandDeviceLoginHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		outcome, err := authSvc.PollAiandDeviceLogin(c.Request.Context(), installation)
		if err != nil {
			var slow *auth.AiandSlowDownError
			if errors.As(err, &slow) {
				c.Header("Retry-After", formatSeconds(slow.RetryAfter))
				c.JSON(http.StatusPreconditionRequired, gin.H{"error": "slow_down"})
				return
			}
			writeAiandAuthError(c, err)
			return
		}
		c.JSON(http.StatusOK, pollAiandDeviceLoginResponse{
			Status:    outcome.Status,
			Email:     outcome.Email,
			OrgName:   outcome.OrgName,
			KeyMask:   outcome.KeyMask,
			ExpiresAt: outcome.ExpiresAt,
		})
	}
}

type pasteAiandKeyRequest struct {
	Key string `json:"key"`
}

type pasteAiandKeyResponse struct {
	Email   string `json:"email"`
	OrgName string `json:"org_name"`
	KeyMask string `json:"key_masked"`
}

// PasteAiandKeyHandler stores a directly pasted sk-... key as the
// installation's Aiand credential after the gateway accepts it.
func PasteAiandKeyHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		var req pasteAiandKeyRequest
		if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Key is required."})
			return
		}
		credential, err := authSvc.CompleteAiandPasteLogin(c.Request.Context(), installation, req.Key)
		if err != nil {
			writeAiandAuthError(c, err)
			return
		}
		c.JSON(http.StatusOK, pasteAiandKeyResponse{
			Email:   credential.UserEmail,
			OrgName: credential.OrgName,
			KeyMask: auth.MaskAiandKey(string(credential.Plaintext)),
		})
	}
}

type aiandAuthStatusResponse struct {
	Connected bool       `json:"connected"`
	Email     string     `json:"email,omitempty"`
	OrgName   string     `json:"org_name,omitempty"`
	KeyMask   string     `json:"key_masked,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Origin    string     `json:"origin,omitempty"`
}

// AiandAuthStatusHandler reports whether the installation has a connected
// Aiand credential, with a masked key and no secrets.
func AiandAuthStatusHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		status, err := authSvc.AiandCredentialStatus(c.Request.Context(), installation)
		if err != nil {
			writeAiandAuthError(c, err)
			return
		}
		c.JSON(http.StatusOK, aiandAuthStatusResponse{
			Connected: status.Connected,
			Email:     status.Email,
			OrgName:   status.OrgName,
			KeyMask:   status.KeyMask,
			ExpiresAt: status.ExpiresAt,
			Origin:    string(status.Origin),
		})
	}
}

// DisconnectAiandAuthHandler removes the installation's Aiand connection,
// revoking the upstream refresh token when one exists (best-effort).
func DisconnectAiandAuthHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		if err := authSvc.DisconnectAiandCredential(c.Request.Context(), installation); err != nil {
			writeAiandAuthError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"disconnected": true})
	}
}

// writeAiandAuthError maps service errors onto the dashboard contract:
// invalid keys 401, gateway problems 502, everything else 500. Error strings
// are operator-oriented; none echo key material.
func writeAiandAuthError(c *gin.Context, err error) {
	log := observability.FromGin(c)
	switch {
	case errors.Is(err, auth.ErrInvalidAiandKey):
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
	case errors.Is(err, auth.ErrAiandGatewayUnavailable):
		log.Error("Aiand gateway request failed", "err", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "aiand_gateway_unavailable"})
	case errors.Is(err, auth.ErrAiandLoginPending):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "no_pending_login"})
	case errors.Is(err, auth.ErrAiandCredentialNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_connected"})
	default:
		log.Error("Aiand auth operation failed", "err", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete Aiand auth operation."})
	}
}

// formatSeconds renders a Retry-After duration as whole seconds.
func formatSeconds(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}
