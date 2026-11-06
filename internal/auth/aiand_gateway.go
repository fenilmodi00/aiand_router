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
)
