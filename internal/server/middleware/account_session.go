package middleware

import (
	"net/http"
	"os"
	"strings"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

const ctxKeyAccount = "router_account_session_account"

// sessionCacheSize and sessionCacheTTL bound the short-lived positive cache
// in front of SessionVerifier.VerifySessionToken. A revoked session may keep
// authenticating for at most one TTL after revocation.
const (
	sessionCacheSize = 1024
	sessionCacheTTL  = 30 * time.Second
)

// cookieSecure controls whether account session cookies are re-minted with
// the Secure flag. Default true so self-hosters never accidentally serve
// plaintext cookies; dev behind non-HTTPS can set ROUTER_COOKIE_INSECURE=true.
// Not derived from X-Forwarded-Proto: that header is attacker-controlled when
// the router is reached directly, and a wrong value silently downgrades cookie
// transport. Mirrors internal/api/admin/auth.go's flag so both minters agree.
var cookieSecure = os.Getenv("ROUTER_COOKIE_INSECURE") != "true"

// AccountSessionCache is the middleware-facing surface of the positive
// session cache. Production uses the built-in expirable LRU; tests inject a
// fake.
type AccountSessionCache interface {
	Get(tokenHash string) (*auth.ActiveSession, bool)
	Put(tokenHash string, s *auth.ActiveSession)
}

// NewSessionLRUCache builds the production positive-only session cache:
// bounded to sessionCacheSize entries, entries expire after sessionCacheTTL.
func NewSessionLRUCache() AccountSessionCache {
	lru := expirable.NewLRU[string, *auth.ActiveSession](sessionCacheSize, nil, sessionCacheTTL)
	return &expirableSessionCache{lru: lru}
}

type expirableSessionCache struct {
	lru *expirable.LRU[string, *auth.ActiveSession]
}

func (c *expirableSessionCache) Get(tokenHash string) (*auth.ActiveSession, bool) {
	return c.lru.Get(tokenHash)
}

func (c *expirableSessionCache) Put(tokenHash string, s *auth.ActiveSession) {
	c.lru.Add(tokenHash, s)
}

// noopSessionCache is the Null Object used when caching is disabled: every
// Get misses, Put is discarded.
type noopSessionCache struct{}

func (noopSessionCache) Get(string) (*auth.ActiveSession, bool) { return nil, false }
func (noopSessionCache) Put(string, *auth.ActiveSession)        {}

// WithAccountSession authenticates an account session cookie (the dashboard
// identity, never a data-plane credential): reads auth.LoginSessionCookieName,
// verifies it through the supplied auth.SessionVerifier, and stashes BOTH the
// resolved installation (ctxKeyInstallation — the same accessor WithAuth uses,
// so every resolveInstallation-based handler works unchanged) and the account
// (AccountFrom) on the request context. It never auto-creates accounts or
// installations: the verifier returns fully resolved rows or an error.
//
// Missing, invalid, expired, or revoked sessions short-circuit 401
// {"error":"account_session_required"} — the dashboard's fetch layer bounces
// to login.
//
// Sliding expiry: when a verified session is past halfway through its
// auth.LoginSessionTTL window, the middleware touches the session (sliding
// LastSeenAt/ExpiresAt) and re-mints the cookie with a fresh Max-Age using the
// same flags the login handler sets (HttpOnly, SameSite=Lax, Path=/, Secure
// unless ROUTER_COOKIE_INSECURE=true). The touch fires at most once per
// half-TTL window; requests inside that window skip it entirely so the write
// stays off the hot path.
//
// Caching: verified sessions are positively cached for sessionCacheTTL. The
// cache never stores negatives, so revocation latency is bounded at one TTL:
// a revoked session may still authenticate for up to sessionCacheTTL after
// the revoke commits, then fails on the next verifier round-trip.
func WithAccountSession(verifier auth.SessionVerifier, cache AccountSessionCache) gin.HandlerFunc {
	return withAccountSession(verifier, cache, false)
}

// WithAccountSessionOrAPIKey accepts EITHER a valid account session cookie OR
// a bearer rk_/ra_ token. Session cookie is tried first; on the 401 path it
// falls through to the same bearer branch WithAuth uses (withAPIKey), so the
// bearer route carries identical key semantics, ctx keys, and error shapes.
// Separate constructor on purpose: no bool flag decides the mode at the call
// site.
//
// Keep this off /v1/* data-plane routes: an account cookie authenticates
// dashboard surfaces only, and a bearer key must not gain dashboard
// capabilities through this middleware.
func WithAccountSessionOrAPIKey(verifier auth.SessionVerifier, cache AccountSessionCache, svc *auth.Service, byokRequiresOptIn bool) gin.HandlerFunc {
	sessionMW := withAccountSession(verifier, cache, true)
	apiKeyMW := withAPIKey(svc, byokRequiresOptIn)
	return func(c *gin.Context) {
		sessionMW(c)
		if c.IsAborted() {
			return
		}
		// No session identity on the request: fall through to bearer.
		// withAPIKey fully owns the response from here (sets
		// ctxKeyInstallation/ctxKeyAPIKey on success, 401/503 on failure).
		apiKeyMW(c)
	}
}

// withAccountSession is the shared body of WithAccountSession and the session
// branch of WithAccountSessionOrAPIKey. With fallback true, a failed session
// lookup writes nothing so the caller can fall through to bearer auth; with
// fallback false it short-circuits 401 account_session_required.
func withAccountSession(verifier auth.SessionVerifier, cache AccountSessionCache, fallback bool) gin.HandlerFunc {
	if cache == nil {
		cache = noopSessionCache{}
	}
	return func(c *gin.Context) {
		token, err := c.Cookie(auth.LoginSessionCookieName)
		if err == nil {
			token = strings.TrimSpace(token)
		}
		if err != nil || token == "" {
			abortAccountSessionRequired(c, fallback)
			return
		}
		tokenHash := auth.HashAPIKeySHA256(token)

		var (
			active *auth.ActiveSession
			cached bool
		)
		active, cached = cache.Get(tokenHash)
		if !cached {
			active, err = verifier.VerifySessionToken(token)
			if err != nil {
				observability.FromGin(c).Debug("Account session verify failed", "err", err)
				abortAccountSessionRequired(c, fallback)
				return
			}
			cache.Put(tokenHash, active)
		}

		c.Set(ctxKeyInstallation, active.Installation)
		c.Set(ctxKeyAccount, active.Account)

		slideAccountSession(c, verifier, token, tokenHash, &active.Session)
		c.Next()
	}
}

// slideAccountSession re-mints the cookie and touches the session row when
// the session is past halfway through its sliding window. The LastSeenAt age
// check is the cheap guard that keeps the write off the hot path: a session
// touched within the last half-TTL skips both the DB write and the cookie
// re-mint entirely. For requests served from the cache the entry's LastSeenAt
// is frozen, so the touch can repeat for at most one cache TTL after a
// halfway crossing before a fresh verifier read sees the slid timestamp.
func slideAccountSession(c *gin.Context, verifier auth.SessionVerifier, token, tokenHash string, session *auth.LoginSession) {
	now := time.Now()
	halfTTL := auth.LoginSessionTTL / 2
	seenAge := now.Sub(session.LastSeenAt)
	if seenAge < halfTTL {
		return
	}
	if err := verifier.TouchSessionLastSeen(tokenHash); err != nil {
		// A failed touch must not fail the request: the session is still
		// live until its stored ExpiresAt; the next eligible request retries.
		observability.FromGin(c).Error("Account session touch failed", "err", err, "session_id", session.ID)
		return
	}
	// Re-mint the cookie so the browser's Max-Age tracks the slid window.
	// The token value is unchanged — the cookie identifies the session; only
	// its lifetime slides.
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.LoginSessionCookieName, token, int(auth.LoginSessionTTL.Seconds()), "/", "", cookieSecure, true)
}

// abortAccountSessionRequired is the uniform 401 for missing, invalid,
// expired, and revoked account sessions. The dashboard's fetch layer reads
// this code and bounces to login. With fallback set, nothing is written so
// WithAccountSessionOrAPIKey can hand the request to the bearer branch.
func abortAccountSessionRequired(c *gin.Context, fallback bool) {
	if fallback {
		return
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "account_session_required"})
}

// AccountFrom retrieves the account set by WithAccountSession. Returns nil
// for unauthenticated requests and for bearer-keyed requests on the
// WithAccountSessionOrAPIKey fall-through (a key is not an account).
func AccountFrom(c *gin.Context) *auth.Account {
	v, ok := c.Get(ctxKeyAccount)
	if !ok {
		return nil
	}
	acct, _ := v.(*auth.Account)
	return acct
}
