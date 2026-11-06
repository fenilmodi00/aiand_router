package auth_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAiandGateway records gateway calls and replays scripted responses, so
// tests can assert the exact wire behavior the service drives.
type fakeAiandGateway struct {
	mu sync.Mutex

	startResponses []startResult
	pollResponses  []pollResult
	refreshResult  pollResult
	refreshErr     error
	// refreshFailsFor maps refresh tokens to a forced failure, so tests can
	// fail one installation's rotation without failing every row's.
	refreshFailsFor map[string]bool
	revokeErr       error
	validateUser    auth.AiandUser
	validateErr     error

	starts  []string // key_name arguments
	polls   []string // device codes
	refresh []string // refresh tokens
	revokes []string
}

type startResult struct {
	code auth.AiandDeviceCode
	err  error
}

type pollResult struct {
	token auth.AiandTokenResponse
	err   error
}

func (f *fakeAiandGateway) DeviceCodeStart(_ context.Context, keyName string) (auth.AiandDeviceCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, keyName)
	if len(f.startResponses) == 0 {
		return auth.AiandDeviceCode{}, errors.New("no scripted start response")
	}
	out := f.startResponses[0]
	f.startResponses = f.startResponses[1:]
	return out.code, out.err
}

func (f *fakeAiandGateway) PollToken(_ context.Context, deviceCode string) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls = append(f.polls, deviceCode)
	if len(f.pollResponses) == 0 {
		return auth.AiandTokenResponse{}, errors.New("no scripted poll response")
	}
	out := f.pollResponses[0]
	f.pollResponses = f.pollResponses[1:]
	return out.token, out.err
}

func (f *fakeAiandGateway) RefreshToken(_ context.Context, refreshToken string) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = append(f.refresh, refreshToken)
	if f.refreshFailsFor[refreshToken] {
		return auth.AiandTokenResponse{}, auth.ErrAiandGateway
	}
	return f.refreshResult.token, f.refreshErr
}

func (f *fakeAiandGateway) Revoke(_ context.Context, refreshToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes = append(f.revokes, refreshToken)
	return f.revokeErr
}

func (f *fakeAiandGateway) ValidateKey(_ context.Context, key string) (auth.AiandUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validateUser, f.validateErr
}

// fakeAiandCredentialRepo is an in-memory auth.AiandCredentialRepository.
type fakeAiandCredentialRepo struct {
	mu      sync.Mutex
	rows    map[string]*auth.AiandCredential
	upserts []auth.UpsertAiandCredentialParams
	getErr  error
	// expiring scripts ListExpiring directly; nil falls back to listing the
	// stored rows whose ExpiresAt precedes the requested threshold.
	expiring []auth.AiandExpiringCredential
	listErr  error
}

func newFakeAiandCredentialRepo() *fakeAiandCredentialRepo {
	return &fakeAiandCredentialRepo{rows: map[string]*auth.AiandCredential{}}
}

func (f *fakeAiandCredentialRepo) Upsert(_ context.Context, params auth.UpsertAiandCredentialParams) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, params)
	now := time.Unix(0, 0)
	row := &auth.AiandCredential{
		ID:                     "cred-" + params.InstallationID,
		InstallationID:         params.InstallationID,
		AccessTokenCiphertext:  params.AccessTokenCiphertext,
		RefreshTokenCiphertext: params.RefreshTokenCiphertext,
		ExpiresAt:              params.ExpiresAt,
		Origin:                 params.Origin,
		UserEmail:              params.UserEmail,
		OrgID:                  params.OrgID,
		OrgName:                params.OrgName,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	f.rows[params.InstallationID] = row
	return row, nil
}

func (f *fakeAiandCredentialRepo) GetByInstallation(_ context.Context, installationID string) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	row, ok := f.rows[installationID]
	if !ok {
		return nil, auth.ErrAiandCredentialNotFound
	}
	return row, nil
}

func (f *fakeAiandCredentialRepo) Delete(_ context.Context, installationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, installationID)
	return nil
}

// ListExpiring replays the scripted expiring rows when set; otherwise it
// lists every stored row so ordinary lifecycle tests keep working.
func (f *fakeAiandCredentialRepo) ListExpiring(_ context.Context, before time.Time) ([]auth.AiandExpiringCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.expiring != nil {
		return f.expiring, nil
	}
	var out []auth.AiandExpiringCredential
	for _, row := range f.rows {
		if !row.ExpiresAt.Before(before) {
			continue
		}
		out = append(out, auth.AiandExpiringCredential{
			Credential:             *row,
			InstallationExternalID: testInstallation.ExternalID,
		})
	}
	return out, nil
}

