package account

import (
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"aiand-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	// loginMaxFailures is the per-IP failure budget inside one window; the
	// next attempt past it is rejected with 429.
	loginMaxFailures = 5
	// loginFailureTTL is how long a failure stays counted against an IP.
	loginFailureTTL = 5 * time.Minute
	// loginMaxTrackedIPs bounds memory: 1024 unique peers is plenty for one
	// self-hosted router; LRU evicts the oldest so a flood can't blow up the
	// heap. Pattern moved from internal/auth/admin.go's admin login limiter.
	loginMaxTrackedIPs = 1024
)

// loginFailureLimiter is the per-IP failure LRU: counts failed login
// attempts by raw TCP peer and blocks the 6th attempt within the window.
// Failures counted: rejected keys (paste) and failed device starts. Polls
// are gateway-governed and never count.
type loginFailureLimiter struct {
	mu    sync.Mutex
	lru   *expirable.LRU[string, int]
	getIP func(*gin.Context) string // overridable in tests
}

func newLoginFailureLimiter() *loginFailureLimiter {
	return &loginFailureLimiter{
		lru:   expirable.NewLRU[string, int](loginMaxTrackedIPs, nil, loginFailureTTL),
		getIP: remotePeerIP,
	}
}

// limited reports whether the peer has exhausted its failure budget.
func (l *loginFailureLimiter) limited(c *gin.Context) bool {
	ip := l.getIP(c)
	if ip == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	count, ok := l.lru.Get(ip)
	return ok && count >= loginMaxFailures
}

// noteFailure records one failed attempt against the peer.
func (l *loginFailureLimiter) noteFailure(c *gin.Context) {
	ip := l.getIP(c)
	if ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	count, _ := l.lru.Get(ip)
	l.lru.Add(ip, count+1)
}

// clear removes the peer's failure count after a success.
func (l *loginFailureLimiter) clear(c *gin.Context) {
	ip := l.getIP(c)
	if ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lru.Remove(ip)
}

// remotePeerIP returns the immediate TCP peer's IP so rate limiting can't be
// bypassed by spoofing X-Forwarded-For (c.ClientIP trusts it). Returns the
// unstripped address if the peer lacks "host:port" shape.
func remotePeerIP(c *gin.Context) string {
	addr := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// cookieSecure controls whether account session cookies are minted with the
// Secure flag. Default true so self-hosters never accidentally serve
// plaintext cookies; dev behind non-HTTPS can set ROUTER_COOKIE_INSECURE=true.
// Not derived from X-Forwarded-Proto: that header is attacker-controlled when
// the router is reached directly, and a wrong value silently downgrades
// cookie transport. Same policy as internal/api/admin's admin cookie.
var cookieSecure = cookieSecureFromEnv()

func cookieSecureFromEnv() bool {
	return os.Getenv("ROUTER_COOKIE_INSECURE") != "true"
}

// CookieSecure reports the live Secure-flag policy.
func CookieSecure() bool { return cookieSecure }

// ConfigureCookieSecurityFromEnv re-reads ROUTER_COOKIE_INSECURE into the
// cookie Secure flag. Package init calls the same logic once; this exported
// form exists so tests can flip the env and re-derive the flag (and so an
// embedder that mounts these handlers can re-apply policy after env setup).
func ConfigureCookieSecurityFromEnv() {
	cookieSecure = os.Getenv("ROUTER_COOKIE_INSECURE") != "true"
}

// setSessionCookie mints the dashboard session cookie. Max-Age follows the
// session row's expires_at so the cookie never outlives its revocable row.
func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.LoginSessionCookieName, token, maxAge, "/", "", cookieSecure, true)
}

// clearSessionCookie expires the session cookie in the browser. It does not
// revoke the session row — logout does that via LoginAPI.Logout first.
func clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.LoginSessionCookieName, "", -1, "/", "", cookieSecure, true)
}
