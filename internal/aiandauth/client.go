// Package aiandauth is the HTTP adapter for the Aiand gateway's device-flow
// auth surface (POST /auth/device/*, GET /api/user, GET /api/orgs). It holds
// no state: polling cadence, persistence, and encryption live in internal/auth,
// which owns the AiandGateway interface, the DTOs, and the sentinel errors;
// this package only translates wire shapes and HTTP failures onto them.
package aiandauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aiand-os/router/internal/auth"
)

// DefaultAuthURL is the production Aiand gateway (AIAND_AUTH_URL).
const DefaultAuthURL = "https://api.aiand.com"

// ClientID identifies this router install to the gateway's device flow.
const ClientID = "aiand-router"

// DeviceGrant is the OAuth2 device authorization grant (RFC 8628).
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

const defaultTimeout = 15 * time.Second

// Client sends one request per call against the configured gateway. It
// implements auth.AiandGateway; cmd/router/main.go constructs it.
type Client struct {
	authURL    string
	httpClient *http.Client
}

// NewClient returns a gateway client. An empty authURL selects DefaultAuthURL;
// a nil httpClient selects one with a bounded request timeout.
func NewClient(authURL string, httpClient *http.Client) *Client {
	if authURL == "" {
		authURL = DefaultAuthURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{authURL: strings.TrimRight(authURL, "/"), httpClient: httpClient}
}

// wireDeviceCode mirrors /auth/device/code.
type wireDeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// wireOrg mirrors the org object the gateway embeds in token and org listings.
type wireOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// wireTokenResponse mirrors /auth/device/token.
type wireTokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    int      `json:"expires_in"`
	Org          *wireOrg `json:"org,omitempty"`
}

// wireUser mirrors GET /api/user.
type wireUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// wireOrgList mirrors GET /api/orgs.
type wireOrgList []wireOrg

// domain converts the wire session onto the auth-side DTO.
func (w wireDeviceCode) domain() auth.AiandDeviceCode {
	return auth.AiandDeviceCode{
		DeviceCode:              w.DeviceCode,
		UserCode:                w.UserCode,
		VerificationURI:         w.VerificationURI,
		VerificationURIComplete: w.VerificationURIComplete,
		ExpiresIn:               w.ExpiresIn,
		Interval:                w.Interval,
	}
}

// domain converts the wire credential pair onto the auth-side DTO.
func (w wireTokenResponse) domain() auth.AiandTokenResponse {
	var org *auth.AiandOrg
	if w.Org != nil {
		org = &auth.AiandOrg{ID: w.Org.ID, Name: w.Org.Name}
	}
	return auth.AiandTokenResponse{
		AccessToken:  w.AccessToken,
		RefreshToken: w.RefreshToken,
		TokenType:    w.TokenType,
		ExpiresIn:    w.ExpiresIn,
		Org:          org,
	}
}

// domain converts the wire account onto the auth-side DTO.
func (w wireUser) domain() auth.AiandUser {
	return auth.AiandUser{ID: w.ID, Email: w.Email}
}

// resolveVerificationURIs makes the gateway's verification links absolute.
// The live gateway returns gateway-relative paths ("/auth/device"), which the
// dashboard renders as a clickable href; RFC 8628 §3.3 requires the
// verification URI to be absolute or resolvable against the authorization
// server's base URL.
func resolveVerificationURIs(authURL string, out wireDeviceCode) wireDeviceCode {
	base, err := url.Parse(authURL)
	if err != nil || base.Host == "" {
		return out
	}
	if ref, err := url.Parse(out.VerificationURI); err == nil && !ref.IsAbs() && ref.Path != "" {
		out.VerificationURI = base.ResolveReference(ref).String()
	}
	if ref, err := url.Parse(out.VerificationURIComplete); err == nil && !ref.IsAbs() && ref.Path != "" {
		out.VerificationURIComplete = base.ResolveReference(ref).String()
	}
	return out
}

// DeviceCodeStart opens a device-login session. keyName labels the key the
// gateway will mint (e.g. "aiand-router@<hostname>").
func (c *Client) DeviceCodeStart(ctx context.Context, keyName string) (auth.AiandDeviceCode, error) {
	body := map[string]string{"client_id": ClientID}
	if keyName != "" {
		body["key_name"] = keyName
	}
	var out wireDeviceCode
	if err := c.postJSON(ctx, "/auth/device/code", body, &out); err != nil {
		return auth.AiandDeviceCode{}, err
	}
	return resolveVerificationURIs(c.authURL, out).domain(), nil
}