func (f *fakeAiandCredentialRepo) deleted(installationID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[installationID]
	return !ok
}

// recordingExternalKeyRepo captures the propagation upserts.
type recordingExternalKeyRepo struct {
	upserts []upsertRecord
	deleted []string
}

type upsertRecord struct {
	provider string
	rawKey   string
}

func (f *recordingExternalKeyRepo) Create(_ context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	f.upserts = append(f.upserts, upsertRecord{provider: params.Provider, rawKey: string(params.KeyCiphertext)})
	return &auth.ExternalAPIKey{
		ID:             "ek-1",
		InstallationID: params.InstallationID,
		Provider:       params.Provider,
		KeyPrefix:      params.KeyPrefix,
		KeySuffix:      params.KeySuffix,
	}, nil
}

func (f *recordingExternalKeyRepo) GetForInstallation(_ context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	var out []*auth.ExternalAPIKey
	for _, up := range f.upserts {
		if up.provider == providers.ProviderAIAND {
			out = append(out, &auth.ExternalAPIKey{Provider: up.provider, Plaintext: []byte(up.rawKey)})
		}
	}
	return out, nil
}

func (f *recordingExternalKeyRepo) SoftDeleteByProvider(_ context.Context, _, provider string) error {
	f.deleted = append(f.deleted, provider)
	return nil
}

func (f *recordingExternalKeyRepo) SoftDelete(_ context.Context, _, _ string) error { return nil }

func (f *recordingExternalKeyRepo) UpdateModelAliases(_ context.Context, _, _ string, _ map[string]string) (*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}

func (f *recordingExternalKeyRepo) MarkUsed(_ context.Context, _ string) error { return nil }

// makeAiandService builds a Service wired with the fake gateway, credential
// repo, and external-key repo. The NoOp encryptor keeps ciphertexts equal to
// plaintext so assertions can read them directly.
func makeAiandService(t *testing.T, gateway *fakeAiandGateway, creds *fakeAiandCredentialRepo, external *recordingExternalKeyRepo) *auth.Service {
	t.Helper()
	return auth.NewService(nil, nil, external, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) }).
		WithEncryptor(auth.NoOpEncryptor{}).
		WithAiandGateway(gateway, creds, "router-host")
}

var testInstallation = &auth.Installation{ID: "inst-1", ExternalID: "ext-1"}

func TestStartAiandDeviceLogin_ReturnsUserCodeAndRecordsPending(t *testing.T) {
	gateway := &fakeAiandGateway{startResponses: []startResult{{
		code: auth.AiandDeviceCode{
			DeviceCode: "dc-secret", UserCode: "ABCD-1234",
			VerificationURI: "https://api.aiand.com/device", ExpiresIn: 900, Interval: 5,
		},
	}}}
	creds := newFakeAiandCredentialRepo()
	svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})

	login, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	assert.Equal(t, "ABCD-1234", login.UserCode)
	assert.Equal(t, "https://api.aiand.com/device", login.VerificationURI)
	assert.Equal(t, 900, login.ExpiresIn)
	assert.Equal(t, 5, login.Interval)
	require.Len(t, gateway.starts, 1)
	assert.Equal(t, "aiand-router@router-host", gateway.starts[0], "gateway key_name must label this router host")

	// A second start replaces the pending one (singleton per installation).
	gateway.startResponses = []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-2", UserCode: "ZZZZ-9999", Interval: 5}}}
	_, err = svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	gateway.pollResponses = []pollResult{{token: auth.AiandTokenResponse{AccessToken: "sk-a", RefreshToken: "r", ExpiresIn: 60}}}
	outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
	require.Equal(t, auth.AiandPollStatusAuthorized, outcome.Status)
	require.Len(t, gateway.polls, 1)
	assert.Equal(t, "dc-2", gateway.polls[0], "the replacement device code must be the one polled")
}

