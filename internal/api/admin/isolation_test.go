package admin_test

// T14 (#124) isolation invariant suite: account A's session cookie must never
// reach account B's data. Every probe drives the FULL middleware stack — the
// real WithAccountSession / WithAccountSessionOrAPIKey over a real
// *auth.LoginService backed by in-memory fakes — and mounts the REAL admin
// handlers the way server.go mounts them (selfhosted block): metrics under
// WithAccountSessionOrAPIKey, everything else under WithAccountSession.
// Accounts A and B have distinct installations (the login model: one
// installation per account, external_id = account id), so any surface that
// serves B's rows to A's cookie is a leak.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/api/admin"
	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/flags"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- shared in-memory stores ----------------------------------------------

// isoAccounts models router.accounts: one row per identity pair, keyed by id.
// UpsertAccountOnLogin is identity-arbitrated like the SQL adapter.
type isoAccounts struct {
	mu   sync.Mutex
	rows map[string]*auth.Account // id → row
}

func newIsoAccounts() *isoAccounts {
	return &isoAccounts{rows: map[string]*auth.Account{}}
}

func (f *isoAccounts) UpsertAccountOnLogin(_ context.Context, params auth.UpsertAccountOnLoginParams) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Arbitrate on the identity pair, like the SQL adapter's 23505 → re-read:
	// a concurrent same-(user, org) login must converge on the existing row
	// regardless of which pre-minted id arrives first.
	for _, row := range f.rows {
		if row.AiandUserID == params.AiandUserID && row.AiandOrgID == params.AiandOrgID {
			return row, nil
		}
	}
	id := params.ID
	if id == "" {
		id = auth.GenerateID(auth.AccountIDPrefix)
	}
	row, ok := f.rows[id]
	if ok {
		// Same id re-upserted: the existing row wins.
		return row, nil
	}
	row = &auth.Account{
		ID:             id,
		AiandUserID:    params.AiandUserID,
		AiandOrgID:     params.AiandOrgID,
		Email:          params.Email,
		InstallationID: params.InstallationID,
	}
	f.rows[id] = row
	return row, nil
}

func (f *isoAccounts) GetAccountByIdentity(_ context.Context, aiandUserID, aiandOrgID string) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.AiandUserID == aiandUserID && row.AiandOrgID == aiandOrgID {
			return row, nil
		}
	}
	return nil, auth.ErrAccountNotFound
}

func (f *isoAccounts) GetAccountByID(_ context.Context, id string) (*auth.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.rows[id]; ok {
		return row, nil
	}
	return nil, auth.ErrAccountNotFound
}

func (f *isoAccounts) SoftDeleteAccount(_ context.Context, id string) error { return nil }

// isoInstallations models model_router_installations. The login service
// creates with external_id = name = account id; the fake assigns
// ID = external_id so every handler's installation-scoped read keys on the
// same string the credential/key rows are written under (the production
// wiring: credential + propagated key are written against the account's
// installation).
type isoInstallations struct {
	mu   sync.Mutex
	rows map[string]*auth.Installation // external_id → row
}

func newIsoInstallations() *isoInstallations {
	return &isoInstallations{rows: map[string]*auth.Installation{}}
}

func (f *isoInstallations) Create(_ context.Context, params auth.CreateInstallationParams) (*auth.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rows := f.byExternalLocked(params.ExternalID); len(rows) > 0 {
		return rows[0], nil // deterministic identity: reuse on conflict
	}
	row := &auth.Installation{ID: params.ExternalID, ExternalID: params.ExternalID, Name: params.Name}
	f.rows[params.ExternalID] = row
	return row, nil
}

func (f *isoInstallations) byExternalLocked(externalID string) []*auth.Installation {
	if row, ok := f.rows[externalID]; ok {
		return []*auth.Installation{row}
	}
	return nil
}

func (f *isoInstallations) ListForExternalID(_ context.Context, externalID string) ([]*auth.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byExternalLocked(externalID), nil
}

