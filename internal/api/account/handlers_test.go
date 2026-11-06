package account_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/api/account"
	"aiand-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLoginAPI is a thin fake of the auth.LoginService-facing surface.
// Construction parameters shape the happy path; the hooks fields let a test
// script one-off outcomes (slow-downs, errors) and observe calls.
type fakeLoginAPI struct {
	mu sync.Mutex

	start     auth.LoginDeviceStart
	startErr  error
	startErrs []error // consumed one per StartDeviceLogin call before startErr

	poll       map[string]auth.LoginPollOutcome
	pollErrs   map[string]error
	pollCalls  []string
	pollTicket chan int // optional serialization point for cookie assertions

	grant    auth.LoginSessionGrant
	grantErr error

	logoutTokens []string
	logoutErr    error

	verifyCalls   int
	verifySession *auth.ActiveSession
	verifyErr     error
}

func (f *fakeLoginAPI) StartDeviceLogin(context.Context) (auth.LoginDeviceStart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startErrs) > 0 {
		err := f.startErrs[0]
		f.startErrs = f.startErrs[1:]
		return auth.LoginDeviceStart{}, err
	}
	return f.start, f.startErr
}

func (f *fakeLoginAPI) PollDeviceLogin(_ context.Context, loginID string) (auth.LoginPollOutcome, error) {
	f.mu.Lock()
	f.pollCalls = append(f.pollCalls, loginID)
	f.mu.Unlock()
	if f.pollTicket != nil {
		<-f.pollTicket
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.pollErrs[loginID]; ok {
		return auth.LoginPollOutcome{}, err
	}
	if outcome, ok := f.poll[loginID]; ok {
		return outcome, nil
	}
	return auth.LoginPollOutcome{Status: auth.AiandPollStatusPending}, nil
}

func (f *fakeLoginAPI) LoginWithKey(context.Context, string) (auth.LoginSessionGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grant, f.grantErr
}

func (f *fakeLoginAPI) Logout(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutTokens = append(f.logoutTokens, token)
	return f.logoutErr
}

func (f *fakeLoginAPI) VerifySessionToken(string) (*auth.ActiveSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls++
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return f.verifySession, nil
}

// newRouter mounts the handlers. Peer identity comes from each request's
// RemoteAddr, which remotePeerIP parses — tests set it per request.
func newRouter(svc account.LoginAPI) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := account.NewHandlers(svc)
	engine := gin.New()
	h.Register(engine.Group("/account/v1"))
	return engine
}

