// Package account provides the SaaS dashboard login HTTP surface mounted
// under /account/v1 in the selfhosted block: device-flow login (start/poll),
// pasted Aiand key login, session introspection, and logout. It is
// deliberately unauthenticated — the first-ever visitor bootstraps here — so
// every route that mutates session state is rate-limited per raw TCP peer.
//
// The handlers depend on a narrow interface satisfied by *auth.LoginService;
// see LoginAPI. All responses are dashboard-facing summaries: the pasted
// key, device code, and session token never appear in a body or log line
// (the token reaches the client only as the HttpOnly cookie value).
package account

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// LoginAPI is the handler-facing method set of the login service. Kept narrow
// so handler tests can fake it without the repositories, and so this package
// never depends on *auth.LoginService's wiring or storage seams.
type LoginAPI interface {
	StartDeviceLogin(ctx context.Context) (auth.LoginDeviceStart, error)
	PollDeviceLogin(ctx context.Context, loginID string) (auth.LoginPollOutcome, error)
	LoginWithKey(ctx context.Context, key string) (auth.LoginSessionGrant, error)
	Logout(ctx context.Context, token string) error
	VerifySessionToken(token string) (*auth.ActiveSession, error)
}

// Handlers bundles the /account/v1 auth handlers over one LoginAPI. The
// failure limiter is shared: device start and paste draw on the same per-IP
// failure budget.
type Handlers struct {
	svc     LoginAPI
	limiter *loginFailureLimiter
}

// NewHandlers builds the handler set. svc is *auth.LoginService in
// production; tests supply a fake.
func NewHandlers(svc LoginAPI) *Handlers {
	return &Handlers{svc: svc, limiter: newLoginFailureLimiter()}
}

// Register mounts the five auth routes on the given group (engine.Group
// ("/account/v1")). The group carries no auth middleware by design.
func (h *Handlers) Register(group *gin.RouterGroup) {
	authGroup := group.Group("/auth")
	authGroup.POST("/device/start", h.deviceStart)
	authGroup.POST("/device/poll", h.devicePoll)
	authGroup.POST("/paste", h.paste)
	authGroup.GET("/me", h.me)
	authGroup.POST("/logout", h.logout)
}