func (f *isoInstallations) SoftDelete(context.Context, string, string) error { return nil }
func (f *isoInstallations) MarkFirstRequestServed(context.Context, string) error {
	return nil
}
func (f *isoInstallations) UpdateExcludedModels(context.Context, string, string, []string) error {
	return nil
}
func (f *isoInstallations) UpdateAllowedModels(context.Context, string, string, []string) error {
	return nil
}
func (f *isoInstallations) UpdateRoutingPreference(context.Context, string, string, *float64) error {
	return nil
}
func (f *isoInstallations) UpdateContentCaptureMode(context.Context, string, string, *string) error {
	return nil
}
func (f *isoInstallations) UpdateHideTerminalSurfaces(context.Context, string, string, bool) error {
	return nil
}
func (f *isoInstallations) UpdateFlagOverrides(context.Context, string, string, flags.Overrides) error {
	return nil
}

// isoSessions models account_sessions with the verify-time join: a token hash
// resolves to session + owning account + that account's installation in one
// read, exactly like GetActiveSessionByTokenHash. The account and
// installation stores are injected after construction (the login transaction
// writes through the same shared stores).
type isoSessions struct {
	mu            sync.Mutex
	byHash        map[string]*auth.LoginSession
	revoked       map[string]bool
	accounts      *isoAccounts
	installations *isoInstallations
}

func newIsoSessions(accounts *isoAccounts, installations *isoInstallations) *isoSessions {
	return &isoSessions{
		byHash:        map[string]*auth.LoginSession{},
		revoked:       map[string]bool{},
		accounts:      accounts,
		installations: installations,
	}
}

func (f *isoSessions) InsertAccountSession(_ context.Context, params auth.InsertSessionParams) (*auth.LoginSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &auth.LoginSession{
		ID:          "sess-" + params.TokenHash[:8],
		AccountID:   params.AccountID,
		TokenHash:   params.TokenHash,
		TokenPrefix: params.TokenPrefix,
		ExpiresAt:   params.ExpiresAt,
	}
	f.byHash[params.TokenHash] = row
	return row, nil
}

func (f *isoSessions) GetActiveSessionByTokenHash(ctx context.Context, params auth.GetActiveSessionParams) (*auth.ActiveSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getActiveLocked(ctx, params)
}

func (f *isoSessions) getActiveLocked(_ context.Context, params auth.GetActiveSessionParams) (*auth.ActiveSession, error) {
	if f.revoked[params.TokenHash] {
		return nil, auth.ErrSessionNotFound
	}
	row, ok := f.byHash[params.TokenHash]
	if !ok {
		return nil, auth.ErrSessionNotFound
	}
	// Join account + installation (the SQL query's INNER JOINs).
	account, err := f.accounts.GetAccountByID(context.Background(), row.AccountID)
	if err != nil {
		return nil, auth.ErrSessionNotFound
	}
	installations, err := f.installations.ListForExternalID(context.Background(), account.InstallationID)
	if err != nil || len(installations) == 0 {
		return nil, auth.ErrSessionNotFound
	}
	return &auth.ActiveSession{
		Session:      *row,
		Account:      *account,
		Installation: installations[0],
	}, nil
}

func (f *isoSessions) TouchSessionLastSeen(_ context.Context, params auth.TouchSessionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byHash[params.TokenHash]; !ok {
		return auth.ErrSessionNotFound
	}
	return nil
}

func (f *isoSessions) RevokeSessionByTokenHash(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[tokenHash] = true
	return nil
}

func (f *isoSessions) RevokeAllSessionsForAccount(_ context.Context, accountID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for hash, row := range f.byHash {
		if row.AccountID == accountID && !f.revoked[hash] {
			f.revoked[hash] = true
			n++
		}
	}
	return n, nil
}

// isoLoginAttempts is a minimal attempt store (the paste path never touches
// it, but the login service requires the interface).
type isoLoginAttempts struct{}

