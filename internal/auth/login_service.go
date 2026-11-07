package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"
)

// loginPollIntervalDefault is the poll cadence the dashboard starts with when
// the gateway advertises none (RFC 8628 default is five seconds).
const loginPollIntervalDefault = 5 * time.Second

// loginDeviceCodeTTLDefault bounds a login attempt when the gateway omits
// expires_in, matching StartAiandDeviceLogin's aiandDeviceCodeTimeout.
const loginDeviceCodeTTLDefault = 15 * time.Minute

// loginSlowDownStep is the interval increase the RFC 8628 slow_down outcome
// demands.
const loginSlowDownStep = 5 * time.Second

// deviceCodeAADLabel is the purpose half of the device-code ciphertext's AAD.
// The attempt row's id is the external-id half; aadFor joins them.
const deviceCodeAADLabel = "aiand-device"

// LoginService is the SaaS dashboard login core: device-flow and paste-key
// logins that converge on one login transaction — account upsert, installation
// ensure, aiand credential + external key, attempt consumption, session mint.
// Deliberately separate from *Service: the admin axis it replaces is deleted
// by the same reform, and the login unit of work has its own dependency set.
// Composition happens in cmd/router/main.go.
type LoginService struct {
	accounts      AccountRepository
	sessions      SessionRepository
	attempts      LoginAttemptRepository
	installations InstallationRepository
	credentials   AiandCredentialRepository
	externalKeys  ExternalAPIKeyRepository
	gateway       AiandLoginGateway
	encryptor     Encryptor
	txRunner      TxRunner
	notifier      InstallationChangeNotifier
	now           Clock
	hostname      string
}

// NewLoginService wires the login service. The transaction seam (TxRunner)
// carries the tx-scoped repository set; the outer repositories exist for the
// reads outside the unit of work (attempt load, session verify). hostname
// labels gateway-minted keys; empty resolves os.Hostname() here, at
// construction, so the request path stays I/O-free.
func NewLoginService(
	accounts AccountRepository,
	sessions SessionRepository,
	attempts LoginAttemptRepository,
	installations InstallationRepository,
	credentials AiandCredentialRepository,
	externalKeys ExternalAPIKeyRepository,
	gateway AiandLoginGateway,
	encryptor Encryptor,
	txRunner TxRunner,
	now Clock,
	notifier InstallationChangeNotifier,
) (*LoginService, error) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "router-host"
	}
	return &LoginService{
		accounts:      accounts,
		sessions:      sessions,
		attempts:      attempts,
		installations: installations,
		credentials:   credentials,
		externalKeys:  externalKeys,
		gateway:       gateway,
		encryptor:     encryptor,
		txRunner:      txRunner,
		notifier:      notifier,
		now:           now,
		hostname:      hostname,
	}, nil
}

// LoginDeviceStart is the dashboard-facing half of a device-login session.
type LoginDeviceStart struct {
	LoginID                 string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

// StartDeviceLogin opens a device-flow login: the gateway mints a code, the
// service seals it into a login attempt row (AAD: attempt id, aiand-device)
// and returns only the user-facing fields. The device code itself never
// leaves the sealed row.
func (s *LoginService) StartDeviceLogin(ctx context.Context) (LoginDeviceStart, error) {
	code, err := s.gateway.DeviceCodeStart(ctx, "aiand-router@"+s.hostname)
	if err != nil {
		return LoginDeviceStart{}, aiandGatewayError(err)
	}
	interval := code.Interval
	if interval <= 0 {
		interval = int(loginPollIntervalDefault / time.Second)
	}
	expiresIn := code.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = int(loginDeviceCodeTTLDefault / time.Second)
	}
	now := s.now()
	attemptID, err := newAttemptID()
	if err != nil {
		return LoginDeviceStart{}, fmt.Errorf("mint login attempt id: %w", err)
	}
	ciphertext, err := s.encryptor.Encrypt([]byte(code.DeviceCode), attemptID, deviceCodeAADLabel)
	if err != nil {
		return LoginDeviceStart{}, fmt.Errorf("seal device code: %w", err)
	}
	attempt, err := s.attempts.InsertLoginAttempt(ctx, InsertLoginAttemptParams{
		ID:                   attemptID,
		DeviceCodeCiphertext: ciphertext,
		Interval:             time.Duration(interval) * time.Second,
		ExpiresAt:            now.Add(time.Duration(expiresIn) * time.Second),
	})
	if err != nil {
		return LoginDeviceStart{}, err
	}
	return LoginDeviceStart{
		LoginID:                 attempt.ID,
		UserCode:                code.UserCode,
		VerificationURI:         code.VerificationURI,
		VerificationURIComplete: code.VerificationURIComplete,
		ExpiresIn:               expiresIn,
		Interval:                interval,
	}, nil
}

