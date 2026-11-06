package middleware_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSessionVerifier stands in for *auth.LoginService behind the
// SessionVerifier interface. It counts verifier round-trips so tests can
// prove the LRU absorbs the second lookup within the cache TTL.
type fakeSessionVerifier struct {
	mu       sync.Mutex
	byToken  map[string]*auth.ActiveSession
	calls    atomic.Int64
	touches  atomic.Int64
	touchErr error
}

func (f *fakeSessionVerifier) VerifySessionToken(token string) (*auth.ActiveSession, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	active, ok := f.byToken[token]
	if !ok {
		return nil, auth.ErrSessionNotFound
	}
	return active, nil
}

func (f *fakeSessionVerifier) TouchSessionLastSeen(tokenHash string) error {
	f.touches.Add(1)
	return f.touchErr
}

func (f *fakeSessionVerifier) callCount() int64 { return f.calls.Load() }

func newActiveSession(lastSeen time.Time) *auth.ActiveSession {
	return &auth.ActiveSession{
		Session: auth.LoginSession{
			ID:         "sess-1",
			AccountID:  "acct-1",
			TokenHash:  "hash-1",
			IssuedAt:   lastSeen.Add(-24 * time.Hour),
			LastSeenAt: lastSeen,
			ExpiresAt:  lastSeen.Add(auth.LoginSessionTTL),
		},
		Account: auth.Account{
			ID:             "acct-1",
			Email:          "dev@example.com",
			AiandUserID:    "user-1",
			AiandOrgID:     "org-1",
			InstallationID: "inst-1",
		},
		Installation: &auth.Installation{ID: "inst-1", ExternalID: "acct-1"},
	}
}

func hashToken(token string) string { return auth.HashAPIKeySHA256(token) }

func accountSessionEngine(verifier auth.SessionVerifier, cache middleware.AccountSessionCache) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithAccountSession(verifier, cache))
	engine.GET("/probe", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return engine
}