func (isoLoginAttempts) InsertLoginAttempt(_ context.Context, params auth.InsertLoginAttemptParams) (*auth.LoginAttempt, error) {
	return &auth.LoginAttempt{ID: params.ID, Interval: params.Interval, ExpiresAt: params.ExpiresAt}, nil
}
func (isoLoginAttempts) GetActiveLoginAttempt(context.Context, string) (*auth.LoginAttempt, error) {
	return nil, auth.ErrLoginAttemptNotFound
}
func (isoLoginAttempts) MarkLoginAttemptConsumed(context.Context, string) error { return nil }
func (isoLoginAttempts) DeleteLoginAttempt(context.Context, string) error       { return nil }
func (isoLoginAttempts) DeleteExpiredLoginAttempts(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// isoCredentials models aiand_credentials, one row per installation.
type isoCredentials struct {
	mu   sync.Mutex
	rows map[string]*auth.AiandCredential
}

func (f *isoCredentials) Upsert(_ context.Context, params auth.UpsertAiandCredentialParams) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &auth.AiandCredential{
		ID:                     "cred-" + params.InstallationID,
		InstallationID:         params.InstallationID,
		AccessTokenCiphertext:  params.AccessTokenCiphertext,
		RefreshTokenCiphertext: params.RefreshTokenCiphertext,
		Plaintext:              params.AccessTokenCiphertext,
		ExpiresAt:              params.ExpiresAt,
		Origin:                 params.Origin,
		UserEmail:              params.UserEmail,
		OrgID:                  params.OrgID,
		OrgName:                params.OrgName,
	}
	f.rows[params.InstallationID] = row
	return row, nil
}

func (f *isoCredentials) GetByInstallation(_ context.Context, installationID string) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[installationID]
	if !ok {
		return nil, auth.ErrAiandCredentialNotFound
	}
	return row, nil
}

func (f *isoCredentials) Delete(_ context.Context, installationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, installationID)
	return nil
}

func (f *isoCredentials) ListExpiring(context.Context, time.Time) ([]auth.AiandExpiringCredential, error) {
	return nil, nil
}

// isoExternalKeys models model_router_external_api_keys per installation.
type isoExternalKeys struct {
	mu   sync.Mutex
	keys []*auth.ExternalAPIKey
}

func (f *isoExternalKeys) Create(_ context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &auth.ExternalAPIKey{
		ID:             params.ExternalID,
		InstallationID: params.InstallationID,
		Provider:       params.Provider,
	}
	f.keys = append(f.keys, row)
	return row, nil
}

func (f *isoExternalKeys) GetForInstallation(_ context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*auth.ExternalAPIKey
	for _, k := range f.keys {
		if k.InstallationID == installationID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *isoExternalKeys) SoftDeleteByProvider(_ context.Context, installationID, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []*auth.ExternalAPIKey
	for _, k := range f.keys {
		if k.InstallationID != installationID || k.Provider != provider {
			kept = append(kept, k)
		}
	}
	f.keys = kept
	return nil
}

func (f *isoExternalKeys) SoftDelete(context.Context, string, string) error { return nil }

func (f *isoExternalKeys) UpdateModelAliases(_ context.Context, installationID, id string, aliases map[string]string) (*auth.ExternalAPIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id && k.InstallationID == installationID {
			k.ModelAliases = aliases
			return k, nil
		}
	}
	return nil, auth.ErrExternalAPIKeyNotFound
}

func (f *isoExternalKeys) MarkUsed(context.Context, string) error { return nil }

// isoAPIKeys models model_router_api_keys per installation.
type isoAPIKeys struct {
	mu                 sync.Mutex
	keys               []*auth.APIKey
	installationLookup func(id string) (*auth.Installation, bool)
}

func (f *isoAPIKeys) Create(_ context.Context, params auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &auth.APIKey{
		ID:             params.ExternalID,
		InstallationID: params.InstallationID,
		ExternalID:     params.ExternalID,
		Scope:          params.Scope,
		KeyPrefix:      params.KeyPrefix,
		KeyHash:        params.KeyHash,
		KeySuffix:      params.KeySuffix,
	}
	f.keys = append(f.keys, row)
	return row, nil
}

func (f *isoAPIKeys) GetActiveByHashWithInstallation(_ context.Context, keyHash string) (*auth.APIKey, *auth.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.KeyHash == keyHash {
			// Resolve the owning installation from the shared store (the SQL
			// query joins model_router_installations).
			row, ok := f.installationsByID(k.InstallationID)
			if !ok {
				return nil, nil, sql.ErrNoRows
			}
			return k, row, nil
		}
	}
	return nil, nil, sql.ErrNoRows
}

