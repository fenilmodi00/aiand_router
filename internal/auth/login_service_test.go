package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/flags"
	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

// --- fakes ---------------------------------------------------------------

// loginFakeGateway scripts gateway responses for the login service tests.
type loginFakeGateway struct {
	mu sync.Mutex

	startCode auth.AiandDeviceCode
	startErr  error
	starts    []string

	pollErr   error
	pollToken auth.AiandTokenResponse
	polls     []string

	validateUser   auth.AiandUser
	validateErr    error
	validated      []string
	verifyIdentity auth.AiandIdentity
	verifyErr      error
	verified       []string

	authorizeParams []auth.AiandAuthorizeParams
	authorizeURLs   []string
	preflighted     []string
	preflightErr    error
	exchanged       []auth.AiandCodeExchangeParams
	exchangeToken   auth.AiandTokenResponse
	exchangeErr     error
}

func (f *loginFakeGateway) DeviceCodeStart(_ context.Context, keyName string) (auth.AiandDeviceCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, keyName)
	return f.startCode, f.startErr
}

func (f *loginFakeGateway) PollToken(_ context.Context, deviceCode string) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls = append(f.polls, deviceCode)
	return f.pollToken, f.pollErr
}

func (f *loginFakeGateway) AuthorizeURL(params auth.AiandAuthorizeParams) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorizeParams = append(f.authorizeParams, params)
	return "https://auth.example/authorize?state=" + params.State + "&redirect_uri=" + params.RedirectURI
}

func (f *loginFakeGateway) BrowserAuthorizeAvailable(_ context.Context, authorizeURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflighted = append(f.preflighted, authorizeURL)
	return f.preflightErr
}

func (f *loginFakeGateway) ExchangeCode(_ context.Context, params auth.AiandCodeExchangeParams) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanged = append(f.exchanged, params)
	return f.pollToken, f.exchangeErr
}

func (f *loginFakeGateway) RefreshToken(_ context.Context, refreshToken string) (auth.AiandTokenResponse, error) {
	return auth.AiandTokenResponse{}, errors.New("not used")
}

func (f *loginFakeGateway) Revoke(_ context.Context, refreshToken string) error {
	return errors.New("not used")
}

func (f *loginFakeGateway) ValidateKey(_ context.Context, key string) (auth.AiandUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validated = append(f.validated, key)
	return f.validateUser, f.validateErr
}

// VerifyKey is the paste-login identity seam. An explicit verifyIdentity wins;
// otherwise the ValidateKey script is reused with the fixture org so tests
// that only care about the user keep working.
func (f *loginFakeGateway) VerifyKey(_ context.Context, key string) (auth.AiandIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified = append(f.verified, key)
	if f.verifyIdentity.UserID != "" || f.verifyErr != nil {
		return f.verifyIdentity, f.verifyErr
	}
	if f.validateErr != nil {
		return auth.AiandIdentity{}, f.validateErr
	}
	return auth.AiandIdentity{UserID: f.validateUser.ID, OrgID: "org-1", Email: f.validateUser.Email}, nil
}

// loginFakeAccounts is an in-memory auth.AccountRepository mirroring the SQL
// adapter's UpsertAccountOnLogin: a fresh insert returns the given
// InstallationID (what the new row persisted); when the identity pair already
// exists (concurrent-login arbitration), the EXISTING row — and its original
// installation — wins, exactly like the SQL 23505 → re-read.
type loginFakeAccounts struct {
	mu         sync.Mutex
	byID       map[string]*auth.Account
	upserts    []auth.UpsertAccountOnLoginParams
	convergeTo *auth.Account
}

func newLoginFakeAccounts() *loginFakeAccounts {
	return &loginFakeAccounts{byID: map[string]*auth.Account{}}
}

func (f *loginFakeAccounts) UpsertAccountOnLogin(_ context.Context, params auth.UpsertAccountOnLoginParams) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, params)
	// Identity-pair arbitration (mirrors the postgres adapter's unique-index
	// loser re-reading the winner): an existing row for the same (user, org)
	// identity wins over the params' fresh installation pointer.
	if f.convergeTo != nil {
		// Scripted convergence takes precedence (explicit test wiring).
		out := f.convergeTo
		f.convergeTo = nil
		return out, nil
	}
	for _, row := range f.byID {
		if row.AiandUserID == params.AiandUserID && row.AiandOrgID == params.AiandOrgID {
			return row, nil
		}
	}
	id := params.ID
	if id == "" {
		id = "acct-fallback"
	}
	row := &auth.Account{
		ID:             id,
		AiandUserID:    params.AiandUserID,
		AiandOrgID:     params.AiandOrgID,
		Email:          params.Email,
		InstallationID: params.InstallationID,
	}
	f.byID[id] = row
	return row, nil
}

// LastUpsertInstallationID reports the installation id the most recent upsert
// carried (what a real account row would persist as installation_id).
func (f *loginFakeAccounts) LastUpsertInstallationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.upserts) == 0 {
		return ""
	}
	return f.upserts[len(f.upserts)-1].InstallationID
}

func (f *loginFakeAccounts) GetAccountByIdentity(_ context.Context, aiandUserID, aiandOrgID string) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.byID {
		if row.AiandUserID == aiandUserID && row.AiandOrgID == aiandOrgID {
			return row, nil
		}
	}
	return nil, auth.ErrAccountNotFound
}

func (f *loginFakeAccounts) GetAccountByID(_ context.Context, id string) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.byID[id]; ok {
		return row, nil
	}
	return nil, auth.ErrAccountNotFound
}

func (f *loginFakeAccounts) SoftDeleteAccount(_ context.Context, id string) error { return nil }

// loginFakeSessions is an in-memory auth.SessionRepository.
type loginFakeSessions struct {
	mu        sync.Mutex
	byHash    map[string]*auth.ActiveSession
	revoked   map[string]bool
	inserted  []auth.InsertSessionParams
	revokeErr error
}

func newLoginFakeSessions() *loginFakeSessions {
	return &loginFakeSessions{byHash: map[string]*auth.ActiveSession{}, revoked: map[string]bool{}}
}

func (f *loginFakeSessions) InsertAccountSession(_ context.Context, params auth.InsertSessionParams) (*auth.LoginSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, params)
	row := &auth.LoginSession{
		ID:          "sess-" + params.TokenHash[:8],
		AccountID:   params.AccountID,
		TokenHash:   params.TokenHash,
		TokenPrefix: params.TokenPrefix,
		ExpiresAt:   params.ExpiresAt,
	}
	f.byHash[params.TokenHash] = &auth.ActiveSession{Session: *row}
	return row, nil
}

func (f *loginFakeSessions) GetActiveSessionByTokenHash(_ context.Context, params auth.GetActiveSessionParams) (*auth.ActiveSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked[params.TokenHash] {
		return nil, auth.ErrSessionNotFound
	}
	if row, ok := f.byHash[params.TokenHash]; ok {
		return row, nil
	}
	return nil, auth.ErrSessionNotFound
}

