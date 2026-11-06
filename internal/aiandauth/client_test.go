package aiandauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/aiandauth"

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

func TestPollToken_ErrorVariantsMapToSentinels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"authorization_pending", http.StatusBadRequest, `{"error": "authorization_pending", "error_description": "still waiting"}`, aiandauth.ErrAuthorizationPending},
		{"slow_down", http.StatusBadRequest, `{"error": "slow_down"}`, aiandauth.ErrSlowDown},
		{"access_denied", http.StatusForbidden, `{"error": "access_denied", "error_description": "no"}`, aiandauth.ErrAccessDenied},
		{"expired_token", http.StatusBadRequest, `{"error": "expired_token"}`, aiandauth.ErrExpiredToken},
		{"unrecognized_error_code", http.StatusBadRequest, `{"error": "mystery"}`, aiandauth.ErrGateway},
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

func TestRevoke_PostsRefreshTokenToLogout(t *testing.T) {
	client, got := captureServer(t, http.StatusOK, `{}`)

	require.NoError(t, client.Revoke(context.Background(), "rt-revoked"))

	assert.Equal(t, "/auth/device/logout", got.Path)
	body := decodeBody(t, got)
	assert.Equal(t, "rt-revoked", body["refresh_token"])
}

func TestRevoke_ServerFailureSurfacesAsError(t *testing.T) {
	client, _ := captureServer(t, http.StatusInternalServerError, `{"error": "boom"}`)
	assert.ErrorIs(t, client.Revoke(context.Background(), "rt"), aiandauth.ErrGateway)
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
	assert.ErrorIs(t, err, aiandauth.ErrInvalidKey)
}

func TestValidateKey_ServerProblemIsNotKeyRejection(t *testing.T) {
	client, _ := captureServer(t, http.StatusBadGateway, `<html>no</html>`)
	_, err := client.ValidateKey(context.Background(), "sk-fine")
	assert.ErrorIs(t, err, aiandauth.ErrGateway, "5xx must not be read as a verdict on the key")
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
	assert.ErrorIs(t, err, aiandauth.ErrGateway)
}

func TestInvalidJSONResponseIsErrGateway(t *testing.T) {
	client, _ := captureServer(t, http.StatusOK, `{"access_token": `)
	_, err := client.PollToken(context.Background(), "dc-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, aiandauth.ErrGateway)
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
	assert.ErrorIs(t, err, aiandauth.ErrGateway)
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

// Compile-time guard: sentinel errors stay distinct for errors.Is dispatch.
func TestSentinelsDistinct(t *testing.T) {
	errs := []error{
		aiandauth.ErrGateway, aiandauth.ErrAuthorizationPending, aiandauth.ErrSlowDown,
		aiandauth.ErrAccessDenied, aiandauth.ErrExpiredToken, aiandauth.ErrInvalidKey,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j {
				assert.False(t, errors.Is(a, b), "%v must not match %v", a, b)
			}
		}
	}
}