func (f *isoAPIKeys) ListForInstallation(_ context.Context, installationID string) ([]*auth.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*auth.APIKey
	for _, k := range f.keys {
		if k.InstallationID == installationID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *isoAPIKeys) MarkUsed(context.Context, string) (bool, error) { return false, nil }

// installationsByID resolves the installation row a key belongs to. The
// fixture injects the lookup after construction to avoid an import cycle in
// the struct definition.
func (f *isoAPIKeys) installationsByID(id string) (*auth.Installation, bool) {
	if f.installationLookup == nil {
		return nil, false
	}
	return f.installationLookup(id)
}

func (f *isoAPIKeys) SoftDelete(_ context.Context, installationID, id string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []*auth.APIKey
	var removed int64
	for _, k := range f.keys {
		if k.ID == id && k.InstallationID == installationID {
			removed++
			continue
		}
		kept = append(kept, k)
	}
	f.keys = kept
	return removed, nil
}

// isoTelemetry records which installation every metrics read was scoped to —
// the direct proof that an account cookie drives installation-scoped (never
// *All) telemetry paths.
type isoTelemetry struct {
	mu                  sync.Mutex
	seenInstallationIDs []string
}

func (r *isoTelemetry) GetTelemetrySummary(_ context.Context, installationID string, _, _ time.Time) (proxy.TelemetrySummary, error) {
	r.mu.Lock()
	r.seenInstallationIDs = append(r.seenInstallationIDs, installationID)
	r.mu.Unlock()
	return proxy.TelemetrySummary{RequestCount: 42}, nil
}

// isoTxRunner hands the login transaction a repo set over the shared stores.
type isoTxRunner struct {
	accounts      *isoAccounts
	installations *isoInstallations
	sessions      *isoSessions
	credentials   *isoCredentials
	externalKeys  *isoExternalKeys
}

func (r *isoTxRunner) InTx(_ context.Context, fn func(auth.Tx) error) error {
	return fn(auth.Tx{
		ExternalAPIKeys:  r.externalKeys,
		AiandCredentials: r.credentials,
		Accounts:         r.accounts,
		Sessions:         r.sessions,
		LoginAttempts:    isoLoginAttempts{},
		Installations:    r.installations,
	})
}

// --- fixture ---------------------------------------------------------------

// isoIdentity is one seeded account + installation + cookie token.
type isoIdentity struct {
	account      *auth.Account
	installation *auth.Installation
	token        string
}

// isoFixture wires the full surface over shared fakes. Sessions mint through
// the REAL LoginService transaction, so the stored rows are exactly what
// production writes — and verify resolves account + installation through the
// same join the postgres query performs.
type isoFixture struct {
	svc           *auth.Service
	sessions      *isoSessions
	credentials   *isoCredentials
	externalKeys  *isoExternalKeys
	apiKeys       *isoAPIKeys
	telemetry     *isoTelemetry
	installations *isoInstallations
	accountsStore *isoAccounts
	accounts      map[string]isoIdentity
	logoutSvc     *auth.LoginService
}

const isoGatewayHostname = "router-host"

// newIsoFixture seeds two accounts whose sessions are minted through the real
// LoginService paste path, then wires the real admin handlers over the same
// stores the way server.go does.
func newIsoFixture(t *testing.T) *isoFixture {
	t.Helper()

	fx := &isoFixture{
		credentials:   &isoCredentials{rows: map[string]*auth.AiandCredential{}},
		externalKeys:  &isoExternalKeys{},
		apiKeys:       &isoAPIKeys{},
		telemetry:     &isoTelemetry{},
		accountsStore: newIsoAccounts(),
		accounts:      map[string]isoIdentity{},
	}
	fx.installations = newIsoInstallations()
	fx.sessions = newIsoSessions(fx.accountsStore, fx.installations)
	fx.apiKeys.installationLookup = func(id string) (*auth.Installation, bool) {
		rows, err := fx.installations.ListForExternalID(context.Background(), id)
		if err != nil || len(rows) == 0 {
			return nil, false
		}
		return rows[0], true
	}

	seed := func(userID, orgID, email string) isoIdentity {
		loginSvc, err := auth.NewLoginService(
			fx.accountsStore, fx.sessions, isoLoginAttempts{}, fx.installations,
			fx.credentials, fx.externalKeys,
			&isoGateway{userID: userID, email: email},
			auth.NoOpEncryptor{},
			&isoTxRunner{
				accounts:      fx.accountsStore,
				installations: fx.installations,
				sessions:      fx.sessions,
				credentials:   fx.credentials,
				externalKeys:  fx.externalKeys,
			},
			func() time.Time { return time.Unix(0, 0) },
			&isoNotifier{},
		)
		require.NoError(t, err)

		grant, err := loginSvc.LoginWithKey(context.Background(), "sk-aiand-seed-"+userID)
		require.NoError(t, err)

		account, err := fx.accountsStore.GetAccountByIdentity(context.Background(), userID, orgID)
		require.NoError(t, err)
		installations, err := fx.installations.ListForExternalID(context.Background(), account.ID)
		require.NoError(t, err)
		require.Len(t, installations, 1, "one installation per account (external_id = account id)")
		require.Equal(t, account.ID, installations[0].ExternalID)

		return isoIdentity{
			account:      account,
			installation: installations[0],
			token:        grant.Token,
		}
	}

	// Paste logins carry no token.Org, so the account identity pair is
	// (gateway user id, empty org) — exactly what production's paste path
	// writes. Distinct user ids still guarantee distinct accounts.
	fx.accounts["A"] = seed("user-a", "", "a@example.com")
	fx.accounts["B"] = seed("user-b", "", "b@example.com")
	require.NotEqual(t, fx.accounts["A"].installation.ID, fx.accounts["B"].installation.ID,
		"distinct accounts must hold distinct installations")

	// The handler-side auth service: backs the repository reads only (session
	// auth never consults it).
	fx.svc = auth.NewService(
		nil, fx.apiKeys, fx.externalKeys, nil,
		auth.NoOpAPIKeyCache{}, nil,
		func() time.Time { return time.Unix(0, 0) },
	).WithEncryptor(auth.NoOpEncryptor{}).
		WithAiandGateway(&isoGateway{}, fx.credentials, isoGatewayHostname)

	// The logout seam: a LoginService over the same shared stores, so
	// logout revokes through the exact production path.
	logoutSvc, err := auth.NewLoginService(
		fx.accountsStore, fx.sessions, isoLoginAttempts{}, fx.installations,
		fx.credentials, fx.externalKeys,
		&isoGateway{},
		auth.NoOpEncryptor{},
		&isoTxRunner{
			accounts:      fx.accountsStore,
			installations: fx.installations,
			sessions:      fx.sessions,
			credentials:   fx.credentials,
			externalKeys:  fx.externalKeys,
		},
		func() time.Time { return time.Unix(0, 0) },
		&isoNotifier{},
	)
	require.NoError(t, err)
	fx.logoutSvc = logoutSvc
	return fx
}

// isoGateway is the login/service gateway fake: one ValidateKey identity.
type isoGateway struct {
	mu     sync.Mutex
	userID string
	email  string
}

func (g *isoGateway) DeviceCodeStart(context.Context, string) (auth.AiandDeviceCode, error) {
	return auth.AiandDeviceCode{}, auth.ErrAiandGateway
}
func (g *isoGateway) PollToken(context.Context, string) (auth.AiandTokenResponse, error) {
	return auth.AiandTokenResponse{}, auth.ErrAiandGateway
}
func (g *isoGateway) RefreshToken(context.Context, string) (auth.AiandTokenResponse, error) {
	return auth.AiandTokenResponse{}, auth.ErrAiandGateway
}
func (g *isoGateway) Revoke(context.Context, string) error { return nil }
func (g *isoGateway) ValidateKey(_ context.Context, _ string) (auth.AiandUser, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return auth.AiandUser{ID: g.userID, Email: g.email}, nil
}

type isoNotifier struct{}

func (isoNotifier) NotifyInstallationChanged(string) {}

// verifier is the middleware-facing SessionVerifier over the session store —
// the same shape *LoginService exposes.
func (fx *isoFixture) verifier() auth.SessionVerifier {
	return &isoVerifier{sessions: fx.sessions}
}

type isoVerifier struct {
	sessions *isoSessions
}

func (v *isoVerifier) VerifySessionToken(token string) (*auth.ActiveSession, error) {
	return v.sessions.GetActiveSessionByTokenHash(context.Background(), auth.GetActiveSessionParams{
		TokenHash: auth.HashAPIKeySHA256(token),
	})
}

func (v *isoVerifier) TouchSessionLastSeen(tokenHash string) error {
	return v.sessions.TouchSessionLastSeen(context.Background(), auth.TouchSessionParams{TokenHash: tokenHash})
}

// engine mounts the admin surface the way server.go's selfhosted block does.
func (fx *isoFixture) engine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	verifier := fx.verifier()
	cache := middleware.NewSessionLRUCache()
	authSvc := fx.svc
	proxySvc := proxy.NewService(nil, nil, nil, false, nil, nil, false, "", "", telemetryRepo{fx.telemetry})

	engine := gin.New()
	// metrics group: WithAccountSessionOrAPIKey (server.go metrics block)
	metrics := engine.Group("/admin/v1", middleware.WithAccountSessionOrAPIKey(verifier, cache, authSvc, false))
	metrics.GET("/metrics/summary", admin.MetricsSummaryHandler(proxySvc))
	metrics.GET("/metrics/timeseries", admin.MetricsTimeseriesHandler(proxySvc))
	metrics.GET("/metrics/model-breakdown", admin.MetricsModelBreakdownHandler(proxySvc))
	metrics.GET("/metrics/details", admin.MetricsDetailsHandler(proxySvc))

	// mgmt group: WithAccountSession (server.go mgmt block)
	mgmt := engine.Group("/admin/v1", middleware.WithAccountSession(verifier, cache))
	mgmt.GET("/keys", admin.ListAPIKeysHandler(authSvc))
	mgmt.POST("/keys", admin.IssueAPIKeyHandler(authSvc))
	mgmt.DELETE("/keys/:id", admin.DeleteAPIKeyHandler(authSvc))
	mgmt.GET("/provider-keys", admin.ListExternalKeysHandler(authSvc))
	mgmt.POST("/provider-keys", admin.UpsertExternalKeyHandler(authSvc, nil))
	mgmt.DELETE("/provider-keys/:id", admin.DeleteExternalKeyHandler(authSvc))
	mgmt.GET("/onboarding", admin.OnboardingHandler(authSvc))
	mgmt.GET("/config", admin.ConfigHandler)
	mgmt.GET("/routing-preferences", admin.GetRoutingPreferencesHandler(authSvc))
	mgmt.PUT("/routing-preferences", admin.UpdateRoutingPreferencesHandler(authSvc))
	mgmt.GET("/aiand-auth/status", admin.AiandAuthStatusHandler(authSvc))
	mgmt.POST("/aiand-auth/disconnect", admin.DisconnectAiandAuthHandler(authSvc))
	return engine
}