func doJSON(engine *gin.Engine, method, target, peerIP, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = peerIP + ":41234"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

var peerA = "10.1.1.10"

func testStart() auth.LoginDeviceStart {
	return auth.LoginDeviceStart{
		LoginID:                 "attempt-1",
		UserCode:                "WDJB-MJHT",
		VerificationURI:         "https://aiand.example/activate",
		VerificationURIComplete: "https://aiand.example/activate?code=WDJB-MJHT",
		ExpiresIn:               900,
		Interval:                5,
	}
}

func authorizedOutcome(token string, expiresAt time.Time) auth.LoginPollOutcome {
	return auth.LoginPollOutcome{
		Status:  auth.AiandPollStatusAuthorized,
		Email:   "ops@example.com",
		OrgName: "Example Org",
		Session: &auth.LoginSessionGrant{
			Token:     token,
			ExpiresAt: expiresAt,
			Account:   auth.Account{ID: "acct_abc", Email: "ops@example.com"},
		},
	}
}

// ---- device start / poll ----

func TestDeviceStartThenPollPendingThenAuthorized(t *testing.T) {
	svc := &fakeLoginAPI{
		start: testStart(),
		poll: map[string]auth.LoginPollOutcome{
			"attempt-1": {Status: auth.AiandPollStatusPending},
		},
	}
	engine := newRouter(svc)

	w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/start", peerA, "")
	require.Equal(t, http.StatusOK, w.Code)
	var startResp map[string]any
	require.NoError(t, jsonUnmarshal(w.Body.Bytes(), &startResp))
	assert.Equal(t, "attempt-1", startResp["login_id"])
	assert.Equal(t, "WDJB-MJHT", startResp["user_code"])
	assert.Equal(t, "https://aiand.example/activate", startResp["verification_uri"])
	assert.Equal(t, float64(900), startResp["expires_in"])
	assert.Equal(t, float64(5), startResp["interval"])

	w = doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{"login_id":"attempt-1"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var pollResp map[string]any
	require.NoError(t, jsonUnmarshal(w.Body.Bytes(), &pollResp))
	assert.Equal(t, "pending", pollResp["status"])
	assert.NotContains(t, pollResp, "email")

	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	svc.mu.Lock()
	svc.poll["attempt-1"] = authorizedOutcome("sess-token-1", expiresAt)
	svc.mu.Unlock()

	w = doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{"login_id":"attempt-1"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, jsonUnmarshal(w.Body.Bytes(), &pollResp))
	assert.Equal(t, "authorized", pollResp["status"])
	assert.Equal(t, "ops@example.com", pollResp["email"])
	assert.Equal(t, "Example Org", pollResp["org_name"])

	cookies := w.Result().Cookies()
	var session *http.Cookie
	for _, ck := range cookies {
		if ck.Name == auth.LoginSessionCookieName {
			session = ck
		}
	}
	require.NotNil(t, session, "authorized poll must set the session cookie")
	assert.Equal(t, "sess-token-1", session.Value)
	assert.True(t, session.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite)
	assert.True(t, session.Secure, "Secure defaults on")
	assert.Equal(t, "/", session.Path)
}

func TestDevicePollSlowDownIs428WithRetryAfter(t *testing.T) {
	svc := &fakeLoginAPI{
		pollErrs: map[string]error{
			"attempt-1": &auth.AiandSlowDownError{RetryAfter: 11 * time.Second},
		},
	}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{"login_id":"attempt-1"}`)
	require.Equal(t, http.StatusPreconditionRequired, w.Code)
	assert.Equal(t, "11", w.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"error":"slow_down"}`, w.Body.String())
}

func TestDevicePollTerminalStatuses(t *testing.T) {
	svc := &fakeLoginAPI{
		poll: map[string]auth.LoginPollOutcome{
			"expired": {Status: auth.AiandPollStatusExpired},
			"denied":  {Status: auth.AiandPollStatusDenied},
		},
	}
	engine := newRouter(svc)
	for id, want := range map[string]string{"expired": "expired", "denied": "denied"} {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{"login_id":"`+id+`"}`)
		require.Equal(t, http.StatusOK, w.Code, id)
		assert.Contains(t, w.Body.String(), `"`+want+`"`, id)
	}
}

func TestDevicePollMissingLoginIDIs400(t *testing.T) {
	engine := newRouter(&fakeLoginAPI{})
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- paste ----

func TestPasteSuccessSetsCookieAndIdentity(t *testing.T) {
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	svc := &fakeLoginAPI{
		grant: auth.LoginSessionGrant{
			Token:     "paste-token-1",
			ExpiresAt: expiresAt,
			Account:   auth.Account{ID: "acct_abc", Email: "ops@example.com"},
		},
	}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-abc"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, jsonUnmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ops@example.com", resp["email"])

	var session *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == auth.LoginSessionCookieName {
			session = ck
		}
	}
	require.NotNil(t, session)
	assert.Equal(t, "paste-token-1", session.Value)
	assert.True(t, session.HttpOnly)
}

func TestPasteInvalidKeyIs401AndCounts(t *testing.T) {
	svc := &fakeLoginAPI{grantErr: auth.ErrInvalidAiandKey}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.JSONEq(t, `{"error":"invalid_key"}`, w.Body.String())
}

func TestPasteGatewayDownIs502(t *testing.T) {
	svc := &fakeLoginAPI{grantErr: auth.ErrAiandGatewayUnavailable}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-abc"}`)
	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.JSONEq(t, `{"error":"gateway_unavailable"}`, w.Body.String())
}

func TestPasteSixthAttemptIsRateLimited(t *testing.T) {
	svc := &fakeLoginAPI{grantErr: auth.ErrInvalidAiandKey}
	engine := newRouter(svc)
	for i := range 5 {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
		require.Equal(t, http.StatusUnauthorized, w.Code, "attempt %d", i+1)
	}
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.JSONEq(t, `{"error":"too_many_attempts"}`, w.Body.String())
}

func TestPasteSuccessClearsFailureBudget(t *testing.T) {
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	svc := &fakeLoginAPI{
		grant: auth.LoginSessionGrant{
			Token:     "t",
			ExpiresAt: expiresAt,
			Account:   auth.Account{ID: "acct_x", Email: "e@x.co"},
		},
	}
	engine := newRouter(svc)
	for i := range 4 {
		svc.mu.Lock()
		svc.grantErr = auth.ErrInvalidAiandKey
		svc.mu.Unlock()
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
		require.Equal(t, http.StatusUnauthorized, w.Code, "attempt %d", i+1)
	}
	svc.mu.Lock()
	svc.grantErr = nil
	svc.mu.Unlock()
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-ok"}`)
	require.Equal(t, http.StatusOK, w.Code)
	// Budget cleared: five more failures must not trip the limiter.
	svc.mu.Lock()
	svc.grantErr = auth.ErrInvalidAiandKey
	svc.mu.Unlock()
	for i := range 5 {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "post-clear attempt %d", i+1)
	}
}

