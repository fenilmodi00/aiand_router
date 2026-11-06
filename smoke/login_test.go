//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// Fixture credentials for the paste-login scenario. SYNTHETIC ONLY: these
// strings must never be a real gateway key, and they must match the
// Authorization values the identity cassettes under
// smoke/mitmproxy/cassettes were keyed on (see identityPaths in
// ../mitmproxy/store.go). A run against the live gateway with these values
// records nothing and validates nothing.
const (
	pasteKeyA = "sk-aiand-smoke-login-a"
	pasteKeyB = "sk-aiand-smoke-login-b"

	sessionCookie = "router_account_session"
)

// dashboardClient is one browser-equivalent dashboard visitor: a cookie jar
// so the HttpOnly session cookie set by POST /auth/paste rides every later
// dashboard call automatically.
type dashboardClient struct {
	base string
	http *http.Client
}

func newDashboardClient(t *testing.T) *dashboardClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &dashboardClient{base: cfg.BaseURL, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (c *dashboardClient) do(t *testing.T, method, path string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return resp.StatusCode, resp.Header, raw
}

// paste performs POST /account/v1/auth/paste and asserts the 200 + Set-Cookie
// bootstrap contract. Returns the decoded response body.
func (c *dashboardClient) paste(t *testing.T, key string) (email string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"key": key})
	if err != nil {
		t.Fatalf("marshal paste body: %v", err)
	}
	status, headers, raw := c.do(t, http.MethodPost, "/account/v1/auth/paste", body)
	if status != http.StatusOK {
		t.Fatalf("paste login: want 200, got %d; body: %s", status, truncate(raw, 400))
	}
	var cookies []*http.Cookie
	for _, sc := range headers.Values("Set-Cookie") {
		for _, parsed := range (&http.Response{Header: http.Header{"Set-Cookie": []string{sc}}}).Cookies() {
			if parsed.Name == sessionCookie {
				cookies = append(cookies, parsed)
			}
		}
	}
	if len(cookies) == 0 {
		t.Fatalf("paste login: no %s Set-Cookie header; got %v", sessionCookie, headers.Values("Set-Cookie"))
	}
	if !cookies[0].HttpOnly {
		t.Errorf("session cookie must stay HttpOnly, got %q", cookies[0].Raw)
	}
	var out struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("paste login: decode body: %v", err)
	}
	return out.Email
}

// me performs GET /account/v1/auth/me and returns the decoded summary.
func (c *dashboardClient) me(t *testing.T) (authenticated bool, email string) {
	t.Helper()
	status, _, raw := c.do(t, http.MethodGet, "/account/v1/auth/me", nil)
	if status != http.StatusOK {
		t.Fatalf("me: want 200 (even unauthenticated), got %d; body: %s", status, truncate(raw, 400))
	}
	var out struct {
		Authenticated bool   `json:"authenticated"`
		Email         string `json:"email"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("me: decode body: %v", err)
	}
	return out.Authenticated, out.Email
}

// metricsSummary performs GET /admin/v1/metrics/summary. Returns the status
// and, on 200, the decoded summary.
func (c *dashboardClient) metricsSummary(t *testing.T) (status int, requestCount int64) {
	t.Helper()
	status, _, raw := c.do(t, http.MethodGet, "/admin/v1/metrics/summary", nil)
	if status != http.StatusOK {
		return status, 0
	}
	var out struct {
		RequestCount int64 `json:"request_count"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("metrics summary: decode body: %v", err)
	}
	return status, out.RequestCount
}

// issueKey performs POST /admin/v1/keys with the session cookie and returns
// the minted rk_ token.
func (c *dashboardClient) issueKey(t *testing.T) string {
	t.Helper()
	body := []byte(`{"name":"smoke-login-scenario"}`)
	status, _, raw := c.do(t, http.MethodPost, "/admin/v1/keys", body)
	if status != http.StatusCreated {
		t.Fatalf("issue key: want 201, got %d; body: %s", status, truncate(raw, 400))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("issue key: decode body: %v", err)
	}
	if !strings.HasPrefix(out.Token, "rk_") {
		t.Fatalf("issue key: want an rk_ token, got %q", truncate([]byte(out.Token), 12))
	}
	return out.Token
}