// LoginPollOutcome is one poll result. Identity/Session/ExpiresAt are
// populated only when Status is authorized.
type LoginPollOutcome struct {
	Status    string
	Email     string
	OrgName   string
	Identity  AiandIdentity
	Session   *LoginSessionGrant
	ExpiresAt *time.Time
}

// LoginSessionGrant is a minted session: the raw cookie token (returned once,
// never stored), its expiry, and the account the session authenticates.
type LoginSessionGrant struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
}

// PollDeviceLogin sends one gateway token request for the attempt. The
// dashboard owns the waiting loop: it polls on the advertised interval and
// raises it by RetryAfter when this returns *AiandSlowDownError. Pending,
// denied, and expired outcomes come back as statuses, not errors; only
// gateway unavailability and wiring gaps are errors. An authorized outcome
// carries the minted session grant.
func (s *LoginService) PollDeviceLogin(ctx context.Context, loginID string) (LoginPollOutcome, error) {
	startedAt := time.Now()
	attempt, err := s.attempts.GetActiveLoginAttempt(ctx, loginID)
	if err != nil {
		return LoginPollOutcome{}, err
	}
	deviceCode, err := s.encryptor.Decrypt(attempt.DeviceCode, attempt.ID, deviceCodeAADLabel)
	if err != nil {
		return LoginPollOutcome{}, fmt.Errorf("open device code: %w", err)
	}
	tokenStartedAt := time.Now()
	token, err := s.gateway.PollToken(ctx, string(deviceCode))
	tokenExchange := time.Since(tokenStartedAt)
	if err != nil {
		switch {
		case errors.Is(err, ErrAiandAuthorizationPending):
			s.logPollOutcome(ctx, AiandPollStatusPending, nil)
			return LoginPollOutcome{Status: AiandPollStatusPending}, nil
		case errors.Is(err, ErrAiandSlowDown):
			// RFC 8628 §3.5: the client must raise its interval by five
			// seconds. Nothing persists — the sealed row stays as inserted;
			// the error itself carries the bumped interval.
			s.logPollOutcome(ctx, AiandPollStatusSlowDown, nil)
			return LoginPollOutcome{}, &AiandSlowDownError{RetryAfter: attempt.Interval + loginSlowDownStep}
		case errors.Is(err, ErrAiandAccessDenied):
			// Terminal: the user rejected the login, so the attempt is
			// worthless and is cleared immediately.
			if delErr := s.attempts.DeleteLoginAttempt(ctx, attempt.ID); delErr != nil {
				observability.FromContext(ctx).Warn("login: could not clear denied attempt", "login_id", attempt.ID, "err", delErr)
			}
			s.logPollOutcome(ctx, AiandPollStatusDenied, nil)
			return LoginPollOutcome{Status: AiandPollStatusDenied}, nil
		case errors.Is(err, ErrAiandExpiredToken):
			// Terminal: the device code expired, so the attempt is
			// worthless and is cleared immediately.
			if delErr := s.attempts.DeleteLoginAttempt(ctx, attempt.ID); delErr != nil {
				observability.FromContext(ctx).Warn("login: could not clear expired attempt", "login_id", attempt.ID, "err", delErr)
			}
			s.logPollOutcome(ctx, AiandPollStatusExpired, nil)
			return LoginPollOutcome{Status: AiandPollStatusExpired}, nil
		}
		return LoginPollOutcome{}, aiandGatewayError(err)
	}

	// Resolve the identity before the unit of work: a failed user lookup is
	// an incomplete gateway response, not a persistence failure, and the
	// attempt must stay pollable so the next poll can retry.
	identityStartedAt := time.Now()
	identity, err := s.resolveIdentity(ctx, token)
	identityResolution := time.Since(identityStartedAt)
	if err != nil {
		s.logPollOutcome(ctx, aiandPollStatusFailed, err)
		return LoginPollOutcome{}, err
	}

	// The attempt stays pollable until the full persistence transaction
	// commits: a transient DB failure must not convert retries into
	// ErrLoginAttemptNotFound. Only a committed completion consumes it.
	txStartedAt := time.Now()
	grant, expiresAt, err := s.completeLoginTx(ctx, attempt.ID, identity, token)
	transaction := time.Since(txStartedAt)
	if err != nil {
		s.logPollOutcome(ctx, aiandPollStatusFailed, err)
		return LoginPollOutcome{}, err
	}
	observability.FromContext(ctx).Info("login: device poll authorized", "token_exchange_ms", tokenExchange.Milliseconds(), "identity_ms", identityResolution.Milliseconds(), "transaction_ms", transaction.Milliseconds(), "total_ms", time.Since(startedAt).Milliseconds())
	s.logPollOutcome(ctx, AiandPollStatusAuthorized, nil)
	return LoginPollOutcome{
		Status:    AiandPollStatusAuthorized,
		Email:     identity.Email,
		OrgName:   identity.OrgName,
		Identity:  identity,
		Session:   grant,
		ExpiresAt: expiresAt,
	}, nil
}