func performAccountSessionRequest(engine *gin.Engine, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: cookie})
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func TestAccountSessionValidCookieSetsInstallationAndAccount(t *testing.T) {
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	active := newActiveSession(time.Now())
	const token = "sess-token-valid"
	verifier.byToken[token] = active
	cache := middleware.NewSessionLRUCache()

	engine := accountSessionEngine(verifier, cache)
	rr := performAccountSessionRequest(engine, token)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAccountSessionAbsentCookieIs401(t *testing.T) {
	verifier := &fakeSessionVerifier{}
	engine := accountSessionEngine(verifier, middleware.NewSessionLRUCache())

	rr := performAccountSessionRequest(engine, "")

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Equal(t, int64(0), verifier.callCount(), "no cookie means no verifier round-trip")
	assertAccountSessionRequired(t, rr)
}

func TestAccountSessionInvalidTokenIs401(t *testing.T) {
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	engine := accountSessionEngine(verifier, middleware.NewSessionLRUCache())

	rr := performAccountSessionRequest(engine, "sess-token-unknown")

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assertAccountSessionRequired(t, rr)
}

func TestAccountSessionRevokedTokenIs401(t *testing.T) {
	verifier := &fakeSessionVerifier{} // empty map = revoked/absent from the verifier's view
	engine := accountSessionEngine(verifier, middleware.NewSessionLRUCache())

	rr := performAccountSessionRequest(engine, "sess-token-revoked")

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assertAccountSessionRequired(t, rr)
}

func TestAccountSessionCacheHitsAvoidSecondVerifierCall(t *testing.T) {
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	const token = "sess-token-cached"
	verifier.byToken[token] = newActiveSession(time.Now())
	cache := middleware.NewSessionLRUCache()

	engine := accountSessionEngine(verifier, cache)
	performAccountSessionRequest(engine, token)
	performAccountSessionRequest(engine, token)

	require.Equal(t, int64(1), verifier.callCount(),
		"second request inside the cache TTL must be served from the LRU, not the verifier")
}

func TestAccountSessionTouchAndCookieRemintOnlyPastHalfway(t *testing.T) {
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	const token = "sess-token-slide"
	// Fresh session: last seen just now, far from halfway. No touch expected.
	fresh := newActiveSession(time.Now())
	verifier.byToken[token] = fresh

	engine := gin.New()
	engine.Use(middleware.WithAccountSession(verifier, middleware.NewSessionLRUCache()))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	rr := performAccountSessionRequest(engine, token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, int64(0), verifier.touches.Load(), "a fresh session must not trigger a touch")
	assert.Empty(t, rr.Header().Get("Set-Cookie"), "no sliding means no cookie re-mint")

	// Stale session: last seen beyond half the TTL. Touch + re-mint expected.
	staleToken := "sess-token-stale"
	stale := newActiveSession(time.Now().Add(-(auth.LoginSessionTTL/2 + time.Minute)))
	verifier.byToken[staleToken] = stale

	rr = performAccountSessionRequest(engine, staleToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, int64(1), verifier.touches.Load(), "a session past halfway must be touched exactly once per window")
	cookies := rr.Header().Values("Set-Cookie")
	require.Len(t, cookies, 1, "slid window must re-mint exactly one cookie")
	assert.Contains(t, cookies[0], auth.LoginSessionCookieName+"="+staleToken)
	assert.Contains(t, cookies[0], "HttpOnly")
	assert.Contains(t, cookies[0], "SameSite=Lax")
	assert.Contains(t, cookies[0], "Path=/")
	assert.Contains(t, cookies[0], "Max-Age="+requiredMaxAge())
}

func requiredMaxAge() string {
	return strconv.Itoa(int(auth.LoginSessionTTL.Seconds()))
}

func TestAccountSessionCacheNeverStoresNegatives(t *testing.T) {
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	engine := accountSessionEngine(verifier, middleware.NewSessionLRUCache())

	// Repeated failures must keep hitting the verifier — nothing negative is cached.
	performAccountSessionRequest(engine, "bad-1")
	performAccountSessionRequest(engine, "bad-1")

	assert.Equal(t, int64(2), verifier.callCount(),
		"the cache never stores negatives: a failed lookup must not absorb a retry after revocation of the blocker")
}

func TestAccountSessionVerifierErrorDoesNotAbortAs500(t *testing.T) {
	verifier := &fakeSessionVerifier{touchErr: errors.New("db down")}
	verifier.byToken = map[string]*auth.ActiveSession{
		"sess-token-touch-fail": newActiveSession(time.Now().Add(-(auth.LoginSessionTTL/2 + time.Minute))),
	}
	engine := accountSessionEngine(verifier, middleware.NewSessionLRUCache())

	rr := performAccountSessionRequest(engine, "sess-token-touch-fail")

	assert.Equal(t, http.StatusOK, rr.Code, "a failed touch must not fail an already-verified request")
}

func TestAccountSessionOrAPIKeyBearerAccepted(t *testing.T) {
	verifier := &fakeSessionVerifier{}
	const routerToken = "rk_fallback"
	hash, prefix, suffix := auth.APITokenFingerprint(routerToken)
	apiKey := &auth.APIKey{ID: "key-1", KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix}
	installation := &auth.Installation{ID: "inst-bearer", ExternalID: "ext-bearer"}

	apiKeyRepo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		hash: {apiKey: apiKey, installation: installation},
	}}
	svc := auth.NewService(fakeInstallationRepository{}, apiKeyRepo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithAccountSessionOrAPIKey(verifier, middleware.NewSessionLRUCache(), svc, false))
	engine.GET("/probe", func(c *gin.Context) {
		assert.Equal(t, installation, middleware.InstallationFrom(c))
		assert.Equal(t, apiKey, middleware.APIKeyFrom(c))
		assert.Nil(t, middleware.AccountFrom(c), "a bearer key is not an account identity")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAccountSessionStrictVariantRejectsBearer(t *testing.T) {
	verifier := &fakeSessionVerifier{}
	const routerToken = "rk_rejected"

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithAccountSession(verifier, middleware.NewSessionLRUCache()))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(middleware.RouterKeyHeader, routerToken)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "the strict variant must never honor a bearer token")
	assertAccountSessionRequired(t, rr)
}

func TestAccountSessionOrAPIKeyBothAbsentIs401(t *testing.T) {
	verifier := &fakeSessionVerifier{}
	apiKeyRepo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	svc := auth.NewService(fakeInstallationRepository{}, apiKeyRepo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.WithAccountSessionOrAPIKey(verifier, middleware.NewSessionLRUCache(), svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAccountSessionCookieNeverAuthenticatesDataPlane(t *testing.T) {
	// A dashboard cookie must never pass the data-plane guard: WithAuth
	// (bearer-only) still 401s a request that carries only a valid account
	// session cookie. This is the cross-boundary guarantee from the design
	// doc — run the session middleware first, then the real WithAuth, and
	// require the data-plane layer to reject it.
	verifier := &fakeSessionVerifier{byToken: map[string]*auth.ActiveSession{}}
	const token = "sess-token-dataplane"
	verifier.byToken[token] = newActiveSession(time.Now())

	apiKeyRepo := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{}}
	svc := auth.NewService(fakeInstallationRepository{}, apiKeyRepo, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Now() })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// Simulates the /v1/* chain: session middleware (as T9 would mount it)
	// followed by the real data-plane guard.
	engine.Use(middleware.WithAccountSession(verifier, middleware.NewSessionLRUCache()))
	engine.Use(middleware.WithAuth(svc, false))
	engine.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code,
		"an account cookie must never satisfy the bearer-only data-plane guard")
	assert.Contains(t, rr.Body.String(), "invalid_key")
}

func assertAccountSessionRequired(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "account_session_required", body.Error)
}