func TestPollAiandDeviceLogin_NoPendingLoginIsError(t *testing.T) {
	svc := makeAiandService(t, &fakeAiandGateway{}, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
	_, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	assert.ErrorIs(t, err, auth.ErrAiandLoginPending)
}

func TestPollAiandDeviceLogin_OutcomeStatuses(t *testing.T) {
	t.Run("pending stays pending", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{{err: auth.ErrAiandAuthorizationPending}},
		}
		svc := makeAiandService(t, gateway, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusPending, outcome.Status)
	})

	t.Run("slow_down raises interval by five", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{{err: auth.ErrAiandSlowDown}},
		}
		svc := makeAiandService(t, gateway, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.Error(t, err)
		var slow *auth.AiandSlowDownError
		require.True(t, errors.As(err, &slow), "slow_down must surface as AiandSlowDownError")
		assert.Equal(t, 10*time.Second, slow.RetryAfter, "gateway contract: interval + 5s")
	})

	t.Run("denied clears pending", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{{err: auth.ErrAiandAccessDenied}},
		}
		svc := makeAiandService(t, gateway, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusDenied, outcome.Status)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		assert.ErrorIs(t, err, auth.ErrAiandLoginPending, "denied flow must be gone")
	})

	t.Run("expired clears pending", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{{err: auth.ErrAiandExpiredToken}},
		}
		svc := makeAiandService(t, gateway, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusExpired, outcome.Status)
	})

	t.Run("gateway failure is 502-shaped error", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{{err: auth.ErrAiandGateway}},
		}
		svc := makeAiandService(t, gateway, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		assert.ErrorIs(t, err, auth.ErrAiandGatewayUnavailable)
	})
}

func TestPollAiandDeviceLogin_AuthorizedPersistsAndPropagates(t *testing.T) {
	gateway := &fakeAiandGateway{
		startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
		pollResponses: []pollResult{{token: auth.AiandTokenResponse{
			AccessToken: "sk-aiand-123456789", RefreshToken: "rt-1", ExpiresIn: 3600,
			Org: &auth.AiandOrg{ID: "org-1", Name: "Acme"},
		}}},
	}
	creds := newFakeAiandCredentialRepo()
	external := &recordingExternalKeyRepo{}
	svc := makeAiandService(t, gateway, creds, external)

	_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
	outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	require.Equal(t, auth.AiandPollStatusAuthorized, outcome.Status)
	assert.Equal(t, "sk-aiand…6789", outcome.KeyMask, "only first8+last4 may reach the dashboard")
	assert.Equal(t, "Acme", outcome.OrgName)
	require.NotNil(t, outcome.ExpiresAt)
	assert.Equal(t, time.Unix(0, 0).Add(3600*time.Second), *outcome.ExpiresAt)

	// Persisted credential is sealed through the encryptor and carries the org.
	require.Len(t, creds.upserts, 1)
	up := creds.upserts[0]
	assert.Equal(t, "inst-1", up.InstallationID)
	assert.Equal(t, auth.AiandOriginDevice, up.Origin)
	assert.Equal(t, "org-1", up.OrgID)
	assert.Equal(t, []byte("sk-aiand-123456789"), up.AccessTokenCiphertext, "NoOp encryptor keeps plaintext legible")
	assert.Equal(t, []byte("rt-1"), up.RefreshTokenCiphertext)

	// Propagated as the aiand provider's external key.
	require.Len(t, external.upserts, 1)
	assert.Equal(t, providers.ProviderAIAND, external.upserts[0].provider)
	assert.Equal(t, "sk-aiand-123456789", external.upserts[0].rawKey)

	// Pending login consumed.
	_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	assert.ErrorIs(t, err, auth.ErrAiandLoginPending)
}

func TestCompleteAiandPasteLogin_RejectedKeyIsInvalidSentinel(t *testing.T) {
	gateway := &fakeAiandGateway{validateErr: auth.ErrAiandInvalidKey}
	creds := newFakeAiandCredentialRepo()
	svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})

	_, err := svc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-bad")
	assert.ErrorIs(t, err, auth.ErrInvalidAiandKey)
	assert.Empty(t, creds.upserts, "a rejected key must not be persisted")
}