// LoginWithKey validates a pasted Aiand key and completes the same login
// transaction the device flow uses. ErrAiandInvalidKey surfaces for a
// rejected key; other gateway failures wrap ErrAiandGatewayUnavailable.
func (s *LoginService) LoginWithKey(ctx context.Context, key string) (LoginSessionGrant, error) {
	// A pasted key is org-scoped: VerifyKey resolves the key's org from the
	// gateway (key-scoped response header, falling back to a sole membership)
	// so paste and device logins converge on the same account identity.
	identity, err := s.gateway.VerifyKey(ctx, key)
	if err != nil {
		if errors.Is(err, ErrAiandInvalidKey) {
			return LoginSessionGrant{}, ErrAiandInvalidKey
		}
		return LoginSessionGrant{}, aiandGatewayError(err)
	}
	grant, _, err := s.completeLoginTx(ctx, "", identity, AiandTokenResponse{AccessToken: key})
	if err != nil {
		return LoginSessionGrant{}, err
	}
	return *grant, nil
}

// resolveIdentity builds the login identity from a token response: the
// gateway's embedded org when present, the /api/user fallback otherwise.
// ValidateKey serves double duty — pasted keys validate through it, and the
// device flow uses it to fetch the account email the token response lacks.
func (s *LoginService) resolveIdentity(ctx context.Context, token AiandTokenResponse) (AiandIdentity, error) {
	user, err := s.gateway.ValidateKey(ctx, token.AccessToken)
	if err != nil {
		return AiandIdentity{}, err
	}
	if user.ID == "" {
		return AiandIdentity{}, fmt.Errorf("%w: gateway returned an empty user id", ErrAiandGateway)
	}
	identity := AiandIdentity{UserID: user.ID, Email: user.Email}
	if token.Org != nil && token.Org.ID != "" {
		identity.OrgID = token.Org.ID
		identity.OrgName = token.Org.Name
		return identity, nil
	}
	// Older token responses omit the embedded org. Resolve it from the key
	// itself so the account still lands on the org the credential is scoped
	// to, rather than the zero-valued org.
	verified, err := s.gateway.VerifyKey(ctx, token.AccessToken)
	if err != nil {
		return AiandIdentity{}, err
	}
	identity.OrgID = verified.OrgID
	identity.OrgName = verified.OrgName
	return identity, nil
}

