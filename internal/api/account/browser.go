// Browser authorization-code login handlers: POST /auth/browser/start mints
// the flow (returns the gateway authorize URL and seals the flow state into a
// short-lived HttpOnly cookie), POST /auth/browser/exchange completes it from
// the gateway's redirect (?code&state on the origin) and mints the dashboard
// session. This file is the dashboard's fast path — the device flow stays as
// the fallback for gateways without browser authorization.
package account

import (
	"errors"
	"io"
	"net/http"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

// browserFlowTTLSeconds mirrors the service's loginBrowserTTL: the start
// response advertises the same window the sealed cookie's Max-Age carries.
const browserFlowTTLSeconds = 600

const (
	browserStateErrMsg = "invalid_state"
	browserRejectedMsg = "exchange_rejected"
)

// browserStartBody is the optional start request body; Next names the
// post-login destination. An absent body is allowed.
type browserStartBody struct {
	Next string `json:"next"`
}

// browserStartResponse hands the dashboard the gateway's authorize URL plus
// the flow window. The browser navigates to authorize_url; the gateway
// redirects back to this origin with ?code&state.
type browserStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	ExpiresIn    int    `json:"expires_in"`
}

// browserStart mints a browser authorization-code login. The Origin header
// (the frontend's own origin) is the redirect URI — the gateway only accepts
// registered loopback origins, so a missing or non-bare origin is 400 before
// the service is even called. Unsupported gateways answer 501
// browser_unsupported and do NOT count against the failure budget: the
// dashboard must fall back to the device flow, not get locked out.
func (h *Handlers) browserStart(c *gin.Context) {
	log := observability.FromGin(c)
	if h.limiter.limited(c) {
		log.Info("Account browser login start rejected: rate limited", "remote_ip", remotePeerIP(c))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too_many_attempts"})
		return
	}
	origin := c.GetHeader("Origin")
	if origin == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing_origin"})
		return
	}

	var body browserStartBody
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	start, err := h.svc.StartBrowserLogin(c.Request.Context(), origin, body.Next)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAiandBrowserUnsupported):
			log.Info("Account browser login start rejected: gateway does not support browser flow")
			c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "browser_unsupported"})
		case errors.Is(err, auth.ErrBrowserFlowState):
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_origin"})
		default:
			log.Error("Account browser login start failed", "err", err)
			h.limiter.noteFailure(c)
			writeGatewayError(c, err)
		}
		return
	}
	h.limiter.clear(c)
	setBrowserLoginCookie(c, start.Sealed, start.ExpiresIn)
	c.JSON(http.StatusOK, browserStartResponse{
		AuthorizeURL: start.AuthorizeURL,
		ExpiresIn:    start.ExpiresIn,
	})
}

// browserExchangeRequest carries the gateway redirect's query parameters.
type browserExchangeRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// browserExchangeResponse mirrors the device flow's authorized shape plus the
// post-login destination the start request asked for.
type browserExchangeResponse struct {
	Email   string `json:"email"`
	OrgName string `json:"org_name,omitempty"`
	Next    string `json:"next"`
}

// browserExchange completes the login from the gateway redirect. The sealed
// cookie is read once and ALWAYS cleared — success or any failure — so a
// spent or hostile callback cannot be replayed. State/flow failures are 400,
// a rejected code exchange is 401, gateway trouble 502; a minted session sets
// the same session cookie the device and paste flows use.
func (h *Handlers) browserExchange(c *gin.Context) {
	log := observability.FromGin(c)
	var req browserExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" || req.State == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": browserStateErrMsg})
		clearBrowserLoginCookie(c)
		return
	}

	sealed, err := c.Cookie(auth.LoginBrowserCookieName)
	if err != nil || len(sealed) == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": browserStateErrMsg})
		clearBrowserLoginCookie(c)
		return
	}

	result, err := h.svc.ExchangeBrowserLogin(c.Request.Context(), req.State, req.Code, []byte(sealed))
	clearBrowserLoginCookie(c)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrBrowserFlowState):
			log.Info("Account browser login exchange rejected: flow state invalid")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": browserStateErrMsg})
		case errors.Is(err, auth.ErrAiandInvalidGrant):
			log.Info("Account browser login exchange rejected: gateway refused the code")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": browserRejectedMsg})
		default:
			log.Error("Account browser login exchange failed", "err", err)
			writeGatewayError(c, err)
		}
		return
	}
	setSessionCookie(c, result.Grant.Token, result.Grant.ExpiresAt)
	c.JSON(http.StatusOK, browserExchangeResponse{
		Email:   result.Email,
		OrgName: result.OrgName,
		Next:    result.Next,
	})
}

// setBrowserLoginCookie mints the short-lived flow cookie. Max-Age follows
// the same TTL the service sealed into the blob, so the cookie can never
// outlive the ciphertext's expires_at.
func setBrowserLoginCookie(c *gin.Context, sealed []byte, expiresIn int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.LoginBrowserCookieName, string(sealed), expiresIn, "/", "", cookieSecure, true)
}

// clearBrowserLoginCookie expires the flow cookie. Called on every exchange
// outcome: the flow state is single-use by construction.
func clearBrowserLoginCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.LoginBrowserCookieName, "", -1, "/", "", cookieSecure, true)
}