type deviceStartResponse struct {
	LoginID                 string `json:"login_id"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceStart opens a device-flow login. Rate-limited per IP: a start failure
// (gateway down, wiring gap) counts against the peer so a broken deployment
// can't be hammered; a success clears the peer's failures.
func (h *Handlers) deviceStart(c *gin.Context) {
	log := observability.FromGin(c)
	if h.limiter.limited(c) {
		log.Info("Account device login start rejected: rate limited", "remote_ip", remotePeerIP(c))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too_many_attempts"})
		return
	}
	login, err := h.svc.StartDeviceLogin(c.Request.Context())
	if err != nil {
		log.Error("Account device login start failed", "err", err)
		h.limiter.noteFailure(c)
		writeGatewayError(c, err)
		return
	}
	h.limiter.clear(c)
	c.JSON(http.StatusOK, deviceStartResponse{
		LoginID:                 login.LoginID,
		UserCode:                login.UserCode,
		VerificationURI:         login.VerificationURI,
		VerificationURIComplete: login.VerificationURIComplete,
		ExpiresIn:               login.ExpiresIn,
		Interval:                login.Interval,
	})
}

type devicePollRequest struct {
	LoginID string `json:"login_id"`
}

type devicePollResponse struct {
	Status  string `json:"status"`
	Email   string `json:"email,omitempty"`
	OrgName string `json:"org_name,omitempty"`
}

// devicePoll exchanges one poll against the gateway. The dashboard owns the
// waiting loop: pending keeps it polling, and a 428 (slow_down) carries the
// raised interval in Retry-After — the only slow-down channel (T2 contract;
// no retry_after_seconds body field). An authorized poll sets the session
// cookie. Not rate-limited here: the gateway governs the poll cadence.
func (h *Handlers) devicePoll(c *gin.Context) {
	var req devicePollRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.LoginID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing_login_id"})
		return
	}
	outcome, err := h.svc.PollDeviceLogin(c.Request.Context(), req.LoginID)
	if err != nil {
		var slow *auth.AiandSlowDownError
		if errors.As(err, &slow) {
			c.Header("Retry-After", formatSeconds(slow.RetryAfter))
			c.JSON(http.StatusPreconditionRequired, gin.H{"error": "slow_down"})
			return
		}
		observability.FromGin(c).Error("Account device poll failed", "err", err)
		writePollError(c, err)
		return
	}
	resp := devicePollResponse{Status: outcome.Status}
	if outcome.Status == auth.AiandPollStatusAuthorized && outcome.Session != nil {
		setSessionCookie(c, outcome.Session.Token, outcome.Session.ExpiresAt)
		resp.Email = outcome.Email
		resp.OrgName = outcome.OrgName
	}
	c.JSON(http.StatusOK, resp)
}

type pasteRequest struct {
	Key string `json:"key"`
}

type pasteResponse struct {
	Email   string `json:"email"`
	OrgName string `json:"org_name"`
}

// paste completes a login with a directly pasted Aiand sk- key. Invalid keys
// count against the peer's failure budget (6th attempt within the window is
// 429); a success clears it. All rejection paths return the same 401 body so
// the response leaks nothing about which half of the key check failed.
func (h *Handlers) paste(c *gin.Context) {
	var req pasteRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing_key"})
		return
	}
	log := observability.FromGin(c)
	if h.limiter.limited(c) {
		log.Info("Account paste login rejected: rate limited", "remote_ip", remotePeerIP(c))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too_many_attempts"})
		return
	}
	grant, err := h.svc.LoginWithKey(c.Request.Context(), req.Key)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidAiandKey), errors.Is(err, auth.ErrAiandInvalidKey):
			log.Info("Account paste login rejected: invalid key", "remote_ip", remotePeerIP(c))
			h.limiter.noteFailure(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
		default:
			log.Error("Account paste login failed", "err", err)
			writeGatewayError(c, err)
		}
		return
	}
	h.limiter.clear(c)
	setSessionCookie(c, grant.Token, grant.ExpiresAt)
	// org_name rides the device flow's gateway token; the account row (T6
	// schema) persists only the opaque org id, so paste has no display name
	// to return. Field stays in the contract shape, empty when unknown.
	c.JSON(http.StatusOK, pasteResponse{Email: grant.Account.Email})
}

type meResponse struct {
	Authenticated bool             `json:"authenticated"`
	Email         string           `json:"email,omitempty"`
	OrgName       string           `json:"org_name,omitempty"`
	Installation  *installationRef `json:"installation,omitempty"`
}

type installationRef struct {
	ExternalID string `json:"external_id"`
}

// me reports the caller's session state. It answers 200 {authenticated:false}
// when there is no live session — NEVER 401, which would trip the frontend's
// global 401 bounce on the login page itself.
func (h *Handlers) me(c *gin.Context) {
	cookie, err := c.Cookie(auth.LoginSessionCookieName)
	if err != nil || cookie == "" {
		c.JSON(http.StatusOK, meResponse{Authenticated: false})
		return
	}
	session, err := h.svc.VerifySessionToken(cookie)
	if err != nil {
		if !errors.Is(err, auth.ErrSessionNotFound) {
			observability.FromGin(c).Error("Account session verify errored", "err", err)
		}
		clearSessionCookie(c)
		c.JSON(http.StatusOK, meResponse{Authenticated: false})
		return
	}
	resp := meResponse{
		Authenticated: true,
		Email:         session.Account.Email,
	}
	if session.Installation != nil {
		resp.Installation = &installationRef{ExternalID: session.Installation.ExternalID}
	}
	c.JSON(http.StatusOK, resp)
}

// logout revokes the session row by cookie token hash (not a cookie-only
// clear: a stolen cookie must die here) and then clears the cookie.
// Idempotent: no cookie or unknown token still yields 200.
func (h *Handlers) logout(c *gin.Context) {
	if cookie, err := c.Cookie(auth.LoginSessionCookieName); err == nil && cookie != "" {
		if err := h.svc.Logout(c.Request.Context(), cookie); err != nil {
			observability.FromGin(c).Error("Account logout failed", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "logout_failed"})
			return
		}
	}
	clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// writeGatewayError maps login-service gateway failures onto the dashboard
// contract: gateway problems 502, unknown login attempt 404, everything else
// 500. Error bodies are coarse; none echo key or token material.
func writeGatewayError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrAiandGatewayUnavailable):
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "gateway_unavailable"})
	default:
		observability.FromGin(c).Error("Account login operation failed", "err", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "login_failed"})
	}
}

func writePollError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrLoginAttemptNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "login_not_found"})
	case errors.Is(err, auth.ErrAiandGatewayUnavailable):
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "gateway_unavailable"})
	default:
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "poll_failed"})
	}
}

// formatSeconds renders a Retry-After duration as whole seconds, rounding up
// so a sub-second remainder never tells the client to poll too early.
func formatSeconds(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}