// telemetryRepo adapts isoTelemetry into proxy.TelemetryRepository.
type telemetryRepo struct {
	*isoTelemetry
}

func (telemetryRepo) InsertRequestTelemetry(context.Context, proxy.InsertTelemetryParams) error {
	return nil
}
func (telemetryRepo) GetTelemetryTimeseries(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetrySummaryAll(context.Context, time.Time, time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}
func (telemetryRepo) GetTelemetryTimeseriesAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetryRows(context.Context, string, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetryRowsAll(context.Context, time.Time, time.Time, int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetryModelBreakdown(context.Context, string, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetryModelBreakdownAll(context.Context, time.Time, time.Time, string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}
func (telemetryRepo) GetTelemetryBySessionSequence(context.Context, uuid.UUID, []byte, string, int) (proxy.TelemetryTurnResult, error) {
	return proxy.TelemetryTurnResult{}, nil
}
func (telemetryRepo) GetSessionCost(context.Context, string, string) (proxy.SessionCost, error) {
	return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
}

// cookieReq issues a request with A's or B's session cookie.
func (fx *isoFixture) cookieReq(t *testing.T, engine *gin.Engine, who, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.LoginSessionCookieName, Value: fx.accounts[who].token})
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return rr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if rr.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "response must be JSON: %q", rr.Body.String())
	}
	return body
}

