package auth

import (
	"context"
	"errors"
)

// This file is the domain-side vocabulary for the Aiand gateway: the
// AiandGateway interface, the token/identity DTOs the credential lifecycle
// speaks, and the sentinel errors gateway outcomes map to. It is pure —
// no I/O, no HTTP — so internal/auth stays inner-ring. The HTTP adapter
// living in internal/aiandauth implements AiandGateway and is constructed
// only in cmd/router/main.go.

// AiandGateway wraps the gateway calls the credential lifecycle needs.
// *aiandauth.Client satisfies it; tests inject fakes.
type AiandGateway interface {
	DeviceCodeStart(ctx context.Context, keyName string) (AiandDeviceCode, error)
	PollToken(ctx context.Context, deviceCode string) (AiandTokenResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (AiandTokenResponse, error)
	Revoke(ctx context.Context, refreshToken string) error
	ValidateKey(ctx context.Context, key string) (AiandUser, error)
}

// AiandLoginGateway is the gateway surface the SaaS login service needs:
// device token operations, key-to-identity resolution, and the browser
// authorization-code flow. The identity half lives in AiandIdentityVerifier
// because it resolves an sk- key to the (user, org) pair the account row is
// keyed on.
type AiandLoginGateway interface {
	AiandGateway
	AiandIdentityVerifier
	AiandBrowserGateway
}

// AiandAuthorizeParams are the inputs for the browser flow's authorization
// URL. RedirectURI is the caller's loopback origin; State and CodeChallenge
// (S256) bind the browser round trip; KeyName optionally labels the key the
// gateway will mint.
type AiandAuthorizeParams struct {
	RedirectURI   string
	State         string
	CodeChallenge string
	KeyName       string
}

// AiandCodeExchangeParams are the inputs for the browser flow's token
// exchange. The verifier must be the PKCE plaintext matching the challenge
// sent with the authorize URL.
type AiandCodeExchangeParams struct {
	Code         string
	CodeVerifier string
	RedirectURI  string
}

// AiandBrowserGateway is the browser authorization-code surface: build the
// authorize URL, preflight that the gateway accepts it, and exchange the
// resulting code for tokens. *aiandauth.Client satisfies it.
type AiandBrowserGateway interface {
	AuthorizeURL(params AiandAuthorizeParams) string
	// BrowserAuthorizeAvailable probes the real authorize URL without
	// following redirects. ErrAiandBrowserUnsupported means the gateway does
	// not serve browser authorization for this shape; callers fall back to
	// the device flow.
	BrowserAuthorizeAvailable(ctx context.Context, authorizeURL string) error
	ExchangeCode(ctx context.Context, params AiandCodeExchangeParams) (AiandTokenResponse, error)
}

// AiandDeviceCode is one pending device-login session minted by
// POST /auth/device/code.
type AiandDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

// AiandOrg is the Aiand organization a credential belongs to.
type AiandOrg struct {
	ID   string
	Name string
}

// AiandTokenResponse is a minted or refreshed credential pair. RefreshToken
// is single-use: never logged, never sent concurrently.
type AiandTokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	Org          *AiandOrg
}

// AiandUser is the account a key authenticates as (GET /api/user).
type AiandUser struct {
	ID    string
	Email string
}

// Sentinel errors the HTTP adapter maps wire failures onto. errors.Is is
// the contract; wrapped text is description only.
var (
	// ErrAiandGateway covers unreachable gateways, non-2xx responses without
	// a recognized OAuth2 error code, and undecodable payloads.
	ErrAiandGateway = errors.New("aiand: gateway error")
	// ErrAiandAuthorizationPending means the user has not finished the
	// browser step yet.
	ErrAiandAuthorizationPending = errors.New("aiand: authorization pending")
	// ErrAiandSlowDown means the poller must raise its interval by five seconds.
	ErrAiandSlowDown = errors.New("aiand: poll interval too short")
	// ErrAiandAccessDenied means the user rejected the login in the browser.
	ErrAiandAccessDenied = errors.New("aiand: access denied")
	// ErrAiandExpiredToken means the device code (or refresh) expired before
	// approval.
	ErrAiandExpiredToken = errors.New("aiand: expired token")
	// ErrAiandInvalidKey means the gateway rejected a key (HTTP 401).
	ErrAiandInvalidKey = errors.New("aiand: invalid key")
	// ErrAiandOrgUnresolved means a valid key's organization could not be
	// determined: the gateway sent no key-scoped org and the membership list
	// is empty or ambiguous.
	ErrAiandOrgUnresolved = errors.New("aiand: key organization could not be resolved")
	// ErrAiandBrowserUnsupported means the gateway does not serve browser
	// authorization for the probed URL (404/501, or 400 invalid_request);
	// the login UI must fall back to the device flow.
	ErrAiandBrowserUnsupported = errors.New("aiand: browser login unsupported")
	// ErrAiandInvalidGrant means the gateway rejected the browser code
	// exchange (OAuth2 invalid_grant): unknown, expired, or replayed code.
	ErrAiandInvalidGrant = errors.New("aiand: invalid grant")
)