func TestCompleteAiandPasteLogin_SuccessStoresPasteOriginWithoutRefresh(t *testing.T) {
	gateway := &fakeAiandGateway{validateUser: auth.AiandUser{ID: "user-1", Email: "ops@example.com"}}
	creds := newFakeAiandCredentialRepo()
	external := &recordingExternalKeyRepo{}
	svc := makeAiandService(t, gateway, creds, external)

	credential, err := svc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-pasted-key-9999")
	require.NoError(t, err)

	assert.Equal(t, auth.AiandOriginPaste, credential.Origin)
	assert.Equal(t, "ops@example.com", credential.UserEmail)
	require.Len(t, creds.upserts, 1)
	up := creds.upserts[0]
	assert.Equal(t, auth.AiandOriginPaste, up.Origin)
	assert.NotNil(t, up.RefreshTokenCiphertext, "empty bytea sentinel, never nil (refresh_token_ciphertext NOT NULL)")
	assert.Equal(t, []byte{}, up.RefreshTokenCiphertext, "empty bytea sentinel, never nil (refresh_token_ciphertext NOT NULL)")
	assert.Equal(t, []byte("sk-pasted-key-9999"), up.AccessTokenCiphertext)
	require.Len(t, external.upserts, 1)
	assert.Equal(t, "sk-pasted-key-9999", external.upserts[0].rawKey)
}

func TestAiandCredentialStatus_MasksKeyAndReportsFields(t *testing.T) {
	creds := newFakeAiandCredentialRepo()
	svc := makeAiandService(t, &fakeAiandGateway{validateUser: auth.AiandUser{Email: "ops@example.com"}}, creds, &recordingExternalKeyRepo{})

	status, err := svc.AiandCredentialStatus(context.Background(), testInstallation)
	require.NoError(t, err)
	assert.False(t, status.Connected, "no credential yet")

	_, err = svc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-aiand-abcdefgh")
	require.NoError(t, err)
	status, err = svc.AiandCredentialStatus(context.Background(), testInstallation)
	require.NoError(t, err)
	assert.True(t, status.Connected)
	assert.Equal(t, "ops@example.com", status.Email)
	assert.Equal(t, auth.AiandOriginPaste, status.Origin)
	assert.Equal(t, "sk-aiand…efgh", status.KeyMask)
	assert.NotContains(t, status.KeyMask, "sk-aiand-abcdefgh", "the raw key must never leak into the status")
}

func TestDisconnectAiandCredential_RevokesDeviceTokenAndDeletesRow(t *testing.T) {
	gateway := &fakeAiandGateway{
		startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
		pollResponses: []pollResult{{token: auth.AiandTokenResponse{
			AccessToken: "sk-1", RefreshToken: "rt-revoke-me", ExpiresIn: 60,
		}}},
	}
	creds := newFakeAiandCredentialRepo()
	external := &recordingExternalKeyRepo{}
	svc := makeAiandService(t, gateway, creds, external)

	_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
	_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	require.NoError(t, svc.DisconnectAiandCredential(context.Background(), testInstallation))

	require.Len(t, gateway.revokes, 1)
	assert.Equal(t, "rt-revoke-me", gateway.revokes[0])
	assert.True(t, creds.deleted("inst-1"))
	require.NotEmpty(t, external.deleted)
	assert.Equal(t, providers.ProviderAIAND, external.deleted[len(external.deleted)-1], "the propagated aiand key must go with it")

	// Paste-origin credentials have no refresh token to revoke.
	pasteSvc := makeAiandService(t, &fakeAiandGateway{validateUser: auth.AiandUser{Email: "x@y.z"}}, creds, external)
	_, err = pasteSvc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-2")
	require.NoError(t, err)
	require.NoError(t, svc.DisconnectAiandCredential(context.Background(), testInstallation))
	assert.Empty(t, gateway.revokes[1:], "paste disconnect must not call Revoke")
}

func TestDisconnectAiandCredential_RevocationFailureIsNotFatal(t *testing.T) {
	gateway := &fakeAiandGateway{
		startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
		pollResponses:  []pollResult{{token: auth.AiandTokenResponse{AccessToken: "sk-1", RefreshToken: "rt-1", ExpiresIn: 60}}},
		revokeErr:      auth.ErrAiandGateway,
	}
	creds := newFakeAiandCredentialRepo()
	svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})
	_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
	_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	require.NoError(t, svc.DisconnectAiandCredential(context.Background(), testInstallation),
		"gateway revocation failure is logged, not fatal")
	assert.True(t, creds.deleted("inst-1"))
}