func (f *loginFakeSessions) TouchSessionLastSeen(_ context.Context, params auth.TouchSessionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byHash[params.TokenHash]; !ok {
		return auth.ErrSessionNotFound
	}
	return nil
}

func (f *loginFakeSessions) RevokeSessionByTokenHash(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked[tokenHash] = true
	return nil
}

func (f *loginFakeSessions) RevokeAllSessionsForAccount(_ context.Context, accountID string) (int64, error) {
	return 0, errors.New("not used")
}

// loginFakeAttempts is an in-memory auth.LoginAttemptRepository. now is the
// fixture's clock so expiry follows the pinned service time.
type loginFakeAttempts struct {
	mu       sync.Mutex
	now      func() time.Time
	rows     map[string]*auth.LoginAttempt
	consumed map[string]bool
	deleted  map[string]bool
	inserted []auth.InsertLoginAttemptParams
}

func newLoginFakeAttempts() *loginFakeAttempts {
	return &loginFakeAttempts{
		now:      func() time.Time { return time.Unix(0, 0) },
		rows:     map[string]*auth.LoginAttempt{},
		consumed: map[string]bool{},
		deleted:  map[string]bool{},
	}
}

func (f *loginFakeAttempts) InsertLoginAttempt(_ context.Context, params auth.InsertLoginAttemptParams) (*auth.LoginAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, params)
	row := &auth.LoginAttempt{
		ID:         params.ID,
		DeviceCode: params.DeviceCodeCiphertext,
		Interval:   params.Interval,
		ExpiresAt:  params.ExpiresAt,
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *loginFakeAttempts) GetActiveLoginAttempt(_ context.Context, id string) (*auth.LoginAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || f.consumed[id] || f.deleted[id] || !row.ExpiresAt.After(f.now()) {
		return nil, auth.ErrLoginAttemptNotFound
	}
	return row, nil
}

func (f *loginFakeAttempts) MarkLoginAttemptConsumed(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return auth.ErrLoginAttemptNotFound
	}
	f.consumed[id] = true
	return nil
}

func (f *loginFakeAttempts) DeleteLoginAttempt(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[id] = true
	return nil
}

func (f *loginFakeAttempts) DeleteExpiredLoginAttempts(_ context.Context, now time.Time) (int64, error) {
	return 0, errors.New("not used")
}

// loginFakeInstallations is an in-memory auth.InstallationRepository limited
// to what the login service exercises. Created rows get UUID-shaped ids
// (the production adapter parses them), so tests can assert the credential
// FK carries the installation UUID, never the acct_ external id.
type loginFakeInstallations struct {
	mu         sync.Mutex
	byExternal map[string][]*auth.Installation
	created    []auth.CreateInstallationParams
	createErr  error
}

func newLoginFakeInstallations() *loginFakeInstallations {
	return &loginFakeInstallations{byExternal: map[string][]*auth.Installation{}}
}

func (f *loginFakeInstallations) Create(_ context.Context, params auth.CreateInstallationParams) (*auth.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, params)
	row := &auth.Installation{
		ID:         fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.created)),
		ExternalID: params.ExternalID,
		Name:       params.Name,
	}
	f.byExternal[params.ExternalID] = append(f.byExternal[params.ExternalID], row)
	return row, nil
}

func (f *loginFakeInstallations) ListForExternalID(_ context.Context, externalID string) ([]*auth.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]*auth.Installation(nil), f.byExternal[externalID]...)
	return out, nil
}

func (f *loginFakeInstallations) SoftDelete(_ context.Context, externalID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.byExternal[externalID]
	for _, row := range rows {
		if row != nil && row.ID == id {
			now := time.Unix(0, 0)
			row.DeletedAt = &now
			return nil
		}
	}
	return nil
}
func (f *loginFakeInstallations) MarkFirstRequestServed(_ context.Context, id string) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateExcludedModels(_ context.Context, externalID, id string, models []string) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateAllowedModels(_ context.Context, externalID, id string, models []string) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateRoutingPreference(_ context.Context, externalID, id string, weight *float64) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateContentCaptureMode(_ context.Context, externalID, id string, mode *string) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateHideTerminalSurfaces(_ context.Context, externalID, id string, hide bool) error {
	return errors.New("not used")
}
func (f *loginFakeInstallations) UpdateFlagOverrides(_ context.Context, externalID, id string, overrides flags.Overrides) error {
	return errors.New("not used")
}

// loginRecordingExternalKeys records the tx-scoped external key creates.
type loginRecordingExternalKeys struct {
	mu          sync.Mutex
	created     []auth.CreateExternalAPIKeyParams
	softDeleted []string
	// active mirrors the partial unique index (installation, provider) WHERE
	// deleted_at IS NULL, so a second login that forgets to retire the prior
	// key fails here exactly as Postgres would.
	active map[string]bool
}

func (f *loginRecordingExternalKeys) Create(_ context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == nil {
		f.active = map[string]bool{}
	}
	slot := params.InstallationID + "|" + params.Provider
	if f.active[slot] {
		return nil, errors.New("duplicate active external key")
	}
	f.active[slot] = true
	f.created = append(f.created, params)
	return &auth.ExternalAPIKey{ID: "ek-1", InstallationID: params.InstallationID, Provider: params.Provider}, nil
}

func (f *loginRecordingExternalKeys) GetForInstallation(_ context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}
func (f *loginRecordingExternalKeys) SoftDeleteByProvider(_ context.Context, installationID, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active != nil {
		f.active[installationID+"|"+provider] = false
	}
	f.softDeleted = append(f.softDeleted, installationID+"|"+provider)
	return nil
}

func (f *loginRecordingExternalKeys) activeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, active := range f.active {
		if active {
			n++
		}
	}
	return n
}
func (f *loginRecordingExternalKeys) SoftDelete(_ context.Context, installationID, id string) error {
	return nil
}
func (f *loginRecordingExternalKeys) UpdateModelAliases(_ context.Context, installationID, id string, aliases map[string]string) (*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}
func (f *loginRecordingExternalKeys) MarkUsed(_ context.Context, id string) error { return nil }

// loginRecordingCredentials records the tx-scoped credential upserts.
type loginRecordingCredentials struct {
	mu      sync.Mutex
	upserts []auth.UpsertAiandCredentialParams
}

func (f *loginRecordingCredentials) Upsert(_ context.Context, params auth.UpsertAiandCredentialParams) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, params)
	return &auth.AiandCredential{ID: "cred-1", InstallationID: params.InstallationID}, nil
}

func (f *loginRecordingCredentials) GetByInstallation(_ context.Context, installationID string) (*auth.AiandCredential, error) {
	return nil, auth.ErrAiandCredentialNotFound
}
func (f *loginRecordingCredentials) Delete(_ context.Context, installationID string) error {
	return nil
}
func (f *loginRecordingCredentials) ListExpiring(_ context.Context, before time.Time) ([]auth.AiandExpiringCredential, error) {
	return nil, nil
}

