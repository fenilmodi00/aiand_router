package aiandauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/aiandauth"
	"aiand-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture records one incoming gateway request so tests can assert the exact
// wire contract the gateway (and its CLI twin) expects.
type capture struct {
	Method    string
	Path      string
	Header    http.Header
	Body      []byte
	ServerURL string
}

func captureServer(t *testing.T, status int, response string) (*aiandauth.Client, *capture) {
	t.Helper()
	var got capture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		got.Method, got.Path, got.Header, got.Body = r.Method, r.URL.Path, r.Header.Clone(), body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	got.ServerURL = server.URL
	return aiandauth.NewClient(server.URL, server.Client()), &got
}

func decodeBody(t *testing.T, c *capture) map[string]string {
	t.Helper()
	var out map[string]string
	require.NoError(t, json.Unmarshal(c.Body, &out))
	return out
}

func TestDeviceCodeStart_SendsClientIDAndKeyName(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{
		"device_code": "dc-1",
		"user_code": "ABCD-EFGH",
		"verification_uri": "https://api.aiand.com/device",
		"verification_uri_complete": "https://api.aiand.com/device?code=ABCD-EFGH",
		"expires_in": 900,
		"interval": 5
	}`)

	code, err := client.DeviceCodeStart(context.Background(), "aiand-router@router-host")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/auth/device/code", got.Path)
	body := decodeBody(t, got)
	assert.Equal(t, "aiand-router", body["client_id"], "gateway requires the router's client_id")
	assert.Equal(t, "aiand-router@router-host", body["key_name"], "gateway keys the minted token by name")
	assert.Equal(t, "ABCD-EFGH", code.UserCode)
	assert.Equal(t, "https://api.aiand.com/device", code.VerificationURI)
	assert.Equal(t, "https://api.aiand.com/device?code=ABCD-EFGH", code.VerificationURIComplete)
	assert.Equal(t, 900, code.ExpiresIn)
	assert.Equal(t, 5, code.Interval)
}

// The live gateway returns gateway-relative verification links; they must
// come back absolute or the dashboard's "Enter this code at …" href is
// unclickable.
func TestDeviceCodeStart_ResolvesRelativeVerificationURI(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{
		"device_code": "dc-1",
		"user_code": "W8LB-SFZN",
		"verification_uri": "/auth/device",
		"verification_uri_complete": "/auth/device?user_code=W8LB-SFZN",
		"expires_in": 900,
		"interval": 5
	}`)

	code, err := client.DeviceCodeStart(context.Background(), "aiand-router@router-host")
	require.NoError(t, err)

	assert.Equal(t, "/auth/device/code", got.Path)
	assert.Equal(t, got.ServerURL+"/auth/device", code.VerificationURI)
	assert.Equal(t, got.ServerURL+"/auth/device?user_code=W8LB-SFZN", code.VerificationURIComplete)
}

func TestPollToken_UsesDeviceGrant(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{
		"access_token": "sk-secret",
		"refresh_token": "rt-secret",
		"token_type": "bearer",
		"expires_in": 2592000,
		"org": {"id": "org-1", "name": "Acme"}
	}`)

	token, err := client.PollToken(context.Background(), "dc-1")
	require.NoError(t, err)

	body := decodeBody(t, got)
	assert.Equal(t, "/auth/device/token", got.Path)
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", body["grant_type"], "device polling must use the RFC 8628 grant")
	assert.Equal(t, "dc-1", body["device_code"])
	assert.Equal(t, "sk-secret", token.AccessToken)
	assert.Equal(t, "rt-secret", token.RefreshToken)
	require.NotNil(t, token.Org, "org binding must survive decoding")
	assert.Equal(t, "org-1", token.Org.ID)
	assert.Equal(t, "Acme", token.Org.Name)
}

// A token response with an empty org object must decode as no org, so the
// login service can fall back to the key-scoped org instead of persisting "".
func TestPollToken_EmptyOrgIsNil(t *testing.T) {
	client, _ := captureServer(t, http.StatusOK, `{
		"access_token": "sk-secret",
		"token_type": "bearer",
		"expires_in": 60,
		"org": {}
	}`)

	token, err := client.PollToken(context.Background(), "dc-1")
	require.NoError(t, err)
	assert.Nil(t, token.Org)
}

func TestPollToken_ErrorVariantsMapToSentinels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"authorization_pending", http.StatusBadRequest, `{"error": "authorization_pending", "error_description": "still waiting"}`, auth.ErrAiandAuthorizationPending},
		{"slow_down", http.StatusBadRequest, `{"error": "slow_down"}`, auth.ErrAiandSlowDown},
		{"access_denied", http.StatusForbidden, `{"error": "access_denied", "error_description": "no"}`, auth.ErrAiandAccessDenied},
		{"expired_token", http.StatusBadRequest, `{"error": "expired_token"}`, auth.ErrAiandExpiredToken},
		{"unrecognized_error_code", http.StatusBadRequest, `{"error": "mystery"}`, auth.ErrAiandGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, got := captureServer(t, tc.status, tc.body)
			_, err := client.PollToken(context.Background(), "dc-1")
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, "/auth/device/token", got.Path)
		})
	}
}

func TestRefreshToken_UsesRefreshGrant(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{
		"access_token": "sk-next",
		"refresh_token": "rt-next",
		"token_type": "bearer",
		"expires_in": 2592000
	}`)

	token, err := client.RefreshToken(context.Background(), "rt-current")
	require.NoError(t, err)

	body := decodeBody(t, got)
	assert.Equal(t, "refresh_token", body["grant_type"])
	assert.Equal(t, "rt-current", body["refresh_token"])
	assert.Equal(t, "sk-next", token.AccessToken)
	assert.Equal(t, "rt-next", token.RefreshToken)
}