// messagesTurn performs POST /v1/messages authenticated by the bearer key.
func (c *dashboardClient) messagesTurn(t *testing.T, key string) response {
	t.Helper()
	body := newRequest("smoke-login-bootstrap").tokens(64).
		text("Reply with exactly the word: ok").build(t)
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set(forceModelHeader, cfg.PinModel)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("messages: read body: %v", err)
	}
	out := response{status: resp.StatusCode, headers: resp.Header, body: raw}
	// Non-streamed bodies carry the terminal message as plain JSON; parse it
	// the same way doCall does so requireOKMessage can assert on it.
	var msg anthropicMessage
	if len(raw) > 0 && json.Unmarshal(raw, &msg) == nil {
		out.message = &msg
	}
	return out
}

// logout performs POST /account/v1/auth/logout, asserting the 200.
func (c *dashboardClient) logout(t *testing.T) {
	t.Helper()
	status, _, raw := c.do(t, http.MethodPost, "/account/v1/auth/logout", nil)
	if status != http.StatusOK {
		t.Fatalf("logout: want 200, got %d; body: %s", status, truncate(raw, 400))
	}
}

// TestPasteLoginBootstrap walks the full first-run bootstrap through a fresh
// dashboard visitor: paste login against the synthetic gateway identity
// cassettes, session-cookie-scoped dashboard surfaces, a provisioned data
// plane, logout revocation, the dead legacy surface, and cross-account
// isolation. Cassettes replay only; the run needs zero upstream access.
func TestPasteLoginBootstrap(t *testing.T) {
	owner := newDashboardClient(t)

	t.Run("paste login bootstraps a session", func(t *testing.T) {
		email := owner.paste(t, pasteKeyA)
		if email != "smoke-login-a@example.invalid" {
			t.Errorf("paste response email = %q, want the cassette identity's", email)
		}
		authenticated, meEmail := owner.me(t)
		if !authenticated {
			t.Fatal("me after paste: want authenticated:true")
		}
		if meEmail != email {
			t.Errorf("me email = %q, want %q", meEmail, email)
		}
	})

	t.Run("metrics scoped to the fresh installation", func(t *testing.T) {
		status, count := owner.metricsSummary(t)
		if status != http.StatusOK {
			t.Fatalf("metrics summary: want 200, got %d", status)
		}
		if count != 0 {
			t.Errorf("fresh installation request_count = %d, want 0", count)
		}
	})

	t.Run("issued key serves the data plane", func(t *testing.T) {
		token := owner.issueKey(t)
		r := owner.messagesTurn(t, token)
		requireOKMessage(t, r)
		assertServedByPin(t, r)
	})

	t.Run("logout revokes the session", func(t *testing.T) {
		owner.logout(t)
		authenticated, _ := owner.me(t)
		if authenticated {
			t.Fatal("me after logout: want authenticated:false")
		}
		if status, _ := owner.metricsSummary(t); status != http.StatusUnauthorized {
			t.Fatalf("metrics after logout: want 401, got %d", status)
		}
	})

	t.Run("legacy login surface is gone", func(t *testing.T) {
		status, _, raw := owner.do(t, http.MethodPost, "/admin/v1/auth/login", []byte(`{}`))
		if status != http.StatusNotFound {
			t.Fatalf("POST /admin/v1/auth/login: want 404 (old surface removed), got %d; body: %s", status, truncate(raw, 200))
		}
	})

	t.Run("second account cannot see the first account's metrics", func(t *testing.T) {
		second := newDashboardClient(t)
		if email := second.paste(t, pasteKeyB); email != "smoke-login-b@example.invalid" {
			t.Errorf("second paste email = %q, want the second cassette identity's", email)
		}
		// The second visitor's fresh installation has zero requests; the first
		// account's issued-key traffic (asserted above) must not leak in.
		_, count := second.metricsSummary(t)
		if count != 0 {
			t.Errorf("second account sees request_count = %d, want 0 (cross-account isolation)", count)
		}
	})
}
