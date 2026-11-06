package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"
)

// aiandRotateWindow is the default look-ahead for RotateAiandCredentialIfExpiring.
const aiandRotateWindow = 72 * time.Hour

// aiandPasteTokenLifetime covers the expires_at column for pasted keys. They
// carry no refresh token, so rotation never applies; the timestamp is bookkeeping.
const aiandPasteTokenLifetime = 365 * 24 * time.Hour

// aiandDeviceCodeTimeout reaps abandoned in-memory logins. The gateway's own
// expires_in governs an active session; this backstop bounds the map when a
// start is never followed by a poll.
const aiandDeviceCodeTimeout = 15 * time.Minute

// AiandGateway wraps the gateway calls the credential lifecycle needs.
// The HTTP adapter in internal/aiandauth satisfies it; tests inject fakes.
// pendingAiandLogin is one in-flight device login. The device code is a
// bearer-equivalent handshake value: it lives only in memory and is never logged.
type pendingAiandLogin struct {
	deviceCode string
	externalID string
	expiresAt  time.Time
	// interval is the gateway-advertised poll cadence plus any slow_down
	// increments observed so far.
	interval int
}

// WithAiandGateway wires the gateway client, the credential repository, and
// the host label used for gateway-side key names. Kept off NewService so
// existing callers stay source-stable.
func (s *Service) WithAiandGateway(gateway AiandGateway, credentials AiandCredentialRepository, hostname string) *Service {
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	s.aiandGateway = gateway
	s.aiandCredentials = credentials
	s.aiandHostname = hostname
	return s
}

// hostnameOrDefault labels the gateway-minted key when os.Hostname() failed.
func hostnameOrDefault(hostname string) string {
	if hostname != "" {
		return hostname
	}
	return "router-host"
}

// AiandDeviceLogin is the dashboard-facing half of a device-login session.
type AiandDeviceLogin struct {
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

// Aiand poll statuses for PollAiandDeviceLogin.
const (
	AiandPollStatusPending    = "pending"
	AiandPollStatusAuthorized = "authorized"
	AiandPollStatusExpired    = "expired"
	AiandPollStatusDenied     = "denied"
	// AiandPollStatusSlowDown is not a returned outcome status (slow_down
	// surfaces as *AiandSlowDownError); it labels the poll-outcome log line.
	AiandPollStatusSlowDown = "slow_down"
)

// AiandPollOutcome is one poll result. KeyMask/ExpiresAt are populated only
// when Status is authorized.
type AiandPollOutcome struct {
	Status    string
	Email     string
	OrgName   string
	KeyMask   string
	ExpiresAt *time.Time
}

// AiandSlowDownError tells the dashboard to raise its poll interval per the
// gateway's slow_down contract (RFC 8628 §3.5): wait RetryAfter, then retry.
type AiandSlowDownError struct {
	RetryAfter time.Duration
}

// Error implements error.
func (e *AiandSlowDownError) Error() string {
	return fmt.Sprintf("aiand: poll again after %s", e.RetryAfter)
}

// StartAiandDeviceLogin opens a device-login session for the installation.
// Starting again while one is pending replaces it, so only the newest
// browser approval can complete. The device code stays in memory; the
// dashboard receives only the user-facing fields.
func (s *Service) StartAiandDeviceLogin(ctx context.Context, installation *Installation) (AiandDeviceLogin, error) {
	if installation == nil || s.aiandGateway == nil {
		return AiandDeviceLogin{}, ErrAiandGatewayUnavailable
	}
	code, err := s.aiandGateway.DeviceCodeStart(ctx, "aiand-router@"+hostnameOrDefault(s.aiandHostname))
	if err != nil {
		return AiandDeviceLogin{}, aiandGatewayError(err)
	}
	interval := code.Interval
	if interval <= 0 {
		interval = 5
	}
	expiresIn := code.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = int(aiandDeviceCodeTimeout / time.Second)
	}
	now := s.now()
	pending := &pendingAiandLogin{
		deviceCode: code.DeviceCode,
		externalID: aiandExternalID(installation),
		expiresAt:  now.Add(time.Duration(expiresIn) * time.Second),
		interval:   interval,
	}
	s.aiandMu.Lock()
	if s.aiandPending == nil {
		s.aiandPending = map[string]*pendingAiandLogin{}
	}
	s.aiandPending[installation.ID] = pending
	s.aiandMu.Unlock()
	return AiandDeviceLogin{
		UserCode:                code.UserCode,
		VerificationURI:         code.VerificationURI,
		VerificationURIComplete: code.VerificationURIComplete,
		ExpiresIn:               expiresIn,
		Interval:                interval,
	}, nil
}

