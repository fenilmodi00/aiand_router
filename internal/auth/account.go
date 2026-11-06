package auth

import (
	"context"
	"errors"
	"time"
)

// Account is the router's SaaS identity row: one account per Aiand identity
// (user id + org id pair). The ID is 'acct_' + 24 random chars
// (GenerateID("acct")) and doubles as the installation's external_id, which
// is the AAD identity the Tink Encryptor binds ciphertexts to.
//
// external_id / account-ID immutability (AAD lifetime): the installation's
// external_id is initialized from the account ID and NEVER renamed or
// rewritten. It is the additional authenticated data for every sealed
// aiand credential, external API key, and device-code ciphertext tied to the
// account; renaming it would orphan the sealed material (unsealing fails
// under a changed AAD). Treat external_id as append-only AAD for the whole
// lifetime of the account and its installation.
type Account struct {
	ID             string
	AiandUserID    string
	AiandOrgID     string
	Email          string
	DisplayName    string
	InstallationID string
	CreatedAt      time.Time
	LastLoginAt    time.Time
	DeletedAt      *time.Time
}

// LoginSession is one dashboard cookie session. TokenHash is the SHA-256 hex
// of the opaque cookie token (never the token itself); TokenPrefix is
// display-only (same 8+4 convention as API keys). Expiry is sliding:
// middleware calls Touch on verified requests, which extends both
// LastSeenAt and ExpiresAt.
type LoginSession struct {
	ID          string
	AccountID   string
	TokenHash   string
	TokenPrefix string
	IssuedAt    time.Time
	LastSeenAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// LoginAttempt is one in-flight device-flow login, keyed by an unguessable
// row UUID. DeviceCodeCiphertext is Tink-sealed with AAD (attempt ID string,
// "aiand-device"); only the login service opens it. An attempt is consumed
// only after the full login persistence transaction commits; consumed rows
// are retained until GC for audit.
type LoginAttempt struct {
	ID         string
	DeviceCode []byte
	Interval   time.Duration
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// UpsertAccountOnLoginParams creates-or-reuses the account for one login.
// The identity pair is the arbitration key; email and display name are
// display-only and never part of the identity.
type UpsertAccountOnLoginParams struct {
	// ID optionally pins the account id. The login service always sets it:
	// the account id doubles as the installation's external_id (the AAD
	// identity), so it must be known before the installation row is created
	// in the same transaction. Empty keeps the adapter-generated
	// GenerateID(AccountIDPrefix).
	ID          string
	AiandUserID string
	AiandOrgID  string
	Email       string
	DisplayName string
	// InstallationID is the installation created (or reused) for this login;
	// the caller owns installation creation so the whole login unit of work
	// stays in one transaction.
	InstallationID string
}

// GetActiveSessionParams verifies one cookie token by hash. The repository
// resolves the owning account and its installation in the same row so the
// middleware gets full request scoping in one query.
type GetActiveSessionParams struct {
	TokenHash string
}

// LoginSessionCookieName is the HttpOnly cookie carrying the dashboard
// session token.
const LoginSessionCookieName = "router_account_session"

// LoginSessionTTL is the dashboard session's sliding validity window. The
// middleware slides it via TouchSessionLastSeen on verified requests.
const LoginSessionTTL = 7 * 24 * time.Hour

// SessionVerifier is the narrow middleware-facing method set on the login
// service: verify a cookie token to its session + account + installation,
// and slide the window. internal/server/middleware depends on this
// interface, never on *LoginService or the repositories.
type SessionVerifier interface {
	// VerifySessionToken resolves a live (unrevoked, unexpired,
	// non-deleted account) session; ErrSessionNotFound otherwise.
	VerifySessionToken(token string) (*ActiveSession, error)
	// TouchSessionLastSeen slides the window of a live session to now + TTL.
	TouchSessionLastSeen(tokenHash string) error
}

// sessionTokenBytes is the raw entropy of one opaque cookie token: 32 random
// bytes, hex-encoded on the cookie, hashed for the row.
const sessionTokenBytes = 32

// ActiveSession is the middleware-facing result of a session lookup: the
// session row plus the owning account and installation in one read.
type ActiveSession struct {
	Session      LoginSession
	Account      Account
	Installation *Installation
}

// InsertSessionParams mints one session row for an account.
type InsertSessionParams struct {
	AccountID   string
	TokenHash   string
	TokenPrefix string
	ExpiresAt   time.Time
}

// TouchSessionParams slides a live session's window: LastSeenAt moves to the
// server's now and ExpiresAt to the supplied instant.
type TouchSessionParams struct {
	TokenHash string
	ExpiresAt time.Time
}

// InsertLoginAttemptParams records one device-flow start.
type InsertLoginAttemptParams struct {
	// ID optionally pins the attempt row's UUID. The login service always
	// sets it: the attempt id is the AAD identity the device code is sealed
	// with, so it must exist before the seal. Empty keeps the
	// DB-generated default.
	ID string
	// DeviceCodeCiphertext is the Tink-sealed gateway device code.
	DeviceCodeCiphertext []byte
	// Interval is the gateway-declared polling interval.
	Interval  time.Duration
	ExpiresAt time.Time
}

// AiandIdentity is what a verified Aiand key resolves to: the stable ids the
// router keys accounts on, plus display-only attributes.
type AiandIdentity struct {
	// UserID and OrgID are the stable identity pair from the gateway; the
	// account identity key. One pasted sk- key resolves to exactly one pair.
	UserID string
	OrgID  string
	// Email and OrgName are display-only.
	Email   string
	OrgName string
}

// AiandIdentityVerifier resolves an Aiand sk- key to its identity. Pure:
// it performs I/O against the Aiand gateway but does not touch router
// storage. internal/aiandauth provides the adapter; the login service
// depends on this interface only.
type AiandIdentityVerifier interface {
	VerifyKey(ctx context.Context, key string) (AiandIdentity, error)
}

// AccountRepository stores the one account per Aiand identity.
type AccountRepository interface {
	// UpsertAccountOnLogin creates the account for a first-time identity or
	// returns the existing one. Concurrent first logins of the same identity
	// arbitrate on the partial unique index: the unique-violation loser
	// re-reads by identity and reuses the winner's account + installation,
	// so no conflict error reaches the user. The winner's
	// LastLoginAt/Email/DisplayName are not overwritten by the loser.
	UpsertAccountOnLogin(ctx context.Context, params UpsertAccountOnLoginParams) (*Account, error)
	// GetAccountByIdentity returns ErrAccountNotFound for an unknown or
	// soft-deleted identity.
	GetAccountByIdentity(ctx context.Context, aiandUserID, aiandOrgID string) (*Account, error)
	// GetAccountByID returns ErrAccountNotFound for an unknown or
	// soft-deleted account.
	GetAccountByID(ctx context.Context, id string) (*Account, error)
	// SoftDeleteAccount marks the account deleted; sessions stop resolving.
	// Deleting an already-deleted or absent account is not an error.
	SoftDeleteAccount(ctx context.Context, id string) error
}

// SessionRepository stores dashboard cookie sessions (hashed tokens only).
type SessionRepository interface {
	// InsertAccountSession mints a session row.
	InsertAccountSession(ctx context.Context, params InsertSessionParams) (*LoginSession, error)
	// GetActiveSessionByTokenHash resolves a live (unrevoked, unexpired,
	// non-deleted account) session to its account + installation in one read.
	// Returns ErrSessionNotFound otherwise.
	GetActiveSessionByTokenHash(ctx context.Context, params GetActiveSessionParams) (*ActiveSession, error)
	// TouchSessionLastSeen slides the window of a live session; a no-op for
	// revoked or expired sessions.
	TouchSessionLastSeen(ctx context.Context, params TouchSessionParams) error
	// RevokeSessionByTokenHash revokes one session; idempotent.
	RevokeSessionByTokenHash(ctx context.Context, tokenHash string) error
	// RevokeAllSessionsForAccount revokes every live session of the account
	// and returns how many were revoked.
	RevokeAllSessionsForAccount(ctx context.Context, accountID string) (int64, error)
}

// LoginAttemptRepository stores in-flight device-flow attempts.
type LoginAttemptRepository interface {
	// InsertLoginAttempt records a new attempt and returns it with its
	// server-generated unguessable ID.
	InsertLoginAttempt(ctx context.Context, params InsertLoginAttemptParams) (*LoginAttempt, error)
	// GetActiveLoginAttempt returns the unconsumed, unexpired attempt with
	// ErrLoginAttemptNotFound otherwise.
	GetActiveLoginAttempt(ctx context.Context, id string) (*LoginAttempt, error)
	// MarkLoginAttemptConsumed stamps consumed_at once; a no-op if already
	// consumed.
	MarkLoginAttemptConsumed(ctx context.Context, id string) error
	// DeleteLoginAttempt removes the attempt row (clear-on-disconnect).
	// Deleting an absent row is not an error.
	DeleteLoginAttempt(ctx context.Context, id string) error
	// DeleteExpiredLoginAttempts GCs attempts whose window closed before
	// now and returns how many rows were removed.
	DeleteExpiredLoginAttempts(ctx context.Context, now time.Time) (int64, error)
}

// Errors for the account/session/attempt lifecycle. Handlers map these to
// HTTP.
var (
	// ErrAccountNotFound means no live account matches the identity or ID.
	ErrAccountNotFound = errors.New("account not found")
	// ErrSessionNotFound means the token hash matches no live session.
	ErrSessionNotFound = errors.New("account session not found")
	// ErrLoginAttemptNotFound means the attempt id is unknown, already
	// consumed, or expired.
	ErrLoginAttemptNotFound = errors.New("account login attempt not found")
)