func TestRotateAiandCredentialIfExpiring(t *testing.T) {
	deviceToken := func(access, refresh string) pollResult {
		return pollResult{token: auth.AiandTokenResponse{AccessToken: access, RefreshToken: refresh, ExpiresIn: 3600}}
	}

	t.Run("rotates inside the window and re-propagates", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{deviceToken("sk-old", "rt-old")},
			refreshResult:  deviceToken("sk-new", "rt-new"),
		}
		creds := newFakeAiandCredentialRepo()
		external := &recordingExternalKeyRepo{}
		svc := makeAiandService(t, gateway, creds, external)
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)

		rotated, err := svc.RotateAiandCredentialIfExpiring(context.Background(), testInstallation, 72*time.Hour)
		require.NoError(t, err)
		assert.True(t, rotated)
		require.Len(t, gateway.refresh, 1)
		assert.Equal(t, "rt-old", gateway.refresh[0])
		require.Len(t, gateway.refresh, 1)
		assert.Equal(t, "sk-old", string(creds.upserts[0].AccessTokenCiphertext))
		assert.Equal(t, "sk-new", string(creds.upserts[1].AccessTokenCiphertext), "rotation must replace the stored access token")
		require.Len(t, external.upserts, 2, "the propagated key must rotate with it")
		assert.Equal(t, "sk-new", external.upserts[1].rawKey)
	})

	t.Run("skips when expiry is outside the window", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}},
			pollResponses:  []pollResult{deviceToken("sk-old", "rt-old")},
		}
		creds := newFakeAiandCredentialRepo()
		svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)

		rotated, err := svc.RotateAiandCredentialIfExpiring(context.Background(), testInstallation, time.Second)
		require.NoError(t, err)
		assert.False(t, rotated)
		assert.Empty(t, gateway.refresh)
	})

	t.Run("paste-origin credentials never rotate", func(t *testing.T) {
		creds := newFakeAiandCredentialRepo()
		gateway := &fakeAiandGateway{validateUser: auth.AiandUser{Email: "x@y.z"}}
		svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})
		_, err := svc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-paste")
		require.NoError(t, err)
		rotated, err := svc.RotateAiandCredentialIfExpiring(context.Background(), testInstallation, 72*time.Hour)
		require.NoError(t, err)
		assert.False(t, rotated)
		assert.Empty(t, gateway.refresh)
	})

	t.Run("absent credential is a no-op", func(t *testing.T) {
		svc := makeAiandService(t, &fakeAiandGateway{}, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		rotated, err := svc.RotateAiandCredentialIfExpiring(context.Background(), testInstallation, 72*time.Hour)
		require.NoError(t, err)
		assert.False(t, rotated)
	})
}

// TestAiandServiceNeverLogsKeyMaterial is a static guard: the credential
// service must not pass tokens or refresh tokens into any logging call.
func TestAiandServiceNeverLogsKeyMaterial(t *testing.T) {
	raw, err := os.ReadFile(sourceFilePath(t, "aiand_credential_service.go"))
	require.NoError(t, err)
	source := string(raw)
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		isLogCall := strings.Contains(trimmed, ".Warn(") || strings.Contains(trimmed, ".Info(") ||
			strings.Contains(trimmed, ".Error(") || strings.Contains(trimmed, ".Debug(")
		if !isLogCall {
			continue
		}
		assert.NotContains(t, trimmed, "AccessToken", "log line must not carry the access token: %s", trimmed)
		assert.NotContains(t, trimmed, "refresh", "log line must not carry a refresh token: %s", trimmed)
	}
}

// sourceFilePath locates a sibling source file regardless of the test's
// working directory, via this test file's own compiled location.
func sourceFilePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), name)
}

