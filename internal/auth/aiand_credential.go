package auth

import (
	"context"
	"errors"
	"time"
)

// AiandCredentialOrigin distinguishes how the installation-wide Aiand key was
// obtained: the dashboard device flow (refreshable) or a pasted key (not).
type AiandCredentialOrigin string

const (
	AiandOriginDevice AiandCredentialOrigin = "device"
	AiandOriginPaste  AiandCredentialOrigin = "paste"
)

// AiandCredential is the installation-wide Aiand upstream credential. The
// repository returns ciphertext fields exactly as stored; the auth Service
// opens them with the shared Tink Encryptor (AAD: installation external ID,
// provider "aiand") into Plaintext/PlainRefresh, which exist only for the
// request lifetime and are never logged.
type AiandCredential struct {
	ID             string
	InstallationID string
	// AccessTokenCiphertext and RefreshTokenCiphertext are the stored bytes.
	AccessTokenCiphertext  []byte
	RefreshTokenCiphertext []byte
	// Plaintext is the sk-... access token, decrypted on read by the service.
	Plaintext []byte
	// PlainRefresh is the decrypted single-use refresh token; empty for
	// paste-origin credentials, which have nothing to rotate.
	PlainRefresh []byte
	ExpiresAt    time.Time
	Origin       AiandCredentialOrigin
	UserEmail    string
	OrgID        string
	OrgName      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UpsertAiandCredentialParams replaces the installation's credential.
type UpsertAiandCredentialParams struct {
	InstallationID string
	// AccessTokenCiphertext is the Tink-sealed sk-... token.
	AccessTokenCiphertext []byte
	// RefreshTokenCiphertext is empty for paste-origin credentials.
	RefreshTokenCiphertext []byte
	ExpiresAt              time.Time
	Origin                 AiandCredentialOrigin
	UserEmail              string
	OrgID                  string
	OrgName                string
}

// AiandCredentialRepository stores the one credential per installation.
type AiandCredentialRepository interface {
	Upsert(ctx context.Context, params UpsertAiandCredentialParams) (*AiandCredential, error)
	// GetByInstallation returns ErrAiandCredentialNotFound when the
	// installation has no credential yet.
	GetByInstallation(ctx context.Context, installationID string) (*AiandCredential, error)
	// Delete removes the row; deleting an absent row is not an error.
	Delete(ctx context.Context, installationID string) error
	// ListExpiring returns every credential whose access token expires before
	// the given threshold, joined with the owning installation's external ID
	// for AAD unsealing. Installations soft-deleted upstream never appear.
	// Rotation still decides per row; paste-origin rows are listed and then
	// skipped so the sweep keeps its queries dumb and the policy in one place.
	ListExpiring(ctx context.Context, before time.Time) ([]AiandExpiringCredential, error)
}

// AiandExpiringCredential is one sweep row: the stored credential plus the
// installation columns the rotation path needs. Ciphertexts stay exactly as
// stored; only the auth Service opens them.
type AiandExpiringCredential struct {
	Credential AiandCredential
	// InstallationExternalID is the AAD identity the credential's ciphertexts
	// are bound to. Empty when the installation's external_id column is empty,
	// in which case the rotation path falls back to the installation ID.
	InstallationExternalID string
}

// Errors for the Aiand credential lifecycle. Handlers map these to HTTP.
var (
	// ErrAiandCredentialNotFound means the installation has no connected Aiand credential.
	ErrAiandCredentialNotFound = errors.New("aiand credential not found")
	// ErrInvalidAiandKey means the gateway rejected a pasted key (HTTP 401 upstream).
	ErrInvalidAiandKey = errors.New("invalid aiand key")
	// ErrAiandLoginPending means no device login has been started (or it already finished).
	ErrAiandLoginPending = errors.New("aiand device login not started")
	// ErrAiandLoginExpired means the device code expired before the user approved it.
	ErrAiandLoginExpired = errors.New("aiand device login expired")
	// ErrAiandAccessDenied means the user rejected the login in the browser.
	ErrAiandAccessDenied = errors.New("aiand device login denied")
	// ErrAiandGatewayUnavailable wraps every gateway transport/HTTP failure;
	// handlers render it as 502 so operators see an upstream problem, not a
	// router bug.
	ErrAiandGatewayUnavailable = errors.New("aiand gateway unavailable")
)