// --- browser authorization-code flow ---------------------------------------

func TestAuthorizeURL_BuildsBrowserQuery(t *testing.T) {
	client := aiandauth.NewClient("https://gateway.test", nil)

	raw := client.AuthorizeURL(auth.AiandAuthorizeParams{
		RedirectURI:   "http://127.0.0.1:8080",
		State:         "st-123",
		CodeChallenge: "cc-challenge",
		KeyName:       "aiand-router@router-host",
	})
	u, err := url.Parse(raw)
	require.NoError(t, err)

	assert.Equal(t, "https://gateway.test", u.Scheme+"://"+u.Host)
	assert.Equal(t, "/auth/authorize", u.Path, "browser flow uses the gateway's authorize endpoint")
	q := u.Query()
	assert.Equal(t, "aiand-cli", q.Get("client_id"), "browser flow must present the public browser client, not the device client")
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "http://127.0.0.1:8080", q.Get("redirect_uri"))
	assert.Equal(t, "st-123", q.Get("state"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, "cc-challenge", q.Get("code_challenge"))
	assert.Equal(t, "aiand-router@router-host", q.Get("key_name"))
}

func TestAuthorizeURL_OmitsEmptyKeyName(t *testing.T) {
	client := aiandauth.NewClient("https://gateway.test", nil)

	u, err := url.Parse(client.AuthorizeURL(auth.AiandAuthorizeParams{
		RedirectURI:   "http://127.0.0.1:8080",
		State:         "s",
		CodeChallenge: "cc",
	}))
	require.NoError(t, err)
	assert.False(t, u.Query().Has("key_name"), "an unset key name must not reach the gateway as an empty value")
}

func TestBrowserAuthorizeAvailable_Accepts302WithoutFollowing(t *testing.T) {
	// The live gateway 302s straight to its login UI on success; the probe
	// must treat the redirect as "available", not chase it.
	client, got := captureServer(t, http.StatusFound, "")

	authorizeURL := aiandauth.NewClient(got.ServerURL, nil).AuthorizeURL(auth.AiandAuthorizeParams{
		RedirectURI:   "http://127.0.0.1:8080",
		State:         "probe-state",
		CodeChallenge: "probe-challenge",
	})
	require.NoError(t, client.BrowserAuthorizeAvailable(context.Background(), authorizeURL))
}

func TestBrowserAuthorizeAvailable_SendsPlainGetWithoutAuthorization(t *testing.T) {
	var got capture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method, got.Path, got.Header = r.Method, r.URL.Path, r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := aiandauth.NewClient(server.URL, server.Client())

	require.NoError(t, client.BrowserAuthorizeAvailable(
		context.Background(), aiandauth.NewClient(server.URL, nil).AuthorizeURL(auth.AiandAuthorizeParams{
			RedirectURI:   "http://127.0.0.1:8080",
			State:         "s",
			CodeChallenge: "cc",
		})))

	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/auth/authorize", got.Path)
	assert.Empty(t, got.Header.Get("Authorization"), "the preflight runs before any credential exists")
}