const isoAccountSessionRequired = "account_session_required"

// --- matrix row 1: IDOR -----------------------------------------------------

// TestIsolation_IDOR_MetricsScopesToOwnInstallation drives all four metrics
// reads with A's cookie and requires the telemetry layer to be scoped to A's
// installation — never B's rows, never the cross-installation *All paths.
func TestIsolation_IDOR_MetricsScopesToOwnInstallation(t *testing.T) {
	fx := newIsoFixture(t)
	engine := fx.engine()

	for _, path := range []string{
		"/admin/v1/metrics/summary",
		"/admin/v1/metrics/timeseries",
		"/admin/v1/metrics/model-breakdown",
		"/admin/v1/metrics/details",
	} {
		rr := fx.cookieReq(t, engine, "A", http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rr.Code, "%s must serve 200 to its own account", path)
		fx.telemetry.mu.Lock()
		last := fx.telemetry.seenInstallationIDs[len(fx.telemetry.seenInstallationIDs)-1]
		fx.telemetry.mu.Unlock()
		require.Equal(t, fx.accounts["A"].installation.ID, last,
			"%s must scope to the cookie account's installation", path)
	}
}

// TestIsolation_IDOR_KeyListAndIssueNeverCrossAccounts: A's cookie lists and
// issues router keys and must see only A's keys; B's pre-seeded key never
// appears, and deleting B's key id from A's session never removes B's key.
func TestIsolation_IDOR_KeyListAndIssueNeverCrossAccounts(t *testing.T) {
	fx := newIsoFixture(t)
	// One key per account, seeded through the handler-facing service.
	_, _, err := fx.svc.IssueAPIKey(context.Background(), fx.accounts["A"].installation.ID, nil, nil)
	require.NoError(t, err)
	bKey, _, err := fx.svc.IssueAPIKey(context.Background(), fx.accounts["B"].installation.ID, nil, nil)
	require.NoError(t, err)
	engine := fx.engine()

	rr := fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/keys", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Keys, 1, "A must see exactly A's key")
	for _, k := range list.Keys {
		assert.NotEqual(t, bKey.ID, k.ID, "B's key must never leak into A's list")
	}

	// A deletes B's key id: never reports success against B's key.
	rr = fx.cookieReq(t, engine, "A", http.MethodDelete, "/admin/v1/keys/"+bKey.ID, "")
	assert.NotEqual(t, http.StatusNoContent, rr.Code,
		"a foreign key delete must not report success")
	surviving, err := fx.svc.ListAPIKeys(context.Background(), fx.accounts["B"].installation.ID)
	require.NoError(t, err)
	require.Len(t, surviving, 1, "B's key must survive A's delete attempt")

	// A issues a fresh key: it lands in A's installation, never B's.
	rr = fx.cookieReq(t, engine, "A", http.MethodPost, "/admin/v1/keys", `{"name":"new"}`)
	require.Equal(t, http.StatusCreated, rr.Code)
	aKeys, err := fx.svc.ListAPIKeys(context.Background(), fx.accounts["A"].installation.ID)
	require.NoError(t, err)
	require.Len(t, aKeys, 2, "the issued key belongs to A")
	bKeys, err := fx.svc.ListAPIKeys(context.Background(), fx.accounts["B"].installation.ID)
	require.NoError(t, err)
	require.Len(t, bKeys, 1, "B's key set untouched by A's issue")
}