// loginRecordingNotifier records exactly-once invalidation events.
type loginRecordingNotifier struct {
	mu      sync.Mutex
	called  []string
	notifyF func(installationID string) // optional hook to fail the tx from the notifier
}

func (f *loginRecordingNotifier) NotifyInstallationChanged(installationID string) {
	f.mu.Lock()
	f.called = append(f.called, installationID)
	hook := f.notifyF
	f.mu.Unlock()
	if hook != nil {
		hook(installationID)
	}
}

// loginTxRunner records which Tx set each closure ran with and can fail runs.
type loginTxRunner struct {
	mu       sync.Mutex
	runs     int
	failRun  int // 1-based; 0 never fails
	failWith error
	tx       auth.Tx
	inTxs    []auth.Tx
}

func (f *loginTxRunner) InTx(_ context.Context, fn func(auth.Tx) error) error {
	f.mu.Lock()
	f.runs++
	fail := f.failRun > 0 && f.runs == f.failRun
	f.inTxs = append(f.inTxs, f.tx)
	tx := f.tx
	f.mu.Unlock()
	if fail {
		return f.failWith
	}
	return fn(tx)
}

// --- harness --------------------------------------------------------------

type loginFixture struct {
	gateway       *loginFakeGateway
	accounts      *loginFakeAccounts
	sessions      *loginFakeSessions
	attempts      *loginFakeAttempts
	installations *loginFakeInstallations
	credentials   *loginRecordingCredentials
	externalKeys  *loginRecordingExternalKeys
	runner        *loginTxRunner
	notifier      *loginRecordingNotifier
	nowFn         func() time.Time // overridable pinned clock
	svc           *auth.LoginService
}

// makeLoginService builds a LoginService over fakes. NoOp encryptor keeps
// ciphertexts equal to plaintext so assertions read them directly.
func makeLoginService(t *testing.T, mutate func(*loginFixture)) *loginFixture {
	t.Helper()
	f := &loginFixture{
		gateway:       &loginFakeGateway{},
		accounts:      newLoginFakeAccounts(),
		sessions:      newLoginFakeSessions(),
		attempts:      newLoginFakeAttempts(),
		installations: newLoginFakeInstallations(),
		credentials:   &loginRecordingCredentials{},
		externalKeys:  &loginRecordingExternalKeys{},
		notifier:      &loginRecordingNotifier{},
	}
	f.runner = &loginTxRunner{tx: auth.Tx{
		ExternalAPIKeys:  f.externalKeys,
		AiandCredentials: f.credentials,
		Accounts:         f.accounts,
		Sessions:         f.sessions,
		LoginAttempts:    f.attempts,
		Installations:    f.installations,
	}}
	if mutate != nil {
		mutate(f)
	}
	if f.nowFn == nil {
		f.nowFn = func() time.Time { return time.Unix(0, 0) }
	}
	if f.svc == nil {
		svc, err := auth.NewLoginService(
			f.accounts, f.sessions, f.attempts, f.installations,
			f.credentials, f.externalKeys, f.gateway,
			auth.NoOpEncryptor{}, f.runner, f.nowFn,
			f.notifier,
		)
		require.NoError(t, err)
		f.svc = svc
	}
	return f
}

// authorizedGateway scripts the happy path: a started device code, a pending
// validate, and an authorized token response.
func authorizedGateway(f *loginFixture) {
	f.gateway.startCode = auth.AiandDeviceCode{
		DeviceCode:              "dc-secret-value-xyz",
		UserCode:                "ABCD-1234",
		VerificationURI:         "https://aiand.test/device",
		VerificationURIComplete: "https://aiand.test/device?code=ABCD-1234",
		ExpiresIn:               600,
		Interval:                5,
	}
	f.gateway.validateUser = auth.AiandUser{ID: "user-1", Email: "ops@example.com"}
	f.gateway.pollToken = auth.AiandTokenResponse{
		AccessToken:  "sk-aiand-topsecret",
		RefreshToken: "rt-secret-value",
		ExpiresIn:    3600,
		Org:          &auth.AiandOrg{ID: "org-1", Name: "Example Org"},
	}
}

// startDevice runs the start step and returns the dashboard-facing start.
func startDevice(t *testing.T, f *loginFixture) auth.LoginDeviceStart {
	t.Helper()
	start, err := f.svc.StartDeviceLogin(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, start.LoginID)
	return start
}

// pollAuthorized polls the started attempt through to the authorized outcome.
func pollAuthorized(t *testing.T, f *loginFixture, loginID string) auth.LoginPollOutcome {
	t.Helper()
	outcome, err := f.svc.PollDeviceLogin(context.Background(), loginID)
	require.NoError(t, err)
	require.Equal(t, auth.AiandPollStatusAuthorized, outcome.Status)
	require.NotNil(t, outcome.Session)
	return outcome
}

// deviceTestIdentity is the gateway identity the fakes resolve.
var deviceTestIdentity = auth.AiandIdentity{UserID: "user-1", OrgID: "org-1", Email: "ops@example.com", OrgName: "Example Org"}

// --- tests ----------------------------------------------------------------

// TestStartDeviceLogin_SealsAttemptWithAttemptIDDerivedAAD drives the start
// path: the gateway receives the host-labeled key name, the attempt row is
// sealed with AAD (attempt id, aiand-device), and only the user-facing fields
// reach the caller.
func TestStartDeviceLogin_SealsAttemptWithAttemptIDDerivedAAD(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	start := startDevice(t, f)

	require.Len(t, f.gateway.starts, 1)
	assert.Contains(t, f.gateway.starts[0], "aiand-router@",
		"gateway key label must carry the aiand-router@<hostname> shape")
	assert.NotEqual(t, "aiand-router@", f.gateway.starts[0], "hostname must not be empty")

	require.Len(t, f.attempts.inserted, 1)
	inserted := f.attempts.inserted[0]
	assert.Equal(t, start.LoginID, inserted.ID, "attempt id must pin the row's uuid — it is the AAD identity")
	assert.Equal(t, "dc-secret-value-xyz", string(inserted.DeviceCodeCiphertext),
		"NoOp encryptor keeps ciphertext equal to plaintext; the seal itself is exercised by the real Encryptor's AAD contract")
	assert.Equal(t, 5*time.Second, inserted.Interval)
	assert.Equal(t, 600, start.ExpiresIn)
	assert.Equal(t, 5, start.Interval)
	assert.Equal(t, "ABCD-1234", start.UserCode)
	assert.Equal(t, "https://aiand.test/device", start.VerificationURI)
	assert.Equal(t, "https://aiand.test/device?code=ABCD-1234", start.VerificationURIComplete)
	assert.NotContains(t, start.LoginID, "dc-secret", "the login id must not embed device code material")
}