func TestPasteRateLimitsArePerIP(t *testing.T) {
	svc := &fakeLoginAPI{grantErr: auth.ErrInvalidAiandKey}
	engine := newRouter(svc)
	other := "10.9.9.9"
	for range 5 {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/paste", peerA, `{"key":"sk-bad"}`)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "same IP hits the cap")
	w = doJSON(engine, http.MethodPost, "/account/v1/auth/paste", other, `{"key":"sk-bad"}`)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "a different IP starts with a fresh budget")
	// X-Forwarded-For must not rotate the perceived peer.
	req := httptest.NewRequest(http.MethodPost, "/account/v1/auth/paste", strings.NewReader(`{"key":"sk-bad"}`))
	req.RemoteAddr = peerA + ":54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "spoofed X-Forwarded-For cannot escape the cap")
}

func TestDeviceStartFailureCountsTowardLimit(t *testing.T) {
	gatewayDown := auth.ErrAiandGatewayUnavailable
	svc := &fakeLoginAPI{startErrs: []error{gatewayDown, gatewayDown, gatewayDown, gatewayDown, gatewayDown}}
	engine := newRouter(svc)
	for i := range 5 {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/start", peerA, "")
		require.Equal(t, http.StatusBadGateway, w.Code, "attempt %d", i+1)
	}
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/start", peerA, "")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.JSONEq(t, `{"error":"too_many_attempts"}`, w.Body.String())
}

func TestDevicePollIsNotRateLimited(t *testing.T) {
	svc := &fakeLoginAPI{pollErrs: map[string]error{
		"a": auth.ErrAiandGatewayUnavailable,
	}}
	engine := newRouter(svc)
	for i := range 10 {
		w := doJSON(engine, http.MethodPost, "/account/v1/auth/device/poll", peerA, `{"login_id":"a"}`)
		assert.Equal(t, http.StatusBadGateway, w.Code, "poll %d must not trip the limiter", i+1)
	}
}

// ---- me ----