// PollAiandDeviceLogin sends one gateway token request for the pending login.
// The dashboard owns the waiting loop: it polls on the advertised interval and
// raises it by RetryAfter when this returns *AiandSlowDownError. Pending,
// denied, and expired outcomes come back as statuses, not errors; only
// gateway unavailability and wiring gaps are errors.
func (s *Service) PollAiandDeviceLogin(ctx context.Context, installation *Installation) (AiandPollOutcome, error) {
	if installation == nil || s.aiandGateway == nil {
		return AiandPollOutcome{}, ErrAiandGatewayUnavailable
	}
	installationID := installation.ID
	s.aiandMu.Lock()
	pending, ok := s.aiandPending[installationID]
	interval := 0
	if ok {
		if s.now().After(pending.expiresAt) {
			delete(s.aiandPending, installationID)
			ok = false
		} else {
			interval = pending.interval
		}
	}
	s.aiandMu.Unlock()
	if !ok {
		return AiandPollOutcome{}, ErrAiandLoginPending
	}

	token, err := s.aiandGateway.PollToken(ctx, pending.deviceCode)
	if err != nil {
		switch {
		case errors.Is(err, ErrAiandAuthorizationPending):
			s.logAiandPollOutcome(ctx, AiandPollStatusPending, nil)
			return AiandPollOutcome{Status: AiandPollStatusPending}, nil
		case errors.Is(err, ErrAiandSlowDown):
			// RFC 8628: the client must increase its interval by 5 seconds.
			s.bumpAiandInterval(installationID, 5)
			s.logAiandPollOutcome(ctx, AiandPollStatusSlowDown, nil)
			return AiandPollOutcome{}, &AiandSlowDownError{RetryAfter: time.Duration(interval+5) * time.Second}
		case errors.Is(err, ErrAiandAccessDenied):
			// Terminal: the user rejected the login, so the attempt is
			// worthless and is dropped immediately.
			s.clearAiandPending(installationID)
			s.logAiandPollOutcome(ctx, AiandPollStatusDenied, nil)
			return AiandPollOutcome{Status: AiandPollStatusDenied}, nil
		case errors.Is(err, ErrAiandExpiredToken):
			// Terminal: the device code expired, so the attempt is
			// worthless and is dropped immediately.
			s.clearAiandPending(installationID)
			s.logAiandPollOutcome(ctx, AiandPollStatusExpired, nil)
			return AiandPollOutcome{Status: AiandPollStatusExpired}, nil
		}
		return AiandPollOutcome{}, aiandGatewayError(err)
	}

	// The attempt stays pollable until the full persistence transaction
	// succeeds: a transient gateway/DB failure must not convert retries into
	// ErrAiandLoginPending. Only a committed completion consumes it.
	credential, err := s.completeAiandCredential(ctx, installation, pending.externalID, token, AiandOriginDevice, "")
	if err != nil {
		s.logAiandPollOutcome(ctx, aiandPollStatusFailed, err)
		return AiandPollOutcome{}, err
	}
	s.clearAiandPending(installationID)
	s.logAiandPollOutcome(ctx, AiandPollStatusAuthorized, nil)
	expiresAt := credential.ExpiresAt
	return AiandPollOutcome{
		Status:    AiandPollStatusAuthorized,
		Email:     credential.UserEmail,
		OrgName:   credential.OrgName,
		KeyMask:   MaskAiandKey(string(credential.Plaintext)),
		ExpiresAt: &expiresAt,
	}, nil
}

// aiandPollStatusFailed marks the debug outcome line for a poll whose token
// exchange succeeded but whose persistence failed; the attempt stays
// pollable, so the dashboard's next poll can complete the login.
const aiandPollStatusFailed = "failed"

// logAiandPollOutcome emits one DEBUG line per poll outcome at the service
// boundary. It never carries the device code, user code, or token material —
// only the coarse outcome and, for failures, the error.
func (s *Service) logAiandPollOutcome(ctx context.Context, status string, err error) {
	if err != nil {
		observability.FromContext(ctx).Debug("aiand: device login poll outcome", "poll_status", status, "err", err)
		return
	}
	observability.FromContext(ctx).Debug("aiand: device login poll outcome", "poll_status", status)
}

// bumpAiandInterval records a gateway slow_down so the pending login's
// advertised interval reflects what the gateway has seen so far.
func (s *Service) bumpAiandInterval(installationID string, extra int) {
	s.aiandMu.Lock()
	defer s.aiandMu.Unlock()
	if pending, ok := s.aiandPending[installationID]; ok {
		pending.interval += extra
	}
}