// TestStartDeviceLogin_DefaultsWhenGatewayOmits covers the 5s/15m fallbacks
// StartAiandDeviceLogin established.
func TestStartDeviceLogin_DefaultsWhenGatewayOmits(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		f.gateway.startCode = auth.AiandDeviceCode{DeviceCode: "dc-1", UserCode: "U-1"}
	})
	start := startDevice(t, f)
	assert.Equal(t, 5, start.Interval)
	assert.Equal(t, int((15 * time.Minute).Seconds()), start.ExpiresIn)
	require.Len(t, f.attempts.inserted, 1)
	assert.Equal(t, 15*time.Minute, f.attempts.inserted[0].ExpiresAt.Sub(time.Unix(0, 0)))
}

// TestPollDeviceLogin_HappyPathCreatesAllArtifactsInOneTx asserts the whole
// authorized poll runs as one transaction and produces every login artifact.
func TestPollDeviceLogin_HappyPathCreatesAllArtifactsInOneTx(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	start := startDevice(t, f)
	outcome := pollAuthorized(t, f, start.LoginID)

	// One transaction, carrying the full repo set.
	require.Equal(t, 1, f.runner.runs)
	require.Len(t, f.runner.inTxs, 1)
	tx := f.runner.inTxs[0]
	require.NotNil(t, tx.Accounts)
	require.NotNil(t, tx.Installations)
	require.NotNil(t, tx.AiandCredentials)
	require.NotNil(t, tx.ExternalAPIKeys)
	require.NotNil(t, tx.LoginAttempts)
	require.NotNil(t, tx.Sessions)

	// Account: keyed on the gateway identity pair, pinned to the account id.
	require.Len(t, f.accounts.upserts, 1)
	up := f.accounts.upserts[0]
	assert.Equal(t, "user-1", up.AiandUserID)
	assert.Equal(t, "org-1", up.AiandOrgID)
	assert.Equal(t, "ops@example.com", up.Email)
	require.NotEmpty(t, up.ID, "service mints the account id so the installation external_id is deterministic")

	// Installation: exactly one, external_id = account id, name = account id.
	require.Len(t, f.installations.created, 1)
	inst := f.installations.created[0]
	assert.Equal(t, up.ID, inst.ExternalID)
	assert.Equal(t, up.ID, inst.Name)

	// Credential: device origin, FK = the installation UUID the account
	// upsert persisted, sealed with AAD external_id = account id,
	// empty-slice refresh normalization NOT applied (refresh token present).
	require.Len(t, f.credentials.upserts, 1)
	cred := f.credentials.upserts[0]
	assert.Equal(t, up.InstallationID, cred.InstallationID,
		"credential FK must carry the installation UUID, never the acct_ external id")
	assert.NotEqual(t, up.ID, cred.InstallationID,
		"the account id is the AAD identity, not the installation FK")
	assert.Equal(t, "sk-aiand-topsecret", string(cred.AccessTokenCiphertext))
	assert.Equal(t, "rt-secret-value", string(cred.RefreshTokenCiphertext))
	assert.Equal(t, auth.AiandOriginDevice, cred.Origin)
	assert.Equal(t, "org-1", cred.OrgID)
	assert.Equal(t, "Example Org", cred.OrgName)
	wantExpiry := time.Unix(0, 0).Add(3600 * time.Second)
	assert.Equal(t, wantExpiry, cred.ExpiresAt)

	// External key: the access token propagates as the aiand provider key,
	// against the same installation UUID.
	require.Len(t, f.externalKeys.created, 1)
	key := f.externalKeys.created[0]
	assert.Equal(t, providers.ProviderAIAND, key.Provider)
	assert.Equal(t, up.InstallationID, key.InstallationID,
		"external key FK must carry the installation UUID")
	assert.Equal(t, "sk-aiand-topsecret", string(key.KeyCiphertext))

	// Session: 32-byte crypto/rand token, hash + display prefix stored.
	require.Len(t, f.sessions.inserted, 1)
	sess := f.sessions.inserted[0]
	assert.Equal(t, up.ID, sess.AccountID)
	raw, err := hex.DecodeString(outcome.Session.Token)
	require.NoError(t, err, "session token must be hex-decodable (32 raw bytes)")
	assert.Len(t, raw, 32)
	assert.Equal(t, hashToken(outcome.Session.Token), sess.TokenHash)
	assert.Equal(t, outcome.Session.Token[:8], sess.TokenPrefix)
	assert.Equal(t, 7*24*time.Hour, sess.ExpiresAt.Sub(time.Unix(0, 0)))

	// Attempt consumed inside the committed transaction.
	assert.True(t, f.attempts.consumed[start.LoginID], "attempt must be consumed once the tx succeeded")

	// Caller-visible grant.
	assert.Equal(t, outcome.Session.Token, outcome.Session.Token)
	assert.Equal(t, wantExpiry, *outcome.ExpiresAt)
	assert.Equal(t, "ops@example.com", outcome.Email)
	assert.Equal(t, "Example Org", outcome.OrgName)
	assert.Equal(t, "user-1", outcome.Identity.UserID)
}

// TestPollDeviceLogin_MissingTokenOrgResolvesFromKey covers gateways whose
// device token response omits the optional embedded org: the login must still
// land on the key-scoped org instead of the zero value.
func TestPollDeviceLogin_MissingTokenOrgResolvesFromKey(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		authorizedGateway(f)
		f.gateway.pollToken.Org = nil
		f.gateway.verifyIdentity = auth.AiandIdentity{
			UserID: "user-1", OrgID: "org-verified", Email: "ops@example.com", OrgName: "Verified Org",
		}
	})
	start := startDevice(t, f)
	pollAuthorized(t, f, start.LoginID)

	require.Len(t, f.accounts.upserts, 1)
	assert.Equal(t, "org-verified", f.accounts.upserts[0].AiandOrgID)
	require.Len(t, f.credentials.upserts, 1)
	assert.Equal(t, "org-verified", f.credentials.upserts[0].OrgID)
}

// TestLoginWithKey_SecondLoginReplacesExternalKey covers logout/login and
// reconnect: the second login for one account must retire the prior
// (installation, provider) key before creating the new one, or the partial
// unique index aborts the transaction.
func TestLoginWithKey_SecondLoginReplacesExternalKey(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		f.gateway.verifyIdentity = auth.AiandIdentity{UserID: "user-relogin", OrgID: "org-relogin", Email: "relogin@example.com"}
	})
	_, err := f.svc.LoginWithKey(context.Background(), "sk-first")
	require.NoError(t, err)
	_, err = f.svc.LoginWithKey(context.Background(), "sk-second")
	require.NoError(t, err)

	require.Len(t, f.externalKeys.created, 2)
	require.NotEmpty(t, f.externalKeys.softDeleted, "the prior active key must be retired")
	assert.Equal(t, 1, f.externalKeys.activeCount(), "exactly one active aiand key per installation")
}

