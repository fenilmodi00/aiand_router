package auth_test

// T14 (#124) matrix row 3: paste-after-disconnect regression. The disconnect
// path the admin handlers run (Service.DisconnectAiandCredential) removes the
// credential row and soft-deletes the propagated aiand external key; a fresh
// LoginWithKey must succeed afterwards and re-provision BOTH writes against
// the SAME installation — no repaste-brick, no second installation.

import (
	"context"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoginWithKey_ReprovisionsAfterDisconnect drives the full round trip:
// login → disconnect (the exact service call DisconnectAiandAuthHandler
// makes) → login again. The second login re-upserts the credential and
// re-propagates the external key for the same installation UUID.
func TestLoginWithKey_ReprovisionsAfterDisconnect(t *testing.T) {
	f := makeLoginService(t, func(f *loginFixture) {
		f.gateway.validateUser = auth.AiandUser{ID: "user-paste", Email: "paste@example.com"}
	})

	// First login provisions credential + external key.
	first, err := f.svc.LoginWithKey(context.Background(), "sk-paste-first")
	require.NoError(t, err)
	require.Len(t, f.credentials.upserts, 1)
	require.Len(t, f.externalKeys.created, 1)
	installationID := first.Account.InstallationID
	accountID := first.Account.ID
	require.NotEmpty(t, installationID)
	require.NotEqual(t, accountID, installationID,
		"the installation FK is a distinct row id, never the acct_ external id")

	// Snapshot the installation inventory: the disconnect must not spawn a
	// replacement installation, and the re-login must not either.
	installsBefore, err := f.installations.ListForExternalID(context.Background(), accountID)
	require.NoError(t, err)
	require.Len(t, installsBefore, 1)

	// The admin disconnect path (aiand_auth.go DisconnectAiandAuthHandler →
	// Service.DisconnectAiandCredential on the account's installation).
	svc := auth.NewService(nil, nil, f.externalKeys, nil,
		auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) },
	).WithAiandGateway(f.gateway, f.credentials, "test-host").
		WithEncryptor(auth.NoOpEncryptor{})
	err = svc.DisconnectAiandCredential(context.Background(), installsBefore[0])
	require.NoError(t, err)
	_, err = f.credentials.GetByInstallation(context.Background(), installationID)
	assert.ErrorIs(t, err, auth.ErrAiandCredentialNotFound, "disconnect must clear the credential row")

	// Second login: succeeds and re-provisions BOTH writes against the SAME
	// installation, with a fresh session.
	f.gateway.validateUser = auth.AiandUser{ID: "user-paste", Email: "paste@example.com"}
	second, err := f.svc.LoginWithKey(context.Background(), "sk-paste-second")
	require.NoError(t, err, "a login after disconnect must succeed, not brick the dashboard")

	require.Len(t, f.credentials.upserts, 2)
	reCred := f.credentials.upserts[1]
	assert.Equal(t, installationID, reCred.InstallationID,
		"the re-provisioned credential belongs to the same installation UUID")
	assert.Equal(t, "sk-paste-second", string(reCred.AccessTokenCiphertext))
	assert.Equal(t, auth.AiandOriginPaste, reCred.Origin)
	assert.Equal(t, "paste@example.com", reCred.UserEmail)

	require.Len(t, f.externalKeys.created, 2)
	reKey := f.externalKeys.created[1]
	assert.Equal(t, installationID, reKey.InstallationID,
		"the re-propagated external key belongs to the same installation UUID")
	assert.Equal(t, providers.ProviderAIAND, reKey.Provider)
	assert.Equal(t, "sk-paste-second", string(reKey.KeyCiphertext))

	installsAfter, err := f.installations.ListForExternalID(context.Background(), accountID)
	require.NoError(t, err)
	require.Len(t, installsAfter, 1, "no second installation may be created")

	// The round trip never regressed the session mint: the second grant is a
	// live, distinct session on the same account.
	assert.NotEqual(t, first.Token, second.Token)
	assert.Equal(t, accountID, second.Account.ID)
	active, err := f.svc.VerifySessionToken(second.Token)
	require.NoError(t, err)
	assert.Equal(t, accountID, active.Session.AccountID)
	assert.Equal(t, installationID, second.Account.InstallationID,
		"the second session grant carries the same installation")
	assert.WithinDuration(t, time.Unix(0, 0).Add(auth.LoginSessionTTL), active.Session.ExpiresAt, time.Second)
}

