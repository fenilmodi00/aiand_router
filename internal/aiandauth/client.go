// Package aiandauth is the HTTP client for the Aiand gateway's device-flow
// auth surface (POST /auth/device/*, GET /api/user, GET /api/orgs). It holds
// no state: polling cadence, persistence, and encryption live in internal/auth.
package aiandauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAuthURL is the production Aiand gateway (AIAND_AUTH_URL).
const DefaultAuthURL = "https://api.aiand.com"

// ClientID identifies this router install to the gateway's device flow.
const ClientID = "aiand-router"

// DeviceGrant is the OAuth2 device authorization grant (RFC 8628).
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

const defaultTimeout = 15 * time.Second

// Client sends one request per call against the configured gateway.
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

// DeviceCode is one pending device-login session minted by /auth/device/code.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Org is the Aiand organization a credential belongs to.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TokenResponse is a minted or refreshed credential pair. RefreshToken is
// single-use: never logged, never sent concurrently.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Org          *Org   `json:"org,omitempty"`
}

// User is the account a key authenticates as (GET /api/user).
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// DeviceCodeStart opens a device-login session. keyName labels the key the
// gateway will mint (e.g. "aiand-router@<hostname>").
func (c *Client) DeviceCodeStart(ctx context.Context, keyName string) (DeviceCode, error) {
	body := map[string]string{"client_id": ClientID}
	if keyName != "" {
		body["key_name"] = keyName
	}
	var out DeviceCode
	if err := c.postJSON(ctx, "/auth/device/code", body, &out); err != nil {
		return DeviceCode{}, err
	}
	return out, nil
}

// PollToken exchanges a device code for tokens exactly once. The caller owns
// the waiting loop: ErrAuthorizationPending and ErrSlowDown are retryable
// outcomes, ErrSlowDown meaning the next attempt waits five seconds longer.
func (c *Client) PollToken(ctx context.Context, deviceCode string) (TokenResponse, error) {
	var out TokenResponse
	if err := c.postJSON(ctx, "/auth/device/token", map[string]string{
		"grant_type":  DeviceGrant,
		"device_code": deviceCode,
	}, &out); err != nil {
		return TokenResponse{}, err
	}
	return out, nil
}

// RefreshToken rotates a device-flow credential. Refresh tokens are single-use
// and never logged; the caller must serialize refreshes per installation.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (TokenResponse, error) {
	var out TokenResponse
	if err := c.postJSON(ctx, "/auth/device/token", map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}, &out); err != nil {
		return TokenResponse{}, err
	}
	return out, nil
}

// Revoke invalidates the refresh token server-side. A non-2xx response or a
// transport failure returns an error; callers treat revocation as best-effort.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	return c.postJSON(ctx, "/auth/device/logout", map[string]string{
		"refresh_token": refreshToken,
	}, nil)
}

// ValidateKey checks a pasted key by fetching the account it belongs to.
// ErrInvalidKey means the gateway rejected the key; any other error is a
// gateway problem, not a verdict on the key.
func (c *Client) ValidateKey(ctx context.Context, key string) (User, error) {
	var out User
	if err := c.getJSON(ctx, "/api/user", key, &out); err != nil {
		return User{}, err
	}
	return out, nil
}

// ListOrgs returns the Aiand organizations the key can access.
func (c *Client) ListOrgs(ctx context.Context, key string) ([]Org, error) {
	var out []Org
	if err := c.getJSON(ctx, "/api/orgs", key, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// tokenErrorBody is the gateway's OAuth2-shaped failure payload.
type tokenErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) postJSON(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encode request body: %w", ErrGateway, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", ErrGateway, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) getJSON(ctx context.Context, path, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.authURL+path, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %w", ErrGateway, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGateway, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read response: %w", ErrGateway, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: decode %s response: %w", ErrGateway, req.URL.Path, err)
	}
	return nil
}

// maxResponseBytes bounds gateway payloads; auth responses are tiny, so this
// only turns a hostile or misrouted body into a decode error instead of an
// unbounded read.
const maxResponseBytes = 1 << 20

func responseError(status int, payload []byte) error {
	if status == http.StatusUnauthorized {
		return ErrInvalidKey
	}
	var body tokenErrorBody
	if err := json.Unmarshal(payload, &body); err == nil && body.Error != "" {
		switch body.Error {
		case "authorization_pending":
			return fmt.Errorf("%w: %s", ErrAuthorizationPending, body.ErrorDescription)
		case "slow_down":
			return ErrSlowDown
		case "access_denied":
			return fmt.Errorf("%w: %s", ErrAccessDenied, body.ErrorDescription)
		case "expired_token":
			return fmt.Errorf("%w: %s", ErrExpiredToken, body.ErrorDescription)
		}
		return fmt.Errorf("%w: gateway returned HTTP %d: %s", ErrGateway, status, body.Error)
	}
	return fmt.Errorf("%w: gateway returned HTTP %d", ErrGateway, status)
}

// Sentinel errors. errors.Is is the contract; wrapped text is description only.
var (
	// ErrGateway covers unreachable gateways, non-2xx responses without a
	// recognized OAuth2 error code, and undecodable payloads.
	ErrGateway = errors.New("aiandauth: gateway error")
	// ErrAuthorizationPending means the user has not finished the browser step yet.
	ErrAuthorizationPending = errors.New("aiandauth: authorization pending")
	// ErrSlowDown means the poller must raise its interval by five seconds.
	ErrSlowDown = errors.New("aiandauth: poll interval too short")
	// ErrAccessDenied means the user rejected the login in the browser.
	ErrAccessDenied = errors.New("aiandauth: access denied")
	// ErrExpiredToken means the device code (or refresh) expired before approval.
	ErrExpiredToken = errors.New("aiandauth: expired token")
	// ErrInvalidKey means the gateway rejected a key (HTTP 401).
	ErrInvalidKey = errors.New("aiandauth: invalid key")
)