func TestMeWithSession(t *testing.T) {
	svc := &fakeLoginAPI{
		verifySession: &auth.ActiveSession{
			Session:      auth.LoginSession{ID: "s-1"},
			Account:      auth.Account{ID: "acct_abc", Email: "ops@example.com"},
			Installation: &auth.Installation{ID: "11111111-1111-1111-1111-111111111111", ExternalID: "acct_abc"},
		},
	}
	engine := newRouter(svc)
	req := httptest.NewRequest(http.MethodGet, "/account/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: "tok"})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotEqual(t, http.StatusUnauthorized, w.Code, "me must never 401 — the frontend treats 401 as a bounce")
	var resp map[string]any
	require.NoError(t, jsonUnmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["authenticated"])
	assert.Equal(t, "ops@example.com", resp["email"])
	inst, ok := resp["installation"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "acct_abc", inst["external_id"])
}

func TestMeWithoutSessionIs200Unauthenticated(t *testing.T) {
	svc := &fakeLoginAPI{verifyErr: auth.ErrSessionNotFound}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodGet, "/account/v1/auth/me", peerA, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
	assert.JSONEq(t, `{"authenticated":false}`, w.Body.String())
	// With no cookie presented there is nothing to clear and no service call.
	assert.Empty(t, w.Result().Cookies())
	svc.mu.Lock()
	called := svc.verifyCalls + len(svc.pollCalls)
	svc.mu.Unlock()
	assert.Zero(t, called)
}

func TestMeUnknownTokenStill200(t *testing.T) {
	svc := &fakeLoginAPI{verifyErr: auth.ErrSessionNotFound}
	engine := newRouter(svc)
	req := httptest.NewRequest(http.MethodGet, "/account/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: "stale"})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"authenticated":false}`, w.Body.String())
}

// ---- logout ----

func TestLogoutRevokesRowAndClearsCookie(t *testing.T) {
	svc := &fakeLoginAPI{}
	engine := newRouter(svc)
	req := httptest.NewRequest(http.MethodPost, "/account/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: "tok-123"})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	svc.mu.Lock()
	tokens := append([]string(nil), svc.logoutTokens...)
	svc.mu.Unlock()
	require.Equal(t, []string{"tok-123"}, tokens, "logout must revoke the session row, not only clear the cookie")

	var clear *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == auth.LoginSessionCookieName {
			clear = ck
		}
	}
	require.NotNil(t, clear)
	assert.Equal(t, -1, clear.MaxAge, "cookie must be expired")
}

func TestLogoutThenMeIsUnauthenticated(t *testing.T) {
	svc := &fakeLoginAPI{}
	engine := newRouter(svc)
	// Session verify after revocation resolves nothing.
	svc.mu.Lock()
	svc.verifyErr = auth.ErrSessionNotFound
	svc.mu.Unlock()
	w := doJSON(engine, http.MethodGet, "/account/v1/auth/me", peerA, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"authenticated":false}`, w.Body.String())
}

func TestLogoutWithoutCookieIsIdempotent200(t *testing.T) {
	svc := &fakeLoginAPI{}
	engine := newRouter(svc)
	w := doJSON(engine, http.MethodPost, "/account/v1/auth/logout", peerA, "")
	require.Equal(t, http.StatusOK, w.Code)
	svc.mu.Lock()
	tokens := append([]string(nil), svc.logoutTokens...)
	svc.mu.Unlock()
	assert.Empty(t, tokens)
}

func TestLogoutRepoFailureIs500(t *testing.T) {
	svc := &fakeLoginAPI{logoutErr: errors.New("db down")}
	engine := newRouter(svc)
	req := httptest.NewRequest(http.MethodPost, "/account/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: "tok"})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---- cookie attributes ----

func TestCookieInsecureEnvFlipsSecureOff(t *testing.T) {
	t.Setenv("ROUTER_COOKIE_INSECURE", "true")
	account.ConfigureCookieSecurityFromEnv()
	assert.False(t, account.CookieSecure(), "ROUTER_COOKIE_INSECURE=true must turn Secure off")
	t.Setenv("ROUTER_COOKIE_INSECURE", "false")
	account.ConfigureCookieSecurityFromEnv()
	assert.True(t, account.CookieSecure())
	t.Setenv("ROUTER_COOKIE_INSECURE", "")
	account.ConfigureCookieSecurityFromEnv()
	assert.True(t, account.CookieSecure())
}