// T14 (#124) matrix row 7: same aiand user, two different org ids → two
// accounts, two installations, and a clean partition of every downstream row
// (credential, external key, session) by installation. No org boundary may
// collapse into shared rows.
func TestLoginWithKey_SameUserTwoOrgsPartitionCleanly(t *testing.T) {
	f := makeLoginService(t, nil)

	// Org A login: the device token response carries org-a; the identity
	// pair is (user-shared, org-a). The paste path carries no org, so the
	// org axis is exercised through the device flow.
	f.gateway.validateUser = auth.AiandUser{ID: "user-shared", Email: "shared@example.com"}
	f.gateway.pollToken = auth.AiandTokenResponse{
		AccessToken: "sk-org-a", RefreshToken: "rt-a", ExpiresIn: 3600,
		Org: &auth.AiandOrg{ID: "org-a", Name: "Org A"},
	}
	startA := startDevice(t, f)
	grantA := pollAuthorized(t, f, startA.LoginID).Session

	// Org B login: same aiand user id, different org.
	f.gateway.pollToken = auth.AiandTokenResponse{
		AccessToken: "sk-org-b", RefreshToken: "rt-b", ExpiresIn: 3600,
		Org: &auth.AiandOrg{ID: "org-b", Name: "Org B"},
	}
	startB := startDevice(t, f)
	grantB := pollAuthorized(t, f, startB.LoginID).Session

	// Two distinct accounts, two distinct installations.
	accountA, err := f.accounts.GetAccountByIdentity(context.Background(), "user-shared", "org-a")
	require.NoError(t, err)
	accountB, err := f.accounts.GetAccountByIdentity(context.Background(), "user-shared", "org-b")
	require.NoError(t, err)
	require.NotEqual(t, accountA.ID, accountB.ID, "same user, different org → different accounts")
	installA := accountA.InstallationID
	installB := accountB.InstallationID
	require.NotEqual(t, installA, installB, "each account owns its own installation")
	require.NotEqual(t, accountA.ID, installA, "installation FK is never the acct_ external id")
	require.NotEqual(t, accountB.ID, installB, "installation FK is never the acct_ external id")

	// Two installations exist, keyed by their own external ids.
	require.Len(t, f.installations.byExternal[accountA.ID], 1)
	require.Len(t, f.installations.byExternal[accountB.ID], 1)

	// Credentials partition by installation.
	require.Len(t, f.credentials.upserts, 2)
	assert.Equal(t, installA, f.credentials.upserts[0].InstallationID)
	assert.Equal(t, installB, f.credentials.upserts[1].InstallationID)
	assert.Equal(t, "sk-org-a", string(f.credentials.upserts[0].AccessTokenCiphertext))
	assert.Equal(t, "sk-org-b", string(f.credentials.upserts[1].AccessTokenCiphertext))

	// External keys partition by installation.
	require.Len(t, f.externalKeys.created, 2)
	assert.Equal(t, installA, f.externalKeys.created[0].InstallationID)
	assert.Equal(t, installB, f.externalKeys.created[1].InstallationID)
	assert.Equal(t, "sk-org-a", string(f.externalKeys.created[0].KeyCiphertext))
	assert.Equal(t, "sk-org-b", string(f.externalKeys.created[1].KeyCiphertext))

	// Sessions partition by account (and therefore by installation).
	require.Len(t, f.sessions.inserted, 2)
	assert.Equal(t, accountA.ID, f.sessions.inserted[0].AccountID)
	assert.Equal(t, accountB.ID, f.sessions.inserted[1].AccountID)
	assert.NotEqual(t, grantA.Token, grantB.Token)

	// Zero shared rows: every recorded write lands in exactly one partition.
	seen := map[string]string{}
	for _, row := range f.installations.byExternal {
		for _, inst := range row {
			require.NotContains(t, seen, inst.ID, "each installation row must be unique")
			seen[inst.ID] = "installation"
		}
	}
	for _, c := range f.credentials.upserts {
		require.NotEqual(t, seen[c.InstallationID], "credential",
			"no installation may hold two credential rows")
		seen[c.InstallationID] = "credential"
	}
	for _, k := range f.externalKeys.created {
		require.NotEqual(t, seen[k.InstallationID], "external-key",
			"no installation may hold two propagated keys")
		seen[k.InstallationID] = "external-key"
	}
	assert.Len(t, seen, 2, "exactly two installations, one credential + one key each")
}