func TestSweepExpiringAiandCredentials(t *testing.T) {
	// seedDeviceCredential stores one device-origin credential for the
	// installation, expiring inside the sweep window.
	seedDeviceCredential := func(t *testing.T, svc *auth.Service, creds *fakeAiandCredentialRepo, outer *fakeAiandGateway, installation *auth.Installation) {
		t.Helper()
		gateway := &fakeAiandGateway{
			startResponses: []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-" + installation.ID, Interval: 5}}},
			pollResponses:  []pollResult{{token: auth.AiandTokenResponse{AccessToken: "sk-" + installation.ID, RefreshToken: "rt-" + installation.ID, ExpiresIn: 60}}},
		}
		// Swap the gateway in for the device login, then restore the one the
		// sweep under test should drive.
		svc.WithAiandGateway(gateway, creds, "router-host")
		_, err := svc.StartAiandDeviceLogin(context.Background(), installation)
		require.NoError(t, err)
		_, err = svc.PollAiandDeviceLogin(context.Background(), installation)
		require.NoError(t, err)
		svc.WithAiandGateway(outer, creds, "router-host")
	}

	t.Run("rotates expiring rows and counts them", func(t *testing.T) {
		gateway := &fakeAiandGateway{
			refreshResult: pollResult{token: auth.AiandTokenResponse{AccessToken: "sk-new", RefreshToken: "rt-new", ExpiresIn: 3600}},
		}
		creds := newFakeAiandCredentialRepo()
		external := &recordingExternalKeyRepo{}
		svc := makeAiandService(t, gateway, creds, external)
		seedDeviceCredential(t, svc, creds, gateway, testInstallation)

		rotated, err := svc.SweepExpiringAiandCredentials(context.Background(), 72*time.Hour)
		require.NoError(t, err)
		assert.Equal(t, 1, rotated)
		require.Len(t, gateway.refresh, 1)
		assert.Equal(t, "rt-inst-1", gateway.refresh[0])
		require.Len(t, creds.upserts, 2)
		assert.Equal(t, "sk-new", string(creds.upserts[1].AccessTokenCiphertext),
			"sweep must replace the stored access token")
		require.Len(t, external.upserts, 2, "the propagated key must rotate with it")
		assert.Equal(t, "sk-new", external.upserts[1].rawKey)
	})

	t.Run("skips paste-origin rows without error", func(t *testing.T) {
		creds := newFakeAiandCredentialRepo()
		svc := makeAiandService(t, &fakeAiandGateway{validateUser: auth.AiandUser{Email: "x@y.z"}}, creds, &recordingExternalKeyRepo{})
		_, err := svc.CompleteAiandPasteLogin(context.Background(), testInstallation, "sk-paste")
		require.NoError(t, err)

		rotated, err := svc.SweepExpiringAiandCredentials(context.Background(), 72*time.Hour)
		require.NoError(t, err)
		assert.Equal(t, 0, rotated, "paste rows carry no refresh token and must be skipped")
	})

	t.Run("continues past a failing installation", func(t *testing.T) {
		other := &auth.Installation{ID: "inst-2", ExternalID: "ext-2"}
		gateway := &fakeAiandGateway{
			refreshResult: pollResult{token: auth.AiandTokenResponse{AccessToken: "sk-new-2", RefreshToken: "rt-new-2", ExpiresIn: 3600}},
		}
		creds := newFakeAiandCredentialRepo()
		svc := makeAiandService(t, gateway, creds, &recordingExternalKeyRepo{})
		seedDeviceCredential(t, svc, creds, gateway, testInstallation)
		seedDeviceCredential(t, svc, creds, gateway, other)

		// The refresh for inst-1 fails; inst-2 must still rotate.
		gateway.refreshFailsFor = map[string]bool{"rt-inst-1": true}
		rotated, err := svc.SweepExpiringAiandCredentials(context.Background(), 72*time.Hour)
		require.NoError(t, err, "a per-installation failure is logged and skipped, not returned")
		assert.Equal(t, 1, rotated)
		assert.Len(t, creds.upserts, 3, "inst-1 fails, inst-2 rotates: two seeds plus one rotation upsert")
	})

	t.Run("list failure is returned", func(t *testing.T) {
		creds := newFakeAiandCredentialRepo()
		creds.listErr = errors.New("pool closed")
		svc := makeAiandService(t, &fakeAiandGateway{}, creds, &recordingExternalKeyRepo{})
		_, err := svc.SweepExpiringAiandCredentials(context.Background(), 72*time.Hour)
		assert.ErrorContains(t, err, "list expiring aiand credentials")
	})

	t.Run("unwired service is the gateway-unavailable sentinel", func(t *testing.T) {
		svc := makeAiandService(t, &fakeAiandGateway{}, newFakeAiandCredentialRepo(), &recordingExternalKeyRepo{})
		svc.WithAiandGateway(nil, nil, "router-host")
		_, err := svc.SweepExpiringAiandCredentials(context.Background(), 72*time.Hour)
		assert.ErrorIs(t, err, auth.ErrAiandGatewayUnavailable)
	})
}

// fakeTxRunner drives InTx through a fixed repo set and can fail a chosen
// invocation, simulating a transient gateway/DB failure inside the
// persistence transaction.
type fakeTxRunner struct {
	mu       sync.Mutex
	runs     int
	failRun  int // 1-based; 0 never fails
	failWith error
	tx       auth.Tx
}