// TestLoginWithKey_PastePathSameTransaction drives the paste path to the same
// five-artifact transaction with a paste-origin credential.
func TestLoginWithKey_PastePathSameTransaction(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		f.gateway.verifyIdentity = auth.AiandIdentity{UserID: "user-2", OrgID: "org-paste", Email: "paster@example.com"}
	})
	grant, err := f.svc.LoginWithKey(context.Background(), "sk-pasted-key")
	require.NoError(t, err)

	require.Equal(t, 1, f.runner.runs)
	require.Len(t, f.accounts.upserts, 1)
	assert.Equal(t, "user-2", f.accounts.upserts[0].AiandUserID)
	assert.Equal(t, "org-paste", f.accounts.upserts[0].AiandOrgID,
		"the pasted key's org must come from the key identity, not an empty value")

	require.Len(t, f.installations.created, 1)
	assert.Equal(t, f.accounts.upserts[0].ID, f.installations.created[0].ExternalID)

	require.Len(t, f.credentials.upserts, 1)
	cred := f.credentials.upserts[0]
	assert.Equal(t, auth.AiandOriginPaste, cred.Origin,
		"a pasted key has no refresh token; nothing is rotatable")
	assert.Equal(t, []byte{}, cred.RefreshTokenCiphertext,
		"empty bytea, never nil: refresh_token_ciphertext is NOT NULL (T1 nil→[]byte{} normalization)")
	assert.Equal(t, "sk-pasted-key", string(cred.AccessTokenCiphertext))

	require.Len(t, f.externalKeys.created, 1)
	assert.Equal(t, "sk-pasted-key", string(f.externalKeys.created[0].KeyCiphertext))

	require.Len(t, f.sessions.inserted, 1)
	assert.Equal(t, hashToken(grant.Token), f.sessions.inserted[0].TokenHash)
	assert.NotEmpty(t, f.notifier.called)
}

// TestLoginWithKey_InvalidKeySurfacesSentinel asserts a gateway-rejected key
// maps to ErrInvalidAiandKey and persists nothing.
func TestLoginWithKey_InvalidKeySurfacesSentinel(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		f.gateway.verifyErr = auth.ErrAiandInvalidKey
	})
	_, err := f.svc.LoginWithKey(context.Background(), "sk-bad")
	assert.ErrorIs(t, err, auth.ErrAiandInvalidKey)
	assert.Equal(t, 0, f.runner.runs, "no transaction may run for an invalid key")
	assert.Empty(t, f.accounts.upserts)
	assert.Empty(t, f.installations.created)
	assert.Empty(t, f.notifier.called)
}

// TestPollDeviceLogin_ConcurrentSameIdentityConverges asserts the
// arbitration contract: the second same-identity login reuses the winner's
// account + installation and both sessions stay valid.
func TestPollDeviceLogin_ConcurrentSameIdentityConverges(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	first := startDevice(t, f)
	firstOutcome := pollAuthorized(t, f, first.LoginID)

	// Second device login for the SAME identity: the accounts fake replays
	// the winner's account on the second upsert (the unique-index loser's
	// re-read); the installations fake returns the winner's row on re-list.
	winnerInstallationID := f.accounts.upserts[0].InstallationID
	winnerAccount := &auth.Account{
		ID:             f.accounts.upserts[0].ID,
		AiandUserID:    "user-1",
		AiandOrgID:     "org-1",
		InstallationID: winnerInstallationID,
	}
	f.accounts.convergeTo = winnerAccount
	f.gateway.startCode = auth.AiandDeviceCode{
		DeviceCode: "dc-second", UserCode: "EFGH-5678", ExpiresIn: 600, Interval: 5,
	}
	second := startDevice(t, f)
	secondOutcome := pollAuthorized(t, f, second.LoginID)

	// Exactly one installation for the account's external id.
	installs := f.installations.byExternal[f.accounts.upserts[0].ID]
	require.Len(t, installs, 1, "concurrent same-identity logins must converge on one installation")

	// The loser's spec installation (its own pre-minted account id) was
	// soft-deleted inside the losing transaction, so no orphan outlives it.
	winnerExternalID := f.accounts.upserts[0].ID
	for externalID, rows := range f.installations.byExternal {
		for _, inst := range rows {
			if externalID != winnerExternalID {
				require.NotNil(t, inst.DeletedAt, "the losing transaction's installation must be retired")
			}
		}
	}

	// Both sessions valid against the shared account.
	for _, grant := range []*auth.LoginSessionGrant{firstOutcome.Session, secondOutcome.Session} {
		active, err := f.svc.VerifySessionToken(grant.Token)
		require.NoError(t, err)
		assert.Equal(t, winnerAccount.ID, active.Session.AccountID)
	}

	// The arbitration loser's credential + external key are written against
	// the WINNER's installation UUID (account.InstallationID), never the
	// soft-deleted local installation — and the AAD stays the shared
	// account id.
	require.Len(t, f.credentials.upserts, 2)
	losingCred := f.credentials.upserts[1]
	assert.Equal(t, winnerInstallationID, losingCred.InstallationID,
		"the losing transaction writes its credential onto the winner's installation UUID")
	require.Len(t, f.externalKeys.created, 2)
	assert.Equal(t, winnerInstallationID, f.externalKeys.created[1].InstallationID)
	// The AAD itself is proven with the real Tink encryptor in
	// TestLogin_CredentialExternalIDIsAccountID (the NoOp encryptor never
	// sees the AAD param).
}

// TestPollDeviceLogin_TxFailureKeepsAttemptPollable asserts the attempt is
// NOT consumed when the transaction fails: the next poll completes the login.
func TestPollDeviceLogin_TxFailureKeepsAttemptPollable(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		authorizedGateway(f)
		f.runner.failRun = 1
		f.runner.failWith = errors.New("db connection reset")
	})
	start := startDevice(t, f)

	_, err := f.svc.PollDeviceLogin(context.Background(), start.LoginID)
	require.Error(t, err)
	assert.False(t, f.attempts.consumed[start.LoginID],
		"a failed transaction must leave the attempt unconsumed")

	// Next poll works and completes.
	f.runner.failRun = 0
	outcome := pollAuthorized(t, f, start.LoginID)
	assert.True(t, f.attempts.consumed[start.LoginID])
	assert.NotEmpty(t, outcome.Session.Token)
}

// TestPollDeviceLogin_TerminalOutcomesClearAttempt covers denied and expired:
// both statuses return cleanly and the attempt row is deleted.
func TestPollDeviceLogin_TerminalOutcomesClearAttempt(t *testing.T) {
	cases := []struct {
		name   string
		pollEr error
		status string
	}{
		{name: "denied", pollEr: auth.ErrAiandAccessDenied, status: auth.AiandPollStatusDenied},
		{name: "expired", pollEr: auth.ErrAiandExpiredToken, status: auth.AiandPollStatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := makeLoginService(t, func(f *loginFixture) {
				authorizedGateway(f)
				f.gateway.pollErr = tc.pollEr
			})
			start := startDevice(t, f)
			outcome, err := f.svc.PollDeviceLogin(context.Background(), start.LoginID)
			require.NoError(t, err)
			assert.Equal(t, tc.status, outcome.Status)
			assert.True(t, f.attempts.deleted[start.LoginID], "terminal outcome must clear the attempt")
			// The attempt no longer resolves.
			_, err = f.svc.PollDeviceLogin(context.Background(), start.LoginID)
			assert.ErrorIs(t, err, auth.ErrLoginAttemptNotFound)
			assert.Equal(t, 0, f.runner.runs, "no login transaction for a terminal outcome")
		})
	}
}