func TestBrowserAuthorizeAvailable_UnsupportedShapesFallBackToDevice(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"not_found", http.StatusNotFound, `{"error": "not_found"}`},
		{"not_implemented", http.StatusNotImplemented, ""},
		{"invalid_request", http.StatusBadRequest, `{"error": "invalid_request", "error_description": "unknown client"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, got := captureServer(t, tc.status, tc.body)
			authorizeURL := aiandauth.NewClient(got.ServerURL, nil).AuthorizeURL(auth.AiandAuthorizeParams{
				RedirectURI:   "http://127.0.0.1:8080",
				State:         "probe-state",
				CodeChallenge: "probe-challenge",
			})
			err := client.BrowserAuthorizeAvailable(context.Background(), authorizeURL)
			assert.ErrorIs(t, err, auth.ErrAiandBrowserUnsupported, "the login UI must fall back to the device flow")
			assert.Equal(t, "/auth/authorize", got.Path)
		})
	}
}

func TestBrowserAuthorizeAvailable_OtherFailuresAreGatewayErrors(t *testing.T) {
	t.Run("bad_request_with_other_code", func(t *testing.T) {
		// A 400 that is not invalid_request is not a shape verdict; treat it
		// as a gateway problem rather than silently dropping the browser flow.
		client, got := captureServer(t, http.StatusBadRequest, `{"error": "server_error"}`)
		authorizeURL := aiandauth.NewClient(got.ServerURL, nil).AuthorizeURL(auth.AiandAuthorizeParams{
			RedirectURI:   "http://127.0.0.1:8080",
			State:         "s",
			CodeChallenge: "cc",
		})
		err := client.BrowserAuthorizeAvailable(context.Background(), authorizeURL)
		assert.ErrorIs(t, err, auth.ErrAiandGateway)
		assert.NotErrorIs(t, err, auth.ErrAiandBrowserUnsupported)
	})
	t.Run("server_error", func(t *testing.T) {
		client, got := captureServer(t, http.StatusInternalServerError, `{"error": "boom"}`)
		authorizeURL := aiandauth.NewClient(got.ServerURL, nil).AuthorizeURL(auth.AiandAuthorizeParams{
			RedirectURI:   "http://127.0.0.1:8080",
			State:         "s",
			CodeChallenge: "cc",
		})
		assert.ErrorIs(t, client.BrowserAuthorizeAvailable(context.Background(), authorizeURL), auth.ErrAiandGateway)
	})
	t.Run("unreachable_gateway", func(t *testing.T) {
		client := aiandauth.NewClient("http://127.0.0.1:1", &http.Client{Timeout: 2 * time.Second})
		assert.ErrorIs(t, client.BrowserAuthorizeAvailable(context.Background(), "http://127.0.0.1:1/auth/authorize"), auth.ErrAiandGateway)
	})
}

func TestExchangeCode_UsesAuthorizationCodeGrant(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{
		"access_token": "sk-browser",
		"refresh_token": "rt-browser",
		"token_type": "bearer",
		"expires_in": 2592000,
		"org": {"id": "org-9", "name": "Beta"}
	}`)

	token, err := client.ExchangeCode(context.Background(), auth.AiandCodeExchangeParams{
		Code:         "ac-code",
		CodeVerifier: "pkce-verifier",
		RedirectURI:  "http://127.0.0.1:8080",
	})
	require.NoError(t, err)

	body := decodeBody(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/auth/device/token", got.Path, "the browser flow exchanges at the gateway's token endpoint")
	assert.Equal(t, "authorization_code", body["grant_type"])
	assert.Equal(t, "ac-code", body["code"])
	assert.Equal(t, "pkce-verifier", body["code_verifier"])
	assert.Equal(t, "aiand-cli", body["client_id"], "the exchange must present the browser client")
	assert.Equal(t, "http://127.0.0.1:8080", body["redirect_uri"])
	assert.Equal(t, "sk-browser", token.AccessToken)
	assert.Equal(t, "rt-browser", token.RefreshToken)
	require.NotNil(t, token.Org)
	assert.Equal(t, "org-9", token.Org.ID)
	assert.Equal(t, "Beta", token.Org.Name)
}

func TestExchangeCode_InvalidGrantMapsToSentinel(t *testing.T) {
	client, got := captureServer(t, http.StatusBadRequest, `{"error": "invalid_grant", "error_description": "code replayed"}`)

	_, err := client.ExchangeCode(context.Background(), auth.AiandCodeExchangeParams{Code: "ac", CodeVerifier: "v", RedirectURI: "http://127.0.0.1:8080"})

	assert.ErrorIs(t, err, auth.ErrAiandInvalidGrant)
	assert.Equal(t, "/auth/device/token", got.Path)
}