// clearAiandPending drops the installation's pending login, if any.
func (s *Service) clearAiandPending(installationID string) {
	s.aiandMu.Lock()
	defer s.aiandMu.Unlock()
	delete(s.aiandPending, installationID)
}

// CompleteAiandPasteLogin validates a pasted key against the gateway and, on
// success, stores it as the installation-wide credential with origin "paste".
// Pasted keys have no refresh token: nothing is revocable or rotatable, so
// the refresh ciphertext is empty and expiry is bookkeeping only.
func (s *Service) CompleteAiandPasteLogin(ctx context.Context, installation *Installation, key string) (*AiandCredential, error) {
	if installation == nil || s.aiandGateway == nil {
		return nil, ErrAiandGatewayUnavailable
	}
	user, err := s.aiandGateway.ValidateKey(ctx, key)
	if err != nil {
		if errors.Is(err, ErrAiandInvalidKey) {
			return nil, ErrInvalidAiandKey
		}
		return nil, aiandGatewayError(err)
	}
	token := AiandTokenResponse{AccessToken: key}
	return s.completeAiandCredential(ctx, installation, aiandExternalID(installation), token, AiandOriginPaste, user.Email)
}

// completeAiandCredential seals and persists one credential pair, then
// propagates the access token as the installation's aiand provider key so the
// existing BYOK dispatch serves every router user with it. userEmail comes
// from the paste-login validation; the device flow fills it in separately.
// The credential row and the propagated external key are written in ONE
// transaction via TxRunner when wired: a failure between the two writes must
// roll both back, never leave a stored credential with no dispatch key.
// Cache invalidation fires only after the commit.
func (s *Service) completeAiandCredential(
	ctx context.Context,
	installation *Installation,
	externalID string,
	token AiandTokenResponse,
	origin AiandCredentialOrigin,
	userEmail string,
) (*AiandCredential, error) {
	if s.aiandCredentials == nil {
		return nil, ErrAiandGatewayUnavailable
	}
	if userEmail == "" && origin == AiandOriginDevice {
		// The token response carries no email; the account endpoint does. A
		// failure here is cosmetic, so the login still completes — with the
		// org fields when the gateway supplied them.
		if user, err := s.aiandGateway.ValidateKey(ctx, token.AccessToken); err == nil {
			userEmail = user.Email
		} else {
			observability.FromContext(ctx).Warn("aiand: could not read account email after device login", "err", err)
		}
	}
	accessCipher, err := s.encryptor.Encrypt([]byte(token.AccessToken), externalID, providers.ProviderAIAND)
	if err != nil {
		return nil, fmt.Errorf("seal aiand access token: %w", err)
	}
	var refreshCipher []byte
	if token.RefreshToken != "" {
		refreshCipher, err = s.encryptor.Encrypt([]byte(token.RefreshToken), externalID, providers.ProviderAIAND)
		if err != nil {
			return nil, fmt.Errorf("seal aiand refresh token: %w", err)
		}
	}
	if refreshCipher == nil {
		refreshCipher = []byte{} // empty bytea, never SQL NULL (refresh_token_ciphertext NOT NULL)
	}
	orgID, orgName := "", ""
	if token.Org != nil {
		orgID, orgName = token.Org.ID, token.Org.Name
	}
	expiresAt := s.now().Add(aiandPasteTokenLifetime)
	if origin == AiandOriginDevice && token.ExpiresIn > 0 {
		expiresAt = s.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	credentialParams := UpsertAiandCredentialParams{
		InstallationID:         installation.ID,
		AccessTokenCiphertext:  accessCipher,
		RefreshTokenCiphertext: refreshCipher,
		ExpiresAt:              expiresAt,
		Origin:                 origin,
		UserEmail:              userEmail,
		OrgID:                  orgID,
		OrgName:                orgName,
	}
	if s.txRunner != nil {
		return s.completeAiandCredentialTx(ctx, installation.ID, credentialParams, token)
	}
	return s.completeAiandCredentialLegacy(ctx, credentialParams, token)
}

// completeAiandCredentialTx writes the credential row and the propagated
// external key on one transaction. Only a successful commit returns the
// credential; the cache invalidation happens after it.
func (s *Service) completeAiandCredentialTx(
	ctx context.Context,
	installationID string,
	credentialParams UpsertAiandCredentialParams,
	token AiandTokenResponse,
) (*AiandCredential, error) {
	var credential *AiandCredential
	err := s.txRunner.InTx(ctx, func(tx Tx) error {
		upserted, err := tx.AiandCredentials.Upsert(ctx, credentialParams)
		if err != nil {
			return err
		}
		create, err := s.externalAPIKeyCreateParams(installationID, UpsertExternalAPIKeyParams{
			Provider: providers.ProviderAIAND,
			RawKey:   token.AccessToken,
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExternalAPIKeys.Create(ctx, create); err != nil {
			return fmt.Errorf("propagate aiand access token: %w", err)
		}
		credential = upserted
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	// Plaintext fields are the in-memory view of the just-minted pair; the
	// row's ciphertext fields stay as stored.
	credential.Plaintext = []byte(token.AccessToken)
	if token.RefreshToken != "" {
		credential.PlainRefresh = []byte(token.RefreshToken)
	}
	return credential, nil
}

// completeAiandCredentialLegacy is the pre-TxRunner write order (credential
// row, then external key, each on the pool). Kept for callers that wire no
// TxRunner; production wires one.
func (s *Service) completeAiandCredentialLegacy(
	ctx context.Context,
	credentialParams UpsertAiandCredentialParams,
	token AiandTokenResponse,
) (*AiandCredential, error) {
	credential, err := s.aiandCredentials.Upsert(ctx, credentialParams)
	if err != nil {
		return nil, err
	}
	if _, err := s.UpsertExternalAPIKey(ctx, credentialParams.InstallationID, UpsertExternalAPIKeyParams{
		Provider: providers.ProviderAIAND,
		RawKey:   token.AccessToken,
	}); err != nil {
		return nil, fmt.Errorf("propagate aiand access token: %w", err)
	}
	// Plaintext fields are the in-memory view of the just-minted pair; the
	// row's ciphertext fields stay as stored.
	credential.Plaintext = []byte(token.AccessToken)
	if token.RefreshToken != "" {
		credential.PlainRefresh = []byte(token.RefreshToken)
	}
	return credential, nil
}

// AiandCredentialStatus is the dashboard-facing connection summary. It never
// carries the raw key — KeyMask is first8+last4.
type AiandCredentialStatus struct {
	Connected bool
	Email     string
	OrgName   string
	KeyMask   string
	ExpiresAt *time.Time
	Origin    AiandCredentialOrigin
}

// AiandCredentialStatus summarizes the installation's Aiand connection.
func (s *Service) AiandCredentialStatus(ctx context.Context, installation *Installation) (AiandCredentialStatus, error) {
	if installation == nil || s.aiandCredentials == nil {
		return AiandCredentialStatus{}, ErrAiandGatewayUnavailable
	}
	installationID := installation.ID
	credential, err := s.aiandCredentials.GetByInstallation(ctx, installationID)
	if err != nil {
		if errors.Is(err, ErrAiandCredentialNotFound) {
			return AiandCredentialStatus{Connected: false}, nil
		}
		return AiandCredentialStatus{}, err
	}
	plaintext, err := s.encryptor.Decrypt(credential.AccessTokenCiphertext, aiandExternalID(installation), providers.ProviderAIAND)
	if err != nil {
		return AiandCredentialStatus{}, fmt.Errorf("open aiand access token: %w", err)
	}
	expiresAt := credential.ExpiresAt
	return AiandCredentialStatus{
		Connected: true,
		Email:     credential.UserEmail,
		OrgName:   credential.OrgName,
		KeyMask:   MaskAiandKey(string(plaintext)),
		ExpiresAt: &expiresAt,
		Origin:    credential.Origin,
	}, nil
}

// DisconnectAiandCredential removes the installation's Aiand connection. For
// device-origin credentials it first tries to revoke the refresh token
// upstream (best-effort: a revocation failure is logged, not fatal, because
// the local row is going away regardless). The propagated aiand provider key
// is deleted with it.
func (s *Service) DisconnectAiandCredential(ctx context.Context, installation *Installation) error {
	if installation == nil || s.aiandCredentials == nil {
		return ErrAiandGatewayUnavailable
	}
	installationID := installation.ID
	credential, err := s.aiandCredentials.GetByInstallation(ctx, installationID)
	if err != nil && !errors.Is(err, ErrAiandCredentialNotFound) {
		return err
	}
	if credential != nil && credential.Origin == AiandOriginDevice && len(credential.RefreshTokenCiphertext) > 0 {
		externalID := aiandExternalID(installation)
		refresh, decErr := s.encryptor.Decrypt(credential.RefreshTokenCiphertext, externalID, providers.ProviderAIAND)
		if decErr != nil {
			observability.FromContext(ctx).Warn("aiand: could not unseal token for revocation", "err", decErr)
		} else if revErr := s.aiandGateway.Revoke(ctx, string(refresh)); revErr != nil {
			observability.FromContext(ctx).Warn("aiand: gateway revocation failed; removing local credential anyway", "err", revErr)
		}
	}
	if err := s.aiandCredentials.Delete(ctx, installationID); err != nil {
		return err
	}
	if err := s.externalKeys.SoftDeleteByProvider(ctx, installationID, providers.ProviderAIAND); err != nil {
		return err
	}
	// An approval started before this disconnect must not silently reconnect
	// afterward: drop any in-flight device login for the installation.
	s.clearAiandPending(installationID)
	s.invalidateInstallation(installationID)
	return nil
}

// RotateAiandCredentialIfExpiring refreshes a device-origin credential whose
// access token expires within the window. Refresh tokens are single-use, so
// rotation is serialized per installation and the row is re-read after
// acquiring the per-installation lock: a rotation that already completed
// makes the second caller observe a fresh expiry and do nothing.
func (s *Service) RotateAiandCredentialIfExpiring(ctx context.Context, installation *Installation, within time.Duration) (bool, error) {
	if installation == nil || s.aiandCredentials == nil {
		return false, ErrAiandGatewayUnavailable
	}
	if within <= 0 {
		within = aiandRotateWindow
	}
	defer s.lockAiandRotation(installation.ID)()

	credential, err := s.aiandCredentials.GetByInstallation(ctx, installation.ID)
	if err != nil {
		if errors.Is(err, ErrAiandCredentialNotFound) {
			return false, nil
		}
		return false, err
	}
	if credential.Origin != AiandOriginDevice || len(credential.RefreshTokenCiphertext) == 0 {
		return false, nil
	}
	if credential.ExpiresAt.After(s.now().Add(within)) {
		return false, nil
	}
	externalID := aiandExternalID(installation)
	refresh, err := s.encryptor.Decrypt(credential.RefreshTokenCiphertext, externalID, providers.ProviderAIAND)
	if err != nil {
		return false, fmt.Errorf("unseal aiand refresh token: %w", err)
	}
	token, err := s.aiandGateway.RefreshToken(ctx, string(refresh))
	if err != nil {
		return false, aiandGatewayError(err)
	}
	if _, err := s.completeAiandCredential(ctx, installation, externalID, token, AiandOriginDevice, credential.UserEmail); err != nil {
		return false, err
	}
	return true, nil
}

// SweepExpiringAiandCredentials rotates every installation credential whose
// access token expires within the window. It is the hourly background entry
// point: RotateAiandCredentialIfExpiring re-checks the window, keeps the
// per-installation single-flight, and skips paste-origin rows, so the sweep
// only enumerates candidates and counts successes. A failed installation is
// logged and skipped — one broken credential must not starve the rest — and
// the returned count covers only successful rotations. The error is the list
// failure; per-row failures never abort the sweep.
func (s *Service) SweepExpiringAiandCredentials(ctx context.Context, within time.Duration) (int, error) {
	if s.aiandCredentials == nil {
		return 0, ErrAiandGatewayUnavailable
	}
	expiring, err := s.aiandCredentials.ListExpiring(ctx, s.now().Add(within))
	if err != nil {
		return 0, fmt.Errorf("list expiring aiand credentials: %w", err)
	}
	log := observability.FromContext(ctx)
	rotated := 0
	for _, row := range expiring {
		installation := &Installation{
			ID:         row.Credential.InstallationID,
			ExternalID: row.InstallationExternalID,
		}
		did, err := s.RotateAiandCredentialIfExpiring(ctx, installation, within)
		if err != nil {
			log.Error("aiand: credential rotation failed", "installation_id", installation.ID, "err", err)
			continue
		}
		if did {
			rotated++
		}
	}
	return rotated, nil
}

// lockAiandRotation returns an unlock func for the installation's rotation
// mutex, creating it on first use.
func (s *Service) lockAiandRotation(installationID string) func() {
	s.aiandRotationMu.Lock()
	if s.aiandRotationLocks == nil {
		s.aiandRotationLocks = map[string]*sync.Mutex{}
	}
	lock, ok := s.aiandRotationLocks[installationID]
	if !ok {
		lock = &sync.Mutex{}
		s.aiandRotationLocks[installationID] = lock
	}
	s.aiandRotationMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// aiandExternalID prefers the installation's external ID, which is the AAD
// identity every other sealed secret in this package binds to.
func aiandExternalID(installation *Installation) string {
	if installation.ExternalID != "" {
		return installation.ExternalID
	}
	return installation.ID
}

// aiandGatewayError funnels gateway transport/HTTP failures into the
// ErrAiandGatewayUnavailable sentinel handlers map to 502.
func aiandGatewayError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAiandGatewayUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrAiandGatewayUnavailable, err)
}