// TestPollDeviceLogin_PendingAndSlowDown asserts the retryable outcomes: both
// return without touching the attempt row or running the transaction, and the
// slow-down error carries the bumped interval.
func TestPollDeviceLogin_PendingAndSlowDown(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		f := makeLoginService(t, func(f *loginFixture) {
			authorizedGateway(f)
			f.gateway.pollErr = auth.ErrAiandAuthorizationPending
		})
		start := startDevice(t, f)
		outcome, err := f.svc.PollDeviceLogin(context.Background(), start.LoginID)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusPending, outcome.Status)
		assert.Nil(t, outcome.Session)
		assert.False(t, f.attempts.consumed[start.LoginID])
		assert.False(t, f.attempts.deleted[start.LoginID])
		assert.Equal(t, 0, f.runner.runs)
	})
	t.Run("slow_down", func(t *testing.T) {
		f := makeLoginService(t, func(f *loginFixture) {
			authorizedGateway(f)
			f.gateway.pollErr = auth.ErrAiandSlowDown
		})
		start := startDevice(t, f)
		_, err := f.svc.PollDeviceLogin(context.Background(), start.LoginID)
		var slow *auth.AiandSlowDownError
		require.ErrorAs(t, err, &slow)
		assert.Equal(t, 10*time.Second, slow.RetryAfter,
			"interval bumps by five seconds over the gateway's advertised 5s")
		assert.False(t, f.attempts.consumed[start.LoginID])
		assert.False(t, f.attempts.deleted[start.LoginID],
			"nothing persists on slow_down: the sealed row stays as inserted")
		assert.Equal(t, 0, f.runner.runs)
	})
}

// TestPollDeviceLogin_UnknownLoginID asserts ErrLoginAttemptNotFound surfaces.
func TestPollDeviceLogin_UnknownLoginID(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	_, err := f.svc.PollDeviceLogin(context.Background(), "00000000-0000-4000-8000-000000000000")
	assert.ErrorIs(t, err, auth.ErrLoginAttemptNotFound)
}

// TestLogout_RevokesAndVerificationFails covers the logout + verify round
// trip and the expired/revoked failure mode.
func TestLogout_RevokesAndVerificationFails(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	start := startDevice(t, f)
	outcome := pollAuthorized(t, f, start.LoginID)
	token := outcome.Session.Token

	active, err := f.svc.VerifySessionToken(token)
	require.NoError(t, err)
	assert.Equal(t, outcome.Session.Account.ID, active.Session.AccountID)

	require.NoError(t, f.svc.Logout(context.Background(), token))
	_, err = f.svc.VerifySessionToken(token)
	assert.ErrorIs(t, err, auth.ErrSessionNotFound, "a revoked session must stop resolving")

	// Idempotent: revoking twice is not an error.
	require.NoError(t, f.svc.Logout(context.Background(), token))

	// Unknown token fails closed.
	_, err = f.svc.VerifySessionToken("no-such-token")
	assert.ErrorIs(t, err, auth.ErrSessionNotFound)
}

// TestVerifySessionToken_ExpiredSessionFails asserts the sessions fake's
// expiry behavior fails verification (the postgres query enforces
// expires_at > now(); the fake models the revoked path and the service maps
// both to ErrSessionNotFound).
func TestVerifySessionToken_ExpiredSessionFails(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	// A hash that never resolved.
	_, err := f.svc.VerifySessionToken("expired-token")
	assert.ErrorIs(t, err, auth.ErrSessionNotFound)
}

// TestLogin_CredentialExternalIDIsAccountID asserts the AAD contract end to
// end: the credential sealed inside the tx binds to the account id, and the
// device-code seal binds to the attempt id — both via the real Encryptor.
func TestLogin_CredentialExternalIDIsAccountID(t *testing.T) {
	// Real AES-GCM encryptor: unsealing under a changed AAD must fail, which
	// is the behavioral proof that the ciphertext was bound to the account id.
	var keysetBuf bytes.Buffer
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&keysetBuf)))
	encryptor, err := auth.NewTinkEncryptor(keysetBuf.String())
	require.NoError(t, err)

	f := makeLoginService(t, func(f *loginFixture) {
		authorizedGateway(f)
		// Rebuild the service with the real encryptor.
		svc, err := auth.NewLoginService(
			f.accounts, f.sessions, f.attempts, f.installations,
			f.credentials, f.externalKeys, f.gateway,
			encryptor, f.runner, func() time.Time { return time.Unix(0, 0) },
			f.notifier,
		)
		require.NoError(t, err)
		f.svc = svc
	})
	start := startDevice(t, f)
	pollAuthorized(t, f, start.LoginID)

	accountID := f.accounts.upserts[0].ID
	installationID := f.accounts.upserts[0].InstallationID
	require.Len(t, f.credentials.upserts, 1)
	cred := f.credentials.upserts[0]
	assert.Equal(t, installationID, cred.InstallationID,
		"credential FK is the installation UUID; the account id is only the AAD identity")

	// Unseal with the account id (the AAD external id): succeeds.
	plaintext, err := encryptor.Decrypt(cred.AccessTokenCiphertext, accountID, providers.ProviderAIAND)
	require.NoError(t, err)
	assert.Equal(t, "sk-aiand-topsecret", string(plaintext))

	// Unseal under the installation UUID: AAD mismatch fails closed — the
	// ciphertext is bound to the account id, not the FK UUID.
	_, err = encryptor.Decrypt(cred.AccessTokenCiphertext, installationID, providers.ProviderAIAND)
	assert.Error(t, err, "the AAD must be the account id, never the installation UUID")

	// Unseal under any other external id: AAD mismatch fails closed.
	_, err = encryptor.Decrypt(cred.AccessTokenCiphertext, "some-other-id", providers.ProviderAIAND)
	assert.Error(t, err, "ciphertext must be bound to the account id via AAD")

	// Device code seal: bound to the attempt id, not the account id.
	require.Len(t, f.attempts.inserted, 1)
	attempt := f.attempts.inserted[0]
	dc, err := encryptor.Decrypt(attempt.DeviceCodeCiphertext, attempt.ID, "aiand-device")
	require.NoError(t, err)
	assert.Equal(t, "dc-secret-value-xyz", string(dc))
	_, err = encryptor.Decrypt(attempt.DeviceCodeCiphertext, attempt.ID, providers.ProviderAIAND)
	assert.Error(t, err, "the device-code purpose label must be part of the AAD")
}