// TestIsolation_IDOR_ProviderKeysScopedToOwnInstallation: A's session lists
// only A's propagated aiand provider key; B's rows are invisible, and A's
// disconnect hits A's credential row only.
func TestIsolation_IDOR_ProviderKeysScopedToOwnInstallation(t *testing.T) {
	fx := newIsoFixture(t)
	engine := fx.engine()

	rr := fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/provider-keys", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Keys []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.NotEmpty(t, list.Keys, "A's propagated aiand key must be listed")
	bKeys, err := fx.externalKeys.GetForInstallation(context.Background(), fx.accounts["B"].installation.ID)
	require.NoError(t, err)
	require.Len(t, bKeys, 1)
	for _, k := range list.Keys {
		assert.Equal(t, "aiand", k.Provider)
		assert.NotEqual(t, bKeys[0].ID, k.ID, "B's provider key must not leak into A's list")
	}

	// Exactly one provider key per account; A's read left B's row intact.
	keysForA, err := fx.externalKeys.GetForInstallation(context.Background(), fx.accounts["A"].installation.ID)
	require.NoError(t, err)
	require.Len(t, keysForA, 1)
}

// TestIsolation_IDOR_AiandStatusAndDisconnectAreInstallationScoped: A's
// status reflects A's credential; A disconnecting removes A's credential +
// provider key and leaves B's credential intact; B's session continues to
// serve B's status.
func TestIsolation_IDOR_AiandStatusAndDisconnectAreInstallationScoped(t *testing.T) {
	fx := newIsoFixture(t)
	engine := fx.engine()

	rr := fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/aiand-auth/status", "")
	require.Equal(t, http.StatusOK, rr.Code)
	status := decodeBody(t, rr)
	assert.Equal(t, true, status["connected"])
	assert.Equal(t, "a@example.com", status["email"], "A's status shows A's credential email")

	// A disconnects.
	rr = fx.cookieReq(t, engine, "A", http.MethodPost, "/admin/v1/aiand-auth/disconnect", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// A's credential + provider key are gone; B's are intact.
	_, err := fx.credentials.GetByInstallation(context.Background(), fx.accounts["A"].installation.ID)
	assert.ErrorIs(t, err, auth.ErrAiandCredentialNotFound, "disconnect must clear A's credential")
	bCred, err := fx.credentials.GetByInstallation(context.Background(), fx.accounts["B"].installation.ID)
	require.NoError(t, err, "B's credential must survive A's disconnect")
	assert.Equal(t, "b@example.com", bCred.UserEmail)

	// B's session still sees a connected status.
	rr = fx.cookieReq(t, engine, "B", http.MethodGet, "/admin/v1/aiand-auth/status", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, true, decodeBody(t, rr)["connected"], "B's connection must be unaffected")
}

// TestIsolation_IDOR_OnboardingConfigRoutingPrefsAreOwnInstallation asserts
// the remaining per-installation read surfaces answer from the cookie's own
// installation and 401 (account_session_required) without any session.
func TestIsolation_IDOR_OnboardingConfigRoutingPrefsAreOwnInstallation(t *testing.T) {
	fx := newIsoFixture(t)
	engine := fx.engine()

	rr := fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/onboarding", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, `{"first_request_served_at":null}`, rr.Body.String())

	rr = fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/config", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Routing preferences read A's installation weight (nil → default).
	rr = fx.cookieReq(t, engine, "A", http.MethodGet, "/admin/v1/routing-preferences", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, `{"quality":70,"price":30,"is_default":true}`, rr.Body.String())

	// No cookie at all: every surface 401s with the bounce code.
	for _, path := range []string{
		"/admin/v1/onboarding", "/admin/v1/config", "/admin/v1/routing-preferences",
		"/admin/v1/keys", "/admin/v1/provider-keys", "/admin/v1/aiand-auth/status",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must 401 without a session", path)
		assert.Contains(t, rr.Body.String(), isoAccountSessionRequired,
			"%s must use the account-session bounce body", path)
	}
}

// TestIsolation_IDOR_SessionNeverResolvesToAnotherInstallation is the
// invariant under every other row, stated directly: verify A's token and
// require the resolved installation to be A's, never B's.
func TestIsolation_IDOR_SessionNeverResolvesToAnotherInstallation(t *testing.T) {
	fx := newIsoFixture(t)
	verifier := fx.verifier()

	activeA, err := verifier.VerifySessionToken(fx.accounts["A"].token)
	require.NoError(t, err)
	require.Equal(t, fx.accounts["A"].account.ID, activeA.Account.ID)
	require.Equal(t, fx.accounts["A"].installation.ID, activeA.Installation.ID)
	require.NotEqual(t, fx.accounts["B"].installation.ID, activeA.Installation.ID)

	activeB, err := verifier.VerifySessionToken(fx.accounts["B"].token)
	require.NoError(t, err)
	require.Equal(t, fx.accounts["B"].installation.ID, activeB.Installation.ID)
}