// PollToken exchanges a device code for tokens exactly once. The caller owns
// the waiting loop: auth.ErrAiandAuthorizationPending and
// auth.ErrAiandSlowDown are retryable outcomes, the latter meaning the next
// attempt waits five seconds longer.
func (c *Client) PollToken(ctx context.Context, deviceCode string) (auth.AiandTokenResponse, error) {
	var out wireTokenResponse
	if err := c.postJSON(ctx, "/auth/device/token", map[string]string{
		"grant_type":  DeviceGrant,
		"device_code": deviceCode,
	}, &out); err != nil {
		return auth.AiandTokenResponse{}, err
	}
	return out.domain(), nil
}

// RefreshToken rotates a device-flow credential. Refresh tokens are single-use
// and never logged; the caller must serialize refreshes per installation.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (auth.AiandTokenResponse, error) {
	var out wireTokenResponse
	if err := c.postJSON(ctx, "/auth/device/token", map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}, &out); err != nil {
		return auth.AiandTokenResponse{}, err
	}
	return out.domain(), nil
}

// Revoke invalidates the refresh token server-side. A non-2xx response or a
// transport failure returns an error; callers treat revocation as best-effort.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	return c.postJSON(ctx, "/auth/device/logout", map[string]string{
		"refresh_token": refreshToken,
	}, nil)
}

// ValidateKey checks a pasted key by fetching the account it belongs to.
// auth.ErrAiandInvalidKey means the gateway rejected the key; any other error
// is a gateway problem, not a verdict on the key.
func (c *Client) ValidateKey(ctx context.Context, key string) (auth.AiandUser, error) {
	var out wireUser
	if err := c.getJSON(ctx, "/api/user", key, &out); err != nil {
		return auth.AiandUser{}, err
	}
	return out.domain(), nil
}

// ListOrgs returns the Aiand organizations the key can access.
func (c *Client) ListOrgs(ctx context.Context, key string) ([]auth.AiandOrg, error) {
	var out wireOrgList
	if err := c.getJSON(ctx, "/api/orgs", key, &out); err != nil {
		return nil, err
	}
	orgs := make([]auth.AiandOrg, 0, len(out))
	for _, org := range out {
		orgs = append(orgs, auth.AiandOrg{ID: org.ID, Name: org.Name})
	}
	return orgs, nil
}

// tokenErrorBody is the gateway's OAuth2-shaped failure payload.
type tokenErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) postJSON(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encode request body: %w", auth.ErrAiandGateway, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", auth.ErrAiandGateway, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) getJSON(ctx context.Context, path, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.authURL+path, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %w", auth.ErrAiandGateway, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", auth.ErrAiandGateway, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read response: %w", auth.ErrAiandGateway, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: decode %s response: %w", auth.ErrAiandGateway, req.URL.Path, err)
	}
	return nil
}

// maxResponseBytes bounds gateway payloads; auth responses are tiny, so this
// only turns a hostile or misrouted body into a decode error instead of an
// unbounded read.
const maxResponseBytes = 1 << 20

func responseError(status int, payload []byte) error {
	if status == http.StatusUnauthorized {
		return auth.ErrAiandInvalidKey
	}
	var body tokenErrorBody
	if err := json.Unmarshal(payload, &body); err == nil && body.Error != "" {
		switch body.Error {
		case "authorization_pending":
			return fmt.Errorf("%w: %s", auth.ErrAiandAuthorizationPending, body.ErrorDescription)
		case "slow_down":
			return auth.ErrAiandSlowDown
		case "access_denied":
			return fmt.Errorf("%w: %s", auth.ErrAiandAccessDenied, body.ErrorDescription)
		case "expired_token":
			return fmt.Errorf("%w: %s", auth.ErrAiandExpiredToken, body.ErrorDescription)
		}
		return fmt.Errorf("%w: gateway returned HTTP %d: %s", auth.ErrAiandGateway, status, body.Error)
	}
	return fmt.Errorf("%w: gateway returned HTTP %d", auth.ErrAiandGateway, status)
}