func (f *fakeTxRunner) InTx(_ context.Context, fn func(auth.Tx) error) error {
	f.mu.Lock()
	f.runs++
	fail := f.failRun > 0 && f.runs == f.failRun
	f.mu.Unlock()
	if fail {
		return f.failWith
	}
	return fn(f.tx)
}

func (f *fakeTxRunner) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// startAndScriptToken starts a device login and scripts one successful token
// exchange, leaving the service one completion away from authorized.
func startAndScriptToken(t *testing.T, svc *auth.Service, gateway *fakeAiandGateway) {
	t.Helper()
	gateway.startResponses = []startResult{{code: auth.AiandDeviceCode{
		DeviceCode: "dc-secret-value-xyz", UserCode: "ABCD-1234", Interval: 5,
	}}}
	gateway.pollResponses = []pollResult{{token: auth.AiandTokenResponse{
		AccessToken: "sk-aiand-topsecret", RefreshToken: "rt-secret-value", ExpiresIn: 3600,
	}}}
	_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
}

// TestPollAiandDeviceLogin_FailedCompletionKeepsAttemptPollable proves the
// #116 lifecycle fix: a persistence failure after the token exchange does not
// consume the attempt — the retry completes without ErrAiandLoginPending —
// and the change notifier fires exactly once, only after the commit.
func TestPollAiandDeviceLogin_FailedCompletionKeepsAttemptPollable(t *testing.T) {
	gateway := &fakeAiandGateway{}
	creds := newFakeAiandCredentialRepo()
	external := &recordingExternalKeyRepo{}
	notifier := &recordingNotifier{}
	runner := &fakeTxRunner{
		tx:       auth.Tx{AiandCredentials: creds, ExternalAPIKeys: external},
		failRun:  1,
		failWith: errors.New("db connection reset"),
	}
	svc := makeAiandService(t, gateway, creds, external).
		WithTxRunner(runner).
		WithInstallationChangeNotifier(notifier)
	startAndScriptToken(t, svc, gateway)
	gateway.pollResponses = append(gateway.pollResponses, pollResult{token: auth.AiandTokenResponse{
		AccessToken: "sk-aiand-topsecret", RefreshToken: "rt-secret-value", ExpiresIn: 3600,
	}})

	// First poll: token exchange succeeds, the transaction fails.
	_, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.Error(t, err)

	// The attempt is still pollable: retry succeeds without the pending error.
	outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)
	assert.Equal(t, auth.AiandPollStatusAuthorized, outcome.Status,
		"the retry must complete the login, not surface ErrAiandLoginPending")

	// Notifier fired exactly once, after the commit, with the installation id.
	assert.Equal(t, []string{"inst-1"}, notifier.snapshot(),
		"exactly one invalidation after the committed completion")
	assert.Equal(t, 2, runner.runCount(), "one failed transaction, one committed")
}

// TestPollAiandDeviceLogin_TerminalOutcomesClearPendingImmediately proves
// denial and expiry consume the attempt even when a tx runner is wired:
// terminal outcomes never depend on the persistence path.
func TestPollAiandDeviceLogin_TerminalOutcomesClearPendingImmediately(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		gateway := &fakeAiandGateway{}
		creds := newFakeAiandCredentialRepo()
		external := &recordingExternalKeyRepo{}
		runner := &fakeTxRunner{tx: auth.Tx{AiandCredentials: creds, ExternalAPIKeys: external}}
		svc := makeAiandService(t, gateway, creds, external).WithTxRunner(runner)
		gateway.startResponses = []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}}
		gateway.pollResponses = []pollResult{{err: auth.ErrAiandAccessDenied}}
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusDenied, outcome.Status)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		assert.ErrorIs(t, err, auth.ErrAiandLoginPending, "denied must clear the attempt immediately")
	})
	t.Run("expired", func(t *testing.T) {
		gateway := &fakeAiandGateway{}
		creds := newFakeAiandCredentialRepo()
		external := &recordingExternalKeyRepo{}
		runner := &fakeTxRunner{tx: auth.Tx{AiandCredentials: creds, ExternalAPIKeys: external}}
		svc := makeAiandService(t, gateway, creds, external).WithTxRunner(runner)
		gateway.startResponses = []startResult{{code: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5}}}
		gateway.pollResponses = []pollResult{{err: auth.ErrAiandExpiredToken}}
		_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		outcome, err := svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		require.NoError(t, err)
		assert.Equal(t, auth.AiandPollStatusExpired, outcome.Status)
		_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
		assert.ErrorIs(t, err, auth.ErrAiandLoginPending, "expired must clear the attempt immediately")
	})
}