func TestRevoke_PostsRefreshTokenToLogout(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{}`)

	require.NoError(t, client.Revoke(context.Background(), "rt-revoked"))

	assert.Equal(t, "/auth/device/logout", got.Path)
	body := decodeBody(t, got)
	assert.Equal(t, "rt-revoked", body["refresh_token"])
}

func TestRevoke_ServerFailureSurfacesAsError(t *testing.T) {
	client, _ := captureServer(t, http.StatusInternalServerError, `{"error": "boom"}`)
	assert.ErrorIs(t, client.Revoke(context.Background(), "rt"), auth.ErrAiandGateway)
}

func TestValidateKey_SendsBearerAndReturnsUser(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{"id": "user-1", "email": "ops@example.com"}`)

	user, err := client.ValidateKey(context.Background(), "sk-pasted")
	require.NoError(t, err)

	assert.Equal(t, "/api/user", got.Path)
	assert.Equal(t, "Bearer sk-pasted", got.Header.Get("Authorization"))
	assert.Equal(t, "user-1", user.ID)
	assert.Equal(t, "ops@example.com", user.Email)
}

func TestValidateKey_RejectedKeyIsSentinelNotGatewayFailure(t *testing.T) {
	client, _ := captureServer(t, http.StatusUnauthorized, `{"error": "invalid"}`)
	_, err := client.ValidateKey(context.Background(), "sk-bad")
	assert.ErrorIs(t, err, auth.ErrAiandInvalidKey)
}

func TestValidateKey_ServerProblemIsNotKeyRejection(t *testing.T) {
	client, _ := captureServer(t, http.StatusBadGateway, `<html>no</html>`)
	_, err := client.ValidateKey(context.Background(), "sk-fine")
	assert.ErrorIs(t, err, auth.ErrAiandGateway, "5xx must not be read as a verdict on the key")
}

func TestListOrgs_DecodesArray(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `[{"id": "org-1", "name": "Acme"}, {"id": "org-2", "name": "Beta"}]`)

	orgs, err := client.ListOrgs(context.Background(), "sk-secret")
	require.NoError(t, err)

	assert.Equal(t, "/api/orgs", got.Path)
	require.Len(t, orgs, 2)
	assert.Equal(t, "Acme", orgs[0].Name)
	assert.Equal(t, "Beta", orgs[1].Name)
}

func TestUnreachableGatewayIsErrGateway(t *testing.T) {
	// Port 1 on loopback refuses connections immediately; anything else risks
	// depending on external network state.
	client := aiandauth.NewClient("http://127.0.0.1:1", &http.Client{Timeout: 2 * time.Second})
	_, err := client.DeviceCodeStart(context.Background(), "aiand-router@x")
	assert.ErrorIs(t, err, auth.ErrAiandGateway)
}

func TestInvalidJSONResponseIsErrGateway(t *testing.T) {
	client, _ := captureServer(t, http.StatusOK, `{"access_token": `)
	_, err := client.PollToken(context.Background(), "dc-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrAiandGateway)
	assert.True(t, strings.Contains(err.Error(), "/auth/device/token"), "decode failures name the endpoint")
}

func TestResponseIsBounded(t *testing.T) {
	var big strings.Builder
	big.WriteString(`{"access_token": "`)
	for range 2 << 20 {
		big.WriteByte('a')
	}
	big.WriteString(`"}`)
	client, _ := captureServer(t, http.StatusOK, big.String())
	_, err := client.PollToken(context.Background(), "dc-1")
	require.Error(t, err, "oversized responses must not be trusted as tokens")
	assert.ErrorIs(t, err, auth.ErrAiandGateway)
}

func TestDefaultAuthURL(t *testing.T) {
	assert.Equal(t, "https://api.aiand.com", aiandauth.DefaultAuthURL)
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/auth/device/code", r.URL.Path, "double slash would break the gateway route")
		_, _ = w.Write([]byte(`{"device_code": "dc", "user_code": "U", "verification_uri": "u", "expires_in": 1, "interval": 1}`))
	}))
	t.Cleanup(server.Close)
	client := aiandauth.NewClient(server.URL+"/", server.Client())
	_, err := client.DeviceCodeStart(context.Background(), "")
	require.NoError(t, err)
}

// Compile-time guard: the production client must keep satisfying the full
// login gateway surface, browser flow included.
var _ auth.AiandLoginGateway = (*aiandauth.Client)(nil)

// Compile-time guard: sentinel errors stay distinct for errors.Is dispatch.
func TestSentinelsDistinct(t *testing.T) {
	errs := []error{
		auth.ErrAiandGateway, auth.ErrAiandAuthorizationPending, auth.ErrAiandSlowDown,
		auth.ErrAiandAccessDenied, auth.ErrAiandExpiredToken, auth.ErrAiandInvalidKey,
		auth.ErrAiandBrowserUnsupported, auth.ErrAiandInvalidGrant,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j {
				assert.False(t, errors.Is(a, b), "%v must not match %v", a, b)
			}
		}
	}
}