// TestLogin_NotifierExactlyOnceWithInstallationID asserts the invalidation
// fires exactly once after the commit, with the installation UUID — the
// identity every cache/routing listener keys on (matching the legacy aiand
// flow's invalidateInstallation(installation.ID)).
func TestLogin_NotifierExactlyOnceWithInstallationID(t *testing.T) {
	f := makeLoginService(t, authorizedGateway)
	start := startDevice(t, f)
	pollAuthorized(t, f, start.LoginID)

	require.Len(t, f.notifier.called, 1, "exactly one invalidation per committed login")
	assert.Equal(t, f.accounts.upserts[0].InstallationID, f.notifier.called[0],
		"the invalidation names the installation UUID, never the acct_ external id")

	// Paste path: same exactly-once contract.
	f2 := makeLoginService(t, func(f *loginFixture) {
		f.gateway.validateUser = auth.AiandUser{ID: "user-3", Email: "x@y.z"}
	})
	_, err := f2.svc.LoginWithKey(context.Background(), "sk-key")
	require.NoError(t, err)
	require.Len(t, f2.notifier.called, 1)
}

// TestPollDeviceLogin_NoTxRunWhenTxFailedForNotifierGuard is a belt-and-braces
// check that the notifier hook is not invoked from inside a failed tx.
func TestPollDeviceLogin_NotifierOnlyAfterCommit(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		authorizedGateway(f)
		f.runner.failRun = 1
		f.runner.failWith = errors.New("db connection reset")
	})
	start := startDevice(t, f)
	_, err := f.svc.PollDeviceLogin(context.Background(), start.LoginID)
	require.Error(t, err)
	assert.Empty(t, f.notifier.called, "a failed transaction must not fire the notifier")

	f.runner.failRun = 0
	_, err = f.svc.PollDeviceLogin(context.Background(), start.LoginID)
	require.NoError(t, err)
	require.Len(t, f.notifier.called, 1)
}

// captureLineHandler records each emitted log line whole.
type captureLineHandler struct {
	mu    sync.Mutex
	lines []string
}

func (h *captureLineHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureLineHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=")
		if err, ok := a.Value.Any().(error); ok {
			b.WriteString(err.Error())
		} else {
			b.WriteString(a.Value.String())
		}
		return true
	})
	h.mu.Lock()
	h.lines = append(h.lines, b.String())
	h.mu.Unlock()
	return nil
}

func (h *captureLineHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLineHandler) WithGroup(string) slog.Handler      { return h }

// TestPollDeviceLogin_LogsCarryNoSecrets drives every poll outcome through a
// capture handler and asserts no device code, user code, or token material
// reaches any log line.
func TestPollDeviceLogin_LogsCarryNoSecrets(t *testing.T) {
	type testCase struct {
		name      string
		pollErr   error
		pollToken auth.AiandTokenResponse
	}
	cases := []testCase{
		{name: "pending", pollErr: auth.ErrAiandAuthorizationPending},
		{name: "slow_down", pollErr: auth.ErrAiandSlowDown},
		{name: "denied", pollErr: auth.ErrAiandAccessDenied},
		{name: "expired", pollErr: auth.ErrAiandExpiredToken},
		{name: "authorized", pollToken: auth.AiandTokenResponse{
			AccessToken: "sk-aiand-topsecret", RefreshToken: "rt-secret-value", ExpiresIn: 3600,
			Org: &auth.AiandOrg{ID: "org-1", Name: "Example Org"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &captureLineHandler{}
			logger := slog.New(handler)
			ctx := observability.WithLogger(context.Background(), logger)

			f := makeLoginService(t, func(f *loginFixture) {
				authorizedGateway(f)
				f.gateway.pollErr = tc.pollErr
				f.gateway.pollToken = tc.pollToken
			})
			start := startDevice(t, f)
			_, _ = f.svc.PollDeviceLogin(ctx, start.LoginID)

			require.NotEmpty(t, handler.lines, "every poll outcome must be logged")
			for _, line := range handler.lines {
				for _, secret := range []string{"dc-secret-value-xyz", "dc-second", "ABCD-1234", "EFGH-5678", "sk-aiand-topsecret", "rt-secret-value"} {
					assert.NotContains(t, line, secret,
						"no device code, user code, or token material may reach the logs")
				}
				assert.NotContains(t, line, "device_code")
				assert.NotContains(t, line, "user_code")
				assert.NotContains(t, line, "access_token")
				assert.NotContains(t, line, "refresh_token")
			}
		})
	}
}

// hashToken mirrors the stored hash: SHA-256 hex of the raw token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// --- browser flow ---------------------------------------------------------

// browserTestToken is the exchange response the browser tests script: an
// org-bearing token so resolveIdentity short-circuits past ValidateKey.
func browserTestToken() auth.AiandTokenResponse {
	return auth.AiandTokenResponse{
		AccessToken:  "sk-aiand-browser",
		RefreshToken: "rt-browser",
		ExpiresIn:    3600,
		Org:          &auth.AiandOrg{ID: "org-1", Name: "Example Org"},
	}
}

// startBrowser runs StartBrowserLogin and hands back the start plus the
// unsealed flow payload (the fixture's NoOp encryptor keeps the cookie bytes
// readable) so tests can pin the state and tamper with it.
func startBrowser(t *testing.T, f *loginFixture, origin, next string) (auth.LoginBrowserStart, auth.SealedBrowserLogin) {
	t.Helper()
	start, err := f.svc.StartBrowserLogin(context.Background(), origin, next)
	require.NoError(t, err)
	var flow auth.SealedBrowserLogin
	require.NoError(t, json.Unmarshal(start.Sealed, &flow))
	return start, flow
}

func TestStartBrowserLogin_BuildsPreflightsAndSeals(t *testing.T) {
	f := makeLoginService(t, nil)
	authorizedGateway(f)

	start, flow := startBrowser(t, f, "http://127.0.0.1:3000", "/sessions")
	require.NotEmpty(t, start.AuthorizeURL)
	assert.Equal(t, 600, start.ExpiresIn)

	// The gateway got the origin as redirect URI, the sealed state, an S256
	// challenge, and the host-labeled key name.
	require.Len(t, f.gateway.authorizeParams, 1)
	params := f.gateway.authorizeParams[0]
	assert.Equal(t, "http://127.0.0.1:3000", params.RedirectURI)
	assert.Equal(t, flow.State, params.State)
	assert.NotEmpty(t, params.CodeChallenge)
	assert.NotContains(t, params.CodeChallenge, "=", "S256 challenges are raw base64url")
	assert.True(t, strings.HasPrefix(params.KeyName, "aiand-router@"), "key name must be host-labeled, got %q", params.KeyName)
	// The preflight saw the exact URL the caller was given.
	require.Len(t, f.gateway.preflighted, 1)
	assert.Equal(t, start.AuthorizeURL, f.gateway.preflighted[0])

	// The sealed payload binds verifier/redirect/next and expires within the TTL.
	assert.Equal(t, params.State, flow.State)
	assert.NotEmpty(t, flow.Verifier)
	assert.Equal(t, "http://127.0.0.1:3000", flow.RedirectURI)
	assert.Equal(t, "/sessions", flow.Next)
	assert.Equal(t, time.Unix(600, 0).UTC(), flow.ExpiresAt, "pinned epoch clock + 10-minute TTL")
}

func TestStartBrowserLogin_SanitizesNext(t *testing.T) {
	f := makeLoginService(t, nil)
	authorizedGateway(f)
	cases := []struct{ next, want string }{
		{"", "/dashboard"},
		{"/sessions", "/sessions"},
		{"//evil.co", "/dashboard"},
		{"/\\evil.co", "/dashboard"},
		{"dashboard", "/dashboard"},
		{"/" + strings.Repeat("x", 600), "/dashboard"},
	}
	for _, tc := range cases {
		start, flow := startBrowser(t, f, "http://127.0.0.1:3000", tc.next)
		require.NotEmpty(t, start.AuthorizeURL, tc.next)
		assert.Equal(t, tc.want, flow.Next, "next=%q", tc.next)
	}
}

func TestStartBrowserLogin_RejectsNonBareOrigin(t *testing.T) {
	f := makeLoginService(t, nil)
	for _, origin := range []string{
		"",
		"ftp://127.0.0.1:3000",
		"http://127.0.0.1:3000/ui",
		"http://127.0.0.1:3000/?x=1",
		"not a url",
	} {
		_, err := f.svc.StartBrowserLogin(context.Background(), origin, "")
		require.ErrorIs(t, err, auth.ErrBrowserFlowState, "origin=%q", origin)
	}
	assert.Empty(t, f.gateway.authorizeParams, "no authorize URL is built for a bad origin")
}

func TestStartBrowserLogin_PropagatesUnsupported(t *testing.T) {
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.gateway.preflightErr = auth.ErrAiandBrowserUnsupported
	})
	_, err := f.svc.StartBrowserLogin(context.Background(), "http://127.0.0.1:3000", "")
	require.ErrorIs(t, err, auth.ErrAiandBrowserUnsupported)
}