// completeLoginTx runs the whole login persistence as ONE transaction:
// account upsert (keyed on the identity pair), installation ensure (external
// id = account id — the AAD identity), aiand credential + propagated external
// key, attempt consumption, session mint. A failure between any two writes
// rolls all of them back, so no half-linked artifacts survive. The cache
// invalidation fires only after the commit.
//
// attemptID is non-empty on the device path: the login transaction consumes
// the attempt only after every write succeeded, so a mid-transaction failure
// leaves the attempt pollable. Empty (paste path) skips consumption.
func (s *LoginService) completeLoginTx(ctx context.Context, attemptID string, identity AiandIdentity, token AiandTokenResponse) (*LoginSessionGrant, *time.Time, error) {
	accountID := GenerateID(AccountIDPrefix)
	var grant *LoginSessionGrant
	var credentialExpiresAt time.Time
	var winnerInstallationID string
	err := s.txRunner.InTx(ctx, func(tx Tx) error {
		installation, err := s.ensureInstallation(ctx, tx, accountID)
		if err != nil {
			return err
		}
		account, err := s.ensureAccount(ctx, tx, identity, accountID, installation.ID)
		if err != nil {
			return err
		}
		winnerInstallationID = account.InstallationID
		// The credential/key writes need BOTH identities: the AAD external id
		// (account id) and the installation row UUID the foreign keys point
		// at. On the arbitration-loss path the winner's account (and hence
		// its installation) won, so the UUID must come from the returned
		// account — the local installation row was soft-deleted.
		expiresAt, err := s.writeCredentialAndKey(ctx, tx, account, account.InstallationID, account.ID, identity, token)
		if err != nil {
			return err
		}
		credentialExpiresAt = expiresAt
		session, err := s.mintSession(ctx, tx, account.ID)
		if err != nil {
			return err
		}
		grant = &LoginSessionGrant{Token: session.token, ExpiresAt: session.expiresAt, Account: *account}
		if attemptID != "" {
			if err := tx.LoginAttempts.MarkLoginAttemptConsumed(ctx, attemptID); err != nil {
				return fmt.Errorf("consume login attempt: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// Only on commit: peers must never invalidate ahead of the durable write.
	// The invalidation carries the installation UUID — the identity every
	// cache/routing listener keys on (matching the legacy aiand flow).
	s.notifier.NotifyInstallationChanged(winnerInstallationID)
	return grant, &credentialExpiresAt, nil
}

// ensureInstallation creates the login's installation on first login and
// reuses it afterwards. external_id = account id and name = account id: the
// external_id is the AAD identity every sealed secret binds to, and a stable
// name keeps the (external_id, name) unique index from fanning out rows. On a
// concurrent same-identity login both transactions create with the same
// deterministic identity; the unique-index loser re-lists and reuses the
// winner's row — mirroring UpsertAccountOnLogin's arbitration.
func (s *LoginService) ensureInstallation(ctx context.Context, tx Tx, accountID string) (*Installation, error) {
	existing, err := tx.Installations.ListForExternalID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, inst := range existing {
		if inst != nil && inst.DeletedAt == nil {
			return inst, nil
		}
	}
	created, createErr := tx.Installations.Create(ctx, CreateInstallationParams{
		ExternalID: accountID,
		Name:       accountID,
	})
	if createErr == nil {
		return created, nil
	}
	// Lost a concurrent same-identity race: reuse the winner's installation.
	existing, err = tx.Installations.ListForExternalID(ctx, accountID)
	if err != nil {
		return nil, createErr
	}
	for _, inst := range existing {
		if inst != nil && inst.DeletedAt == nil {
			return inst, nil
		}
	}
	return nil, createErr
}

// ensureAccount creates-or-reuses the account for the identity, pointing at
// the installation ensured in the same transaction.
func (s *LoginService) ensureAccount(ctx context.Context, tx Tx, identity AiandIdentity, accountID, installationID string) (*Account, error) {
	account, err := tx.Accounts.UpsertAccountOnLogin(ctx, UpsertAccountOnLoginParams{
		ID:             accountID,
		AiandUserID:    identity.UserID,
		AiandOrgID:     identity.OrgID,
		Email:          identity.Email,
		InstallationID: installationID,
	})
	if err != nil {
		return nil, err
	}
	if account.ID != accountID {
		// Lost the concurrent same-identity race: the winner's account (and
		// its installation) won the arbitration. Soft-delete the installation
		// this transaction created with the pre-minted id so the loser's
		// uncommitted spec-row never outlives the transaction as an orphan.
		if delErr := tx.Installations.SoftDelete(ctx, accountID, installationID); delErr != nil {
			return nil, fmt.Errorf("retire losing installation: %w", delErr)
		}
	}
	return account, nil
}

// writeCredentialAndKey seals and writes the installation's aiand credential
// (device origin; the access token expires per the gateway, a pasted key is
// bookkeeping-only) and propagates the access token as the installation's
// aiand external key so BYOK dispatch serves it unchanged. Both writes land
// inside the caller's transaction.
//
// Two identities, deliberately distinct:
//   - aadExternalID (account.ID): the AAD every ciphertext is sealed with.
//     The installation's external_id IS the account id, so this matches the
//     legacy flow's aiandExternalID(installation).
//   - installationID (account.InstallationID): the installation row UUID the
//     credential and external-key foreign keys point at. The login flow
//     passes the WINNING account's installation: on the arbitration-loss
//     path account.InstallationID names the winner's row, while the local
//     installation this transaction ensured was soft-deleted.
func (s *LoginService) writeCredentialAndKey(ctx context.Context, tx Tx, account *Account, installationID, aadExternalID string, identity AiandIdentity, token AiandTokenResponse) (time.Time, error) {
	// external_id immutability (AAD lifetime): the installation's external_id
	// IS the account id and is sealed into every credential/key ciphertext as
	// AAD. It is set once at installation creation and never renamed — a
	// rename would orphan the sealed material (unsealing fails under a
	// changed AAD). See Account's doc comment.
	accessCipher, err := s.encryptor.Encrypt([]byte(token.AccessToken), aadExternalID, providers.ProviderAIAND)
	if err != nil {
		return time.Time{}, fmt.Errorf("seal aiand access token: %w", err)
	}
	var refreshCipher []byte
	if token.RefreshToken != "" {
		refreshCipher, err = s.encryptor.Encrypt([]byte(token.RefreshToken), aadExternalID, providers.ProviderAIAND)
		if err != nil {
			return time.Time{}, fmt.Errorf("seal aiand refresh token: %w", err)
		}
	}
	if refreshCipher == nil {
		refreshCipher = []byte{} // empty bytea, never SQL NULL (refresh_token_ciphertext NOT NULL)
	}
	origin := AiandOriginPaste
	expiresAt := s.now().Add(aiandPasteTokenLifetime)
	if token.RefreshToken != "" {
		origin = AiandOriginDevice
		if token.ExpiresIn > 0 {
			expiresAt = s.now().Add(time.Duration(token.ExpiresIn) * time.Second)
		}
	}
	orgID, orgName := identity.OrgID, identity.OrgName
	if token.Org != nil && token.Org.ID != "" {
		orgID, orgName = token.Org.ID, token.Org.Name
	}
	if _, err := tx.AiandCredentials.Upsert(ctx, UpsertAiandCredentialParams{
		InstallationID:         installationID,
		AccessTokenCiphertext:  accessCipher,
		RefreshTokenCiphertext: refreshCipher,
		ExpiresAt:              expiresAt,
		Origin:                 origin,
		UserEmail:              identity.Email,
		OrgID:                  orgID,
		OrgName:                orgName,
	}); err != nil {
		return time.Time{}, err
	}
	create, err := externalAPIKeyCreateParamsWith(s.encryptor, installationID, UpsertExternalAPIKeyParams{
		Provider: providers.ProviderAIAND,
		RawKey:   token.AccessToken,
	})
	if err != nil {
		return time.Time{}, err
	}
	// One active key per (installation, provider). Re-login/reconnect must
	// retire the previous key before inserting; otherwise the partial unique
	// index rejects the new key and aborts the whole login transaction.
	if err := tx.ExternalAPIKeys.SoftDeleteByProvider(ctx, installationID, create.Provider); err != nil {
		return time.Time{}, fmt.Errorf("retire previous aiand key: %w", err)
	}
	if _, err := tx.ExternalAPIKeys.Create(ctx, create); err != nil {
		return time.Time{}, fmt.Errorf("propagate aiand access token: %w", err)
	}
	return expiresAt, nil
}

// The propagated external key reuses the Service's sealing/normalization
// helper so both write paths produce byte-identical create params.
// mintedSession is one session minted inside the transaction: the raw token
// (returned to the caller once) plus the stored hash/prefix/expiry.
type mintedSession struct {
	token     string
	expiresAt time.Time
}

// mintSession mints one opaque cookie session inside the transaction: a
// 32-byte crypto/rand token, stored as its SHA-256 hex hash with the same
// 8+4 display prefix as API keys, expiring after the sliding TTL.
func (s *LoginService) mintSession(ctx context.Context, tx Tx, accountID string) (*mintedSession, error) {
	raw, err := generateSessionToken()
	if err != nil {
		return nil, fmt.Errorf("mint session token: %w", err)
	}
	hash, prefix, _ := APITokenFingerprint(raw)
	expiresAt := s.now().Add(LoginSessionTTL)
	if _, err := tx.Sessions.InsertAccountSession(ctx, InsertSessionParams{
		AccountID:   accountID,
		TokenHash:   hash,
		TokenPrefix: prefix,
		ExpiresAt:   expiresAt,
	}); err != nil {
		return nil, err
	}
	return &mintedSession{token: raw, expiresAt: expiresAt}, nil
}

// Logout revokes the session the cookie token names. Idempotent: revoking an
// unknown or already-revoked token is not an error.
func (s *LoginService) Logout(ctx context.Context, token string) error {
	return s.sessions.RevokeSessionByTokenHash(ctx, HashAPIKeySHA256(token))
}

// VerifySessionToken resolves a live session (and its account + installation)
// by cookie token. ErrSessionNotFound for unknown, expired, or revoked
// tokens, and for soft-deleted accounts.
func (s *LoginService) VerifySessionToken(token string) (*ActiveSession, error) {
	return s.sessions.GetActiveSessionByTokenHash(context.Background(), GetActiveSessionParams{TokenHash: HashAPIKeySHA256(token)})
}

// TouchSessionLastSeen slides the window of a live session to now + TTL; a
// no-op for revoked or expired sessions.
func (s *LoginService) TouchSessionLastSeen(tokenHash string) error {
	return s.sessions.TouchSessionLastSeen(context.Background(), TouchSessionParams{
		TokenHash: tokenHash,
		ExpiresAt: s.now().Add(LoginSessionTTL),
	})
}

// logPollOutcome emits one DEBUG line per poll outcome at the service
// boundary. It never carries the device code, user code, or token material —
// only the coarse outcome and, for failures, the error.
func (s *LoginService) logPollOutcome(ctx context.Context, status string, err error) {
	if err != nil {
		observability.FromContext(ctx).Debug("login: device poll outcome", "poll_status", status, "err", err)
		return
	}
	observability.FromContext(ctx).Debug("login: device poll outcome", "poll_status", status)
}

// newAttemptID mints the attempt row's UUID: the unguessable poll key AND the
// AAD identity the device code is sealed with.
func newAttemptID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// RFC 4122 version 4 shape; gen_random_uuid() would mint the same.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// generateSessionToken returns the opaque cookie token: 32 crypto/rand bytes
// hex-encoded. The row stores only its SHA-256 hash.
func generateSessionToken() (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