// TestDisconnectAiandCredential_ClearsInFlightPending proves a browser
// approval started before a disconnect cannot silently reconnect afterward.
func TestDisconnectAiandCredential_ClearsInFlightPending(t *testing.T) {
	creds := newFakeAiandCredentialRepo()
	svc := makeAiandService(t, &fakeAiandGateway{}, creds, &recordingExternalKeyRepo{})
	gateway := &fakeAiandGateway{startResponses: []startResult{{code: auth.AiandDeviceCode{
		DeviceCode: "dc-1", Interval: 5,
	}}}}
	svc.WithAiandGateway(gateway, creds, "router-host")
	_, err := svc.StartAiandDeviceLogin(context.Background(), testInstallation)
	require.NoError(t, err)

	require.NoError(t, svc.DisconnectAiandCredential(context.Background(), testInstallation))

	_, err = svc.PollAiandDeviceLogin(context.Background(), testInstallation)
	assert.ErrorIs(t, err, auth.ErrAiandLoginPending,
		"a pending approval must not survive a disconnect")
}

// captureLogHandler records each emitted line whole, message plus attrs.
type captureLogHandler struct {
	mu    sync.Mutex
	lines []string
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureLogHandler) Handle(_ context.Context, r slog.Record) error {
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

func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLogHandler) WithGroup(string) slog.Handler      { return h }

// TestPollAiandDeviceLogin_OutcomeLoggedWithoutSecrets drives every poll
// outcome through a capture handler and asserts each outcome is logged at
// debug while no device code, user code, or token material reaches any line.
func TestPollAiandDeviceLogin_OutcomeLoggedWithoutSecrets(t *testing.T) {
	type testCase struct {
		name           string
		poll           pollResult
		failTxRun      int
		expectedStatus string
	}
	cases := []testCase{
		{name: "pending", poll: pollResult{err: auth.ErrAiandAuthorizationPending}, expectedStatus: "pending"},
		{name: "slow_down", poll: pollResult{err: auth.ErrAiandSlowDown}, expectedStatus: "slow_down"},
		{name: "denied", poll: pollResult{err: auth.ErrAiandAccessDenied}, expectedStatus: "denied"},
		{name: "expired", poll: pollResult{err: auth.ErrAiandExpiredToken}, expectedStatus: "expired"},
		{name: "authorized", poll: pollResult{token: auth.AiandTokenResponse{
			AccessToken: "sk-aiand-topsecret", RefreshToken: "rt-secret-value", ExpiresIn: 3600,
		}}, expectedStatus: "authorized"},
		{name: "failed completion", poll: pollResult{token: auth.AiandTokenResponse{
			AccessToken: "sk-aiand-topsecret", RefreshToken: "rt-secret-value", ExpiresIn: 3600,
		}}, failTxRun: 1, expectedStatus: "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &captureLogHandler{}
			logger := slog.New(handler)
			ctx := observability.WithLogger(context.Background(), logger)

			gateway := &fakeAiandGateway{}
			creds := newFakeAiandCredentialRepo()
			external := &recordingExternalKeyRepo{}
			runner := &fakeTxRunner{
				tx:       auth.Tx{AiandCredentials: creds, ExternalAPIKeys: external},
				failRun:  tc.failTxRun,
				failWith: errors.New("db connection reset"),
			}
			svc := makeAiandService(t, gateway, creds, external).WithTxRunner(runner)
			startAndScriptToken(t, svc, gateway)
			gateway.pollResponses = []pollResult{tc.poll}

			_, _ = svc.PollAiandDeviceLogin(ctx, testInstallation)

			require.NotEmpty(t, handler.lines, "every poll outcome must be logged")
			found := false
			for _, line := range handler.lines {
				assert.Contains(t, line, "aiand: device login poll outcome")
				assert.Contains(t, line, "poll_status="+tc.expectedStatus)
				for _, secret := range []string{"dc-secret-value-xyz", "ABCD-1234", "sk-aiand-topsecret", "rt-secret-value", "secret"} {
					assert.NotContains(t, line, secret, "no device code, user code, or token material may reach the logs")
				}
				assert.NotContains(t, line, "device_code")
				assert.NotContains(t, line, "user_code")
				assert.NotContains(t, line, "access_token")
				found = true
			}
			assert.True(t, found)
		})
	}
}