func TestStartBrowserLogin_PreflightGatewayFailureWrapsUnavailable(t *testing.T) {
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.gateway.preflightErr = errors.New("connection refused")
	})
	_, err := f.svc.StartBrowserLogin(context.Background(), "http://127.0.0.1:3000", "")
	require.ErrorIs(t, err, auth.ErrAiandGatewayUnavailable)
}

func TestExchangeBrowserLogin_HappyPath(t *testing.T) {
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.gateway.pollToken = browserTestToken()
	})
	authorizedGateway(f)
	start, flow := startBrowser(t, f, "http://127.0.0.1:3000", "/keys")

	result, err := f.svc.ExchangeBrowserLogin(context.Background(), flow.State, "auth-code-1", start.Sealed)
	require.NoError(t, err)
	require.NotNil(t, result.Grant)
	assert.Equal(t, "ops@example.com", result.Email)
	assert.Equal(t, "Example Org", result.OrgName)
	assert.Equal(t, "/keys", result.Next)
	assert.NotEmpty(t, result.Grant.Token)

	// The gateway exchange carried the code, the sealed verifier, and the
	// original redirect URI.
	require.Len(t, f.gateway.exchanged, 1)
	params := f.gateway.exchanged[0]
	assert.Equal(t, "auth-code-1", params.Code)
	assert.Equal(t, flow.Verifier, params.CodeVerifier)
	assert.Equal(t, "http://127.0.0.1:3000", params.RedirectURI)

	// The same login transaction ran: one session minted, notify fired.
	require.Len(t, f.sessions.inserted, 1)
	require.Len(t, f.notifier.called, 1)
	// The token landed as the installation's propagated aiand external key.
	require.Len(t, f.externalKeys.created, 1)
}

func TestExchangeBrowserLogin_StateMismatchIsFlowStateError(t *testing.T) {
	f := makeLoginService(t, nil)
	start, _ := startBrowser(t, f, "http://127.0.0.1:3000", "")
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), "attacker-state", "code", start.Sealed)
	require.ErrorIs(t, err, auth.ErrBrowserFlowState)
	assert.Empty(t, f.gateway.exchanged, "a mismatched state never reaches the gateway")
}

func TestExchangeBrowserLogin_ExpiredFlowIsFlowStateError(t *testing.T) {
	// Mutable clock shared with the service: start at the epoch, then jump
	// past the 10-minute TTL before the exchange.
	now := time.Unix(0, 0)
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.nowFn = func() time.Time { return now }
	})
	start, flow := startBrowser(t, f, "http://127.0.0.1:3000", "")
	now = time.Unix(3600, 0)
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), flow.State, "code", start.Sealed)
	require.ErrorIs(t, err, auth.ErrBrowserFlowState)
}

func TestExchangeBrowserLogin_TamperedSealedIsFlowStateError(t *testing.T) {
	f := makeLoginService(t, nil)
	start, _ := startBrowser(t, f, "http://127.0.0.1:3000", "")
	tampered := append([]byte(nil), start.Sealed...)
	tampered[0] ^= 0xFF
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), flowStateOf(t, start.Sealed), "code", tampered)
	require.ErrorIs(t, err, auth.ErrBrowserFlowState)
}

func TestExchangeBrowserLogin_MissingSealedIsFlowStateError(t *testing.T) {
	f := makeLoginService(t, nil)
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), "s", "code", nil)
	require.ErrorIs(t, err, auth.ErrBrowserFlowState)
}

func TestExchangeBrowserLogin_RejectedGrantSurfacesSentinel(t *testing.T) {
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.gateway.exchangeErr = auth.ErrAiandInvalidGrant
	})
	start, flow := startBrowser(t, f, "http://127.0.0.1:3000", "")
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), flow.State, "spent-code", start.Sealed)
	require.ErrorIs(t, err, auth.ErrAiandInvalidGrant)
}

func TestExchangeBrowserLogin_GatewayDownWrapsUnavailable(t *testing.T) {
	f := makeLoginService(t, func(fx *loginFixture) {
		fx.gateway.exchangeErr = errors.New("connection refused")
	})
	start, flow := startBrowser(t, f, "http://127.0.0.1:3000", "")
	_, err := f.svc.ExchangeBrowserLogin(context.Background(), flow.State, "code", start.Sealed)
	require.ErrorIs(t, err, auth.ErrAiandGatewayUnavailable)
}

// flowStateOf unseals the state from a NoOp-sealed blob.
func flowStateOf(t *testing.T, sealed []byte) string {
	t.Helper()
	var flow auth.SealedBrowserLogin
	require.NoError(t, json.Unmarshal(sealed, &flow))
	return flow.State
}
