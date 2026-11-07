// Command account_auth_lifecycle_check exercises the SaaS account schema
// (migration 0143) end to end against a live Postgres: concurrent
// same-identity account upserts converge to one account, session
// insert/verify/touch/revoke/expire behavior, and login-attempt
// insert/consume/expire/GC. It is a separate main package (not a _test.go),
// gated on ROUTER_TEST_DATABASE_URL, and a no-op without it. Every row it
// creates is deleted before exit.
//
// Usage (from the repo root, against the docker-compose Postgres, with
// migration 0143 applied):
//
//	ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5433/router?search_path=router" \
//	    go run ./scripts/account_auth_lifecycle_check
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres"
	"aiand-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	created, cleanupCreated := (*testAccount)(nil), func() {}
	defer cleanupCreated()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Println("ROUTER_TEST_DATABASE_URL not set; skipping live-DB account auth lifecycle check (see file header for usage)")
		return
	}
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fail("parse database url", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fail("connect to database", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fail("ping database", err)
	}

	q := sqlc.New(pool)
	accounts := postgres.NewAccountRepo(pool)
	sessions := postgres.NewAccountSessionRepo(pool)
	attempts := postgres.NewLoginAttemptRepo(pool)

	fmt.Println("=== 1. Concurrent same-identity account upserts (2 goroutines) ===")
	upserted, err := checkConcurrentUpserts(ctx, q, accounts)
	if err != nil {
		fail("concurrent upserts", err)
	}
	created = upserted
	cleanupCreated = func() { cleanup(pool, created) }

	fmt.Println("\n=== 2. Browser + device flows ===")
	if err := checkBrowserLogin(ctx, pool, created); err != nil {
		fail("browser flow", err)
	}
	if err := checkDeviceFlow(ctx, pool, created); err != nil {
		fail("device flow", err)
	}

	fmt.Println("\n=== 3. Transactional re-login reuses the existing account ===")
	if err := checkTransactionalRelogin(ctx, postgres.NewTxRunner(pool, auth.NoOpEncryptor{}), created); err != nil {
		fail("transactional re-login", err)
	}

	fmt.Println("\n=== 4. Session lifecycle (insert / verify / touch / revoke / expire) ===")
	if err := checkSessionLifecycle(ctx, sessions, created); err != nil {
		fail("session lifecycle", err)
	}

	fmt.Println("\n=== 5. Login attempt lifecycle (insert / consume / expire / GC) ===")
	if err := checkAttemptLifecycle(ctx, attempts, created); err != nil {
		fail("attempt lifecycle", err)
	}

	fmt.Println("\n=== 6. Browser authorization-code flow (start → exchange → session) ===")
	if err := checkBrowserFlow(ctx, pool, created); err != nil {
		fail("browser flow", err)
	}

	fmt.Println("\naccount auth lifecycle check: all assertions passed")
}

// testAccount is one identity + the installation(s) and session(s) the check
// created, so cleanup can remove every row in FK order.
type testAccount struct {
	identityUserID  string
	identityOrgID   string
	accountIDs      []string
	installationIDs []string
	tokenHashes     []string
	attemptIDs      []string
}

// checkConcurrentUpserts races two goroutines through create-installation →
// UpsertAccountOnLogin for the same (user, org) identity and asserts they
// converge: exactly one live account, referencing exactly one installation.
func checkConcurrentUpserts(ctx context.Context, q *sqlc.Queries, accounts auth.AccountRepository) (*testAccount, error) {
	t := &testAccount{
		identityUserID: "lifecycle-check-user-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:32],
		identityOrgID:  "lifecycle-check-org-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:32],
	}

	type result struct {
		account      *auth.Account
		installation string
		err          error
	}
	results := make(chan result, 4) // two sends per goroutine (installation, account)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, err := q.CreateModelRouterInstallation(ctx, sqlc.CreateModelRouterInstallationParams{
				// model_router_installations.external_id is VARCHAR(36);
				// production ids are GenerateID("ekid") (29 chars).
				ExternalID: "lcchk-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:23],
				Name:       "lifecycle-check installation",
			})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{installation: inst.ID.String()}
			account, err := accounts.UpsertAccountOnLogin(ctx, auth.UpsertAccountOnLoginParams{
				AiandUserID:    t.identityUserID,
				AiandOrgID:     t.identityOrgID,
				Email:          "lifecycle-check@example.com",
				DisplayName:    "Lifecycle Check",
				InstallationID: inst.ID.String(),
			})
			if err != nil {
				results <- result{err: fmt.Errorf("upsert after installation %s: %w", inst.ID, err)}
				return
			}
			results <- result{account: account}
		}()
	}
	wg.Wait()
	close(results)

	winner := ""
	for r := range results {
		if r.err != nil {
			return t, r.err
		}
		if r.installation != "" {
			t.installationIDs = append(t.installationIDs, r.installation)
			continue
		}
		t.accountIDs = append(t.accountIDs, r.account.ID)
		if winner == "" {
			winner = r.account.ID
		} else if winner != r.account.ID {
			return t, fmt.Errorf("race produced two accounts: %s and %s", winner, r.account.ID)
		}
	}

	row, err := q.GetAccountByIdentity(ctx, sqlc.GetAccountByIdentityParams{
		AiandUserID: t.identityUserID,
		AiandOrgID:  t.identityOrgID,
	})
	if err != nil {
		return t, fmt.Errorf("re-read by identity after race: %w", err)
	}
	t.installationIDs = append(t.installationIDs, row.InstallationID.String())
	fmt.Printf("converged: 1 account (%s), 1 installation (%s)\n", row.ID, row.InstallationID)

	// The identity lookup and the ID lookup must agree.
	byID, err := accounts.GetAccountByID(ctx, row.ID)
	if err != nil {
		return t, err
	}
	if byID.InstallationID != row.InstallationID.String() || byID.AiandUserID != row.AiandUserID {
		return t, errors.New("GetAccountByID and GetAccountByIdentity disagree")
	}

	return t, nil
}

// deviceGateway scripts one device authorization: the first poll is pending,
// the second is authorized with an org-bearing token. It implements the full
// auth.AiandLoginGateway surface the login service depends on.
type deviceGateway struct {
	userID string
	orgID  string
	email  string
	polls  int
}

func (g *deviceGateway) DeviceCodeStart(context.Context, string) (auth.AiandDeviceCode, error) {
	return auth.AiandDeviceCode{
		DeviceCode:              "dc-lifecycle-check",
		UserCode:                "LIFE-CYC",
		VerificationURI:         "https://example.invalid/device",
		VerificationURIComplete: "https://example.invalid/device?user_code=LIFE-CYC",
		ExpiresIn:               900,
		Interval:                5,
	}, nil
}

func (g *deviceGateway) PollToken(context.Context, string) (auth.AiandTokenResponse, error) {
	g.polls++
	if g.polls == 1 {
		return auth.AiandTokenResponse{}, auth.ErrAiandAuthorizationPending
	}
	return auth.AiandTokenResponse{
		AccessToken:  "sk-lifecycle-device",
		RefreshToken: "rt-lifecycle-device",
		ExpiresIn:    3600,
		Org:          &auth.AiandOrg{ID: g.orgID, Name: "Lifecycle Device Org"},
	}, nil
}

func (g *deviceGateway) RefreshToken(context.Context, string) (auth.AiandTokenResponse, error) {
	return auth.AiandTokenResponse{}, errors.New("not used")
}

func (g *deviceGateway) Revoke(context.Context, string) error { return nil }

func (g *deviceGateway) ValidateKey(context.Context, string) (auth.AiandUser, error) {
	return auth.AiandUser{ID: g.userID, Email: g.email}, nil
}

func (g *deviceGateway) VerifyKey(context.Context, string) (auth.AiandIdentity, error) {
	return auth.AiandIdentity{UserID: g.userID, OrgID: g.orgID, Email: g.email}, nil
}

func (g *deviceGateway) AuthorizeURL(params auth.AiandAuthorizeParams) string {
	return "https://example.invalid/auth/authorize?state=" + params.State
}
func (g *deviceGateway) BrowserAuthorizeAvailable(context.Context, string) error { return nil }
func (g *deviceGateway) ExchangeCode(context.Context, auth.AiandCodeExchangeParams) (auth.AiandTokenResponse, error) {
	return auth.AiandTokenResponse{
		AccessToken:  "sk-lifecycle-browser",
		RefreshToken: "rt-lifecycle-browser",
		ExpiresIn:    3600,
		Org:          &auth.AiandOrg{ID: g.orgID, Name: "Lifecycle Browser Org"},
	}, nil
}

type noopNotifier struct{}

func (noopNotifier) NotifyInstallationChanged(string) {}

// checkDeviceFlow drives the real LoginService against live Postgres with a
// scripted gateway: start seals an attempt, the first poll is pending, the
// authorized poll runs the whole login transaction, and the minted session
// resolves to the account/installation with a device credential and one
// propagated external key.
func checkDeviceFlow(ctx context.Context, pool *pgxpool.Pool, t *testAccount) error {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	gw := &deviceGateway{
		userID: "lcchk-device-user-" + suffix,
		orgID:  "lcchk-device-org-" + suffix,
		email:  "lifecycle-device@example.com",
	}
	repo := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	svc, err := auth.NewLoginService(
		repo.Accounts, repo.Sessions, repo.LoginAttempts, repo.Installations,
		repo.AiandCredentials, repo.ExternalAPIKeys, gw,
		auth.NoOpEncryptor{},
		postgres.NewTxRunner(pool, auth.NoOpEncryptor{}),
		time.Now, noopNotifier{},
	)
	if err != nil {
		return err
	}

	start, err := svc.StartDeviceLogin(ctx)
	if err != nil {
		return fmt.Errorf("start device login: %w", err)
	}
	pending, err := svc.PollDeviceLogin(ctx, start.LoginID)
	if err != nil {
		return fmt.Errorf("pending poll: %w", err)
	}
	if pending.Status != auth.AiandPollStatusPending {
		return fmt.Errorf("first poll status = %s, want pending", pending.Status)
	}
	outcome, err := svc.PollDeviceLogin(ctx, start.LoginID)
	if err != nil {
		return fmt.Errorf("authorized poll: %w", err)
	}
	if outcome.Status != auth.AiandPollStatusAuthorized || outcome.Session == nil {
		return fmt.Errorf("second poll status = %s, want authorized with a session", outcome.Status)
	}
	active, err := svc.VerifySessionToken(outcome.Session.Token)
	if err != nil {
		return fmt.Errorf("verify minted session: %w", err)
	}
	if active.Installation == nil {
		return errors.New("minted session has no installation")
	}
	credential, err := repo.AiandCredentials.GetByInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read device credential: %w", err)
	}
	if credential.Origin != auth.AiandOriginDevice || len(credential.RefreshTokenCiphertext) == 0 {
		return fmt.Errorf("credential origin=%s refresh_len=%d, want device + refresh", credential.Origin, len(credential.RefreshTokenCiphertext))
	}
	keys, err := repo.ExternalAPIKeys.GetForInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read propagated external keys: %w", err)
	}
	if len(keys) != 1 || keys[0].Provider != "aiand" {
		return fmt.Errorf("propagated keys = %d, want exactly one aiand key", len(keys))
	}
	if active.Account.AiandOrgID != gw.orgID {
		return fmt.Errorf("account org = %q, want gateway org %q", active.Account.AiandOrgID, gw.orgID)
	}

	t.accountIDs = append(t.accountIDs, active.Account.ID)
	t.installationIDs = append(t.installationIDs, active.Installation.ID)
	t.tokenHashes = append(t.tokenHashes, auth.HashAPIKeySHA256(outcome.Session.Token))
	t.attemptIDs = append(t.attemptIDs, start.LoginID)
	fmt.Println("device flow: pending → authorized, session/account/credential/external key created")
	return nil
}

// checkBrowserLogin drives the browser authorization-code flow through the
// real LoginService against live Postgres with a scripted gateway: start mints
// PKCE/state, the exchange runs the same login transaction, and the minted
// session carries a browser credential and one propagated external key.
func checkBrowserLogin(ctx context.Context, pool *pgxpool.Pool, t *testAccount) error {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	gw := &deviceGateway{
		userID: "lcchk-browser-user-" + suffix,
		orgID:  "lcchk-browser-org-" + suffix,
		email:  "lifecycle-browser@example.com",
	}
	repo := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	svc, err := auth.NewLoginService(
		repo.Accounts, repo.Sessions, repo.LoginAttempts, repo.Installations,
		repo.AiandCredentials, repo.ExternalAPIKeys, gw,
		auth.NoOpEncryptor{},
		postgres.NewTxRunner(pool, auth.NoOpEncryptor{}),
		time.Now, noopNotifier{},
	)
	if err != nil {
		return err
	}

	start, err := svc.StartBrowserLogin(ctx, "http://127.0.0.1:3000", "/dashboard")
	if err != nil {
		return fmt.Errorf("start browser login: %w", err)
	}
	authorize, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		return fmt.Errorf("parse browser authorize URL: %w", err)
	}
	state := authorize.Query().Get("state")
	if state == "" {
		return errors.New("browser authorize URL has no state")
	}
	result, err := svc.ExchangeBrowserLogin(ctx, state, "lifecycle-code", start.Sealed)
	if err != nil {
		return fmt.Errorf("exchange browser login: %w", err)
	}
	if result.Grant == nil {
		return errors.New("browser exchange returned no session grant")
	}
	active, err := svc.VerifySessionToken(result.Grant.Token)
	if err != nil {
		return fmt.Errorf("verify browser session: %w", err)
	}
	if active.Installation == nil {
		return errors.New("browser session has no installation")
	}
	credential, err := repo.AiandCredentials.GetByInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read browser credential: %w", err)
	}
	if len(credential.RefreshTokenCiphertext) == 0 {
		return errors.New("browser credential has no refresh token")
	}
	keys, err := repo.ExternalAPIKeys.GetForInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read browser external keys: %w", err)
	}
	if len(keys) != 1 || keys[0].Provider != "aiand" {
		return fmt.Errorf("browser propagated keys = %d, want exactly one aiand key", len(keys))
	}
	if active.Account.AiandOrgID != gw.orgID {
		return fmt.Errorf("browser account org = %q, want gateway org %q", active.Account.AiandOrgID, gw.orgID)
	}
	if result.Next != "/dashboard" {
		return fmt.Errorf("browser next = %q, want /dashboard", result.Next)
	}

	t.accountIDs = append(t.accountIDs, active.Account.ID)
	t.installationIDs = append(t.installationIDs, active.Installation.ID)
	t.tokenHashes = append(t.tokenHashes, auth.HashAPIKeySHA256(result.Grant.Token))
	fmt.Println("browser flow: PKCE start → code exchange, session/account/credential/external key created")
	return nil
}

// checkTransactionalRelogin reproduces the real login unit of work's second
// login: create an installation, upsert the identity, commit; then do it again
// in a fresh transaction. Before the fix the second insert hit the partial
// unique index inside the transaction, poisoning it (25P02), so the follow-up
// identity re-read failed and re-login returned 500. The check asserts the
// second transaction reuses the first account instead.
func checkTransactionalRelogin(ctx context.Context, runner *postgres.TxRunner, t *testAccount) error {
	newExternalID := func() string {
		return "lcchk-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:23]
	}
	userID := "lcchk-tx-user-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	orgID := "lcchk-tx-org-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]

	var firstAccountID, firstInstallationID string
	firstExternalID := newExternalID()
	if err := runner.InTx(ctx, func(tx auth.Tx) error {
		inst, err := tx.Installations.Create(ctx, auth.CreateInstallationParams{
			ExternalID: firstExternalID,
			Name:       "lifecycle tx check",
		})
		if err != nil {
			return err
		}
		acct, err := tx.Accounts.UpsertAccountOnLogin(ctx, auth.UpsertAccountOnLoginParams{
			AiandUserID:    userID,
			AiandOrgID:     orgID,
			Email:          "lifecycle-tx@example.com",
			InstallationID: inst.ID,
		})
		if err != nil {
			return err
		}
		firstAccountID, firstInstallationID = acct.ID, inst.ID
		return nil
	}); err != nil {
		return fmt.Errorf("first login in tx: %w", err)
	}

	var secondAccountID, secondInstallationID string
	secondExternalID := newExternalID()
	if err := runner.InTx(ctx, func(tx auth.Tx) error {
		inst, err := tx.Installations.Create(ctx, auth.CreateInstallationParams{
			ExternalID: secondExternalID,
			Name:       "lifecycle tx check",
		})
		if err != nil {
			return err
		}
		acct, err := tx.Accounts.UpsertAccountOnLogin(ctx, auth.UpsertAccountOnLoginParams{
			AiandUserID:    userID,
			AiandOrgID:     orgID,
			Email:          "lifecycle-tx@example.com",
			InstallationID: inst.ID,
		})
		if err != nil {
			return fmt.Errorf("re-login upsert in tx: %w", err)
		}
		secondAccountID, secondInstallationID = acct.ID, inst.ID
		// LoginService.ensureAccount retires the installation minted for the
		// fresh account id when the existing account wins; mirror it.
		return tx.Installations.SoftDelete(ctx, secondExternalID, inst.ID)
	}); err != nil {
		return err
	}
	if secondAccountID != firstAccountID {
		return fmt.Errorf("re-login created account %s, want existing %s", secondAccountID, firstAccountID)
	}

	t.accountIDs = append(t.accountIDs, firstAccountID)
	t.installationIDs = append(t.installationIDs, firstInstallationID, secondInstallationID)
	fmt.Println("transactional re-login reused the existing account")
	return nil
}

func checkSessionLifecycle(ctx context.Context, sessions auth.SessionRepository, t *testAccount) error {
	accountID := t.accountIDs[0]

	// Expired session inserted on purpose: verify must refuse it.
	expiredToken := newToken(t)
	if _, err := sessions.InsertAccountSession(ctx, auth.InsertSessionParams{
		AccountID:   accountID,
		TokenHash:   expiredToken.hash,
		TokenPrefix: expiredToken.prefix,
		ExpiresAt:   time.Now().Add(-time.Minute),
	}); err != nil {
		return err
	}
	if _, err := sessions.GetActiveSessionByTokenHash(ctx, auth.GetActiveSessionParams{TokenHash: expiredToken.hash}); !errors.Is(err, auth.ErrSessionNotFound) {
		return fmt.Errorf("expired session verify: want ErrSessionNotFound, got %v", err)
	}
	fmt.Println("expired session refused")

	// Live session: verify returns session + account + installation in one row.
	liveToken := newToken(t)
	expires := time.Now().Add(time.Hour)
	inserted, err := sessions.InsertAccountSession(ctx, auth.InsertSessionParams{
		AccountID:   accountID,
		TokenHash:   liveToken.hash,
		TokenPrefix: liveToken.prefix,
		ExpiresAt:   expires,
	})
	if err != nil {
		return err
	}
	active, err := sessions.GetActiveSessionByTokenHash(ctx, auth.GetActiveSessionParams{TokenHash: liveToken.hash})
	if err != nil {
		return fmt.Errorf("live session verify: %w", err)
	}
	if active.Session.ID != inserted.ID || active.Account.ID != accountID || active.Installation == nil || active.Installation.ID == "" {
		return errors.New("active session row is missing session/account/installation wiring")
	}

	// Touch slides the window forward.
	newExpiry := time.Now().Add(2 * time.Hour)
	if err := sessions.TouchSessionLastSeen(ctx, auth.TouchSessionParams{TokenHash: liveToken.hash, ExpiresAt: newExpiry}); err != nil {
		return fmt.Errorf("touch: %w", err)
	}
	active, err = sessions.GetActiveSessionByTokenHash(ctx, auth.GetActiveSessionParams{TokenHash: liveToken.hash})
	if err != nil {
		return err
	}
	if active.Session.ExpiresAt.Before(time.Now().Add(time.Hour)) {
		return fmt.Errorf("touch did not extend expiry: %v", active.Session.ExpiresAt)
	}
	fmt.Println("live session verified and touched (sliding expiry)")

	// Unknown hash refuses.
	if _, err := sessions.GetActiveSessionByTokenHash(ctx, auth.GetActiveSessionParams{TokenHash: newToken(t).hash}); !errors.Is(err, auth.ErrSessionNotFound) {
		return fmt.Errorf("unknown session verify: want ErrSessionNotFound, got %v", err)
	}

	// Revoke: single session disappears, and revoking again is a no-op.
	if err := sessions.RevokeSessionByTokenHash(ctx, liveToken.hash); err != nil {
		return err
	}
	if err := sessions.RevokeSessionByTokenHash(ctx, liveToken.hash); err != nil {
		return err
	}
	if _, err := sessions.GetActiveSessionByTokenHash(ctx, auth.GetActiveSessionParams{TokenHash: liveToken.hash}); !errors.Is(err, auth.ErrSessionNotFound) {
		return fmt.Errorf("revoked session verify: want ErrSessionNotFound, got %v", err)
	}
	fmt.Println("revoke (idempotent) enforced")

	// RevokeAllSessionsForAccount on a fresh session pair.
	for range 2 {
		tok := newToken(t)
		if _, err := sessions.InsertAccountSession(ctx, auth.InsertSessionParams{
			AccountID:   accountID,
			TokenHash:   tok.hash,
			TokenPrefix: tok.prefix,
			ExpiresAt:   time.Now().Add(time.Hour),
		}); err != nil {
			return err
		}
	}
	revoked, err := sessions.RevokeAllSessionsForAccount(ctx, accountID)
	if err != nil {
		return err
	}
	// RevokeAllSessionsForAccount on a fresh session pair. The expired-but
	// never-revoked session from the first assertion also counts as live
	// (the query only filters revoked_at), so expect >= 2.
	if revoked < 2 {
		return fmt.Errorf("RevokeAllSessionsForAccount: want at least 2 revoked, got %d", revoked)
	}
	fmt.Println("revoke-all revoked both live sessions")
	return nil
}

func checkAttemptLifecycle(ctx context.Context, attempts auth.LoginAttemptRepository, t *testAccount) error {
	// Live attempt.
	inserted, err := attempts.InsertLoginAttempt(ctx, auth.InsertLoginAttemptParams{
		DeviceCodeCiphertext: []byte{0x01, 0x02, 0x03},
		Interval:             5 * time.Second,
		ExpiresAt:            time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		return err
	}
	t.attemptIDs = append(t.attemptIDs, inserted.ID)
	got, err := attempts.GetActiveLoginAttempt(ctx, inserted.ID)
	if err != nil {
		return fmt.Errorf("active attempt lookup: %w", err)
	}
	if got.ID != inserted.ID || got.Interval != 5*time.Second || len(got.DeviceCode) != 3 {
		return errors.New("active attempt round-trip mismatch")
	}
	if err := attempts.MarkLoginAttemptConsumed(ctx, inserted.ID); err != nil {
		return err
	}
	// Consuming twice is a no-op; consumed attempts stop resolving.
	if err := attempts.MarkLoginAttemptConsumed(ctx, inserted.ID); err != nil {
		return err
	}
	if _, err := attempts.GetActiveLoginAttempt(ctx, inserted.ID); !errors.Is(err, auth.ErrLoginAttemptNotFound) {
		return fmt.Errorf("consumed attempt lookup: want ErrLoginAttemptNotFound, got %v", err)
	}
	fmt.Println("attempt inserted, consumed once, no longer active")

	// Unknown attempt id.
	if _, err := attempts.GetActiveLoginAttempt(ctx, uuid.NewString()); !errors.Is(err, auth.ErrLoginAttemptNotFound) {
		return fmt.Errorf("unknown attempt lookup: want ErrLoginAttemptNotFound, got %v", err)
	}

	// Expired attempt: invisible to polls, swept by GC.
	expired, err := attempts.InsertLoginAttempt(ctx, auth.InsertLoginAttemptParams{
		DeviceCodeCiphertext: []byte{0x04},
		Interval:             5 * time.Second,
		ExpiresAt:            time.Now().Add(-time.Minute),
	})
	if err != nil {
		return err
	}
	t.attemptIDs = append(t.attemptIDs, expired.ID)
	if _, err := attempts.GetActiveLoginAttempt(ctx, expired.ID); !errors.Is(err, auth.ErrLoginAttemptNotFound) {
		return fmt.Errorf("expired attempt lookup: want ErrLoginAttemptNotFound, got %v", err)
	}
	deleted, err := attempts.DeleteExpiredLoginAttempts(ctx, time.Now())
	if err != nil {
		return err
	}
	if deleted < 1 {
		return fmt.Errorf("GC: want at least the expired attempt deleted, got %d", deleted)
	}
	fmt.Printf("expired attempt refused by polls; GC removed %d row(s)\n", deleted)

	// Clear-on-disconnect delete of the consumed row.
	if err := attempts.DeleteLoginAttempt(ctx, inserted.ID); err != nil {
		return err
	}
	if err := attempts.DeleteLoginAttempt(ctx, inserted.ID); err != nil {
		return err
	}
	fmt.Println("delete (idempotent) enforced")
	return nil
}

// browserScriptGateway extends deviceGateway with the browser surface: the
// authorize URL embeds the caller's redirect URI and state, the preflight
// always succeeds, and the first exchange returns the org-bearing token while
// a second exchange of the same code is rejected (one-time codes).
type browserScriptGateway struct {
	deviceGateway
	authorizeCalls int
	exchangeCalls  int
}

func (g *browserScriptGateway) AuthorizeURL(params auth.AiandAuthorizeParams) string {
	g.authorizeCalls++
	return "https://aiand.example/auth/authorize?client_id=aiand-cli&response_type=code&redirect_uri=" +
		url.QueryEscape(params.RedirectURI) + "&state=" + url.QueryEscape(params.State) +
		"&code_challenge=" + url.QueryEscape(params.CodeChallenge) +
		"&code_challenge_method=S256&key_name=" + url.QueryEscape(params.KeyName)
}

func (g *browserScriptGateway) BrowserAuthorizeAvailable(_ context.Context, authorizeURL string) error {
	if authorizeURL == "" {
		return errors.New("empty authorize URL")
	}
	return nil
}

func (g *browserScriptGateway) ExchangeCode(_ context.Context, params auth.AiandCodeExchangeParams) (auth.AiandTokenResponse, error) {
	g.exchangeCalls++
	if g.exchangeCalls > 1 {
		// One-time codes: a replayed code is an invalid grant.
		return auth.AiandTokenResponse{}, auth.ErrAiandInvalidGrant
	}
	if params.Code == "" || params.CodeVerifier == "" || params.RedirectURI == "" {
		return auth.AiandTokenResponse{}, errors.New("exchange params incomplete")
	}
	return auth.AiandTokenResponse{
		AccessToken:  "sk-lifecycle-browser",
		RefreshToken: "rt-lifecycle-browser",
		ExpiresIn:    3600,
		Org:          &auth.AiandOrg{ID: g.orgID, Name: "Lifecycle Browser Org"},
	}, nil
}

// checkBrowserFlow drives the real LoginService browser path against live
// Postgres: start (seals the flow, preflights the authorize URL), exchange
// (runs the same login transaction as the device flow), then a replayed
// exchange that must be refused. The minted session must resolve to the
// account/installation with a browser-org credential and one propagated
// external aiand key.
func checkBrowserFlow(ctx context.Context, pool *pgxpool.Pool, t *testAccount) error {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	gw := &browserScriptGateway{deviceGateway: deviceGateway{
		userID: "lcchk-browser-user-" + suffix,
		orgID:  "lcchk-browser-org-" + suffix,
		email:  "lifecycle-browser@example.com",
	}}
	repo := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	svc, err := auth.NewLoginService(
		repo.Accounts, repo.Sessions, repo.LoginAttempts, repo.Installations,
		repo.AiandCredentials, repo.ExternalAPIKeys, gw,
		auth.NoOpEncryptor{},
		postgres.NewTxRunner(pool, auth.NoOpEncryptor{}),
		time.Now, noopNotifier{},
	)
	if err != nil {
		return err
	}

	const origin = "http://127.0.0.1:3000"
	start, err := svc.StartBrowserLogin(ctx, origin, "/dashboard")
	if err != nil {
		return fmt.Errorf("start browser login: %w", err)
	}
	if start.AuthorizeURL == "" || start.ExpiresIn <= 0 || len(start.Sealed) == 0 {
		return fmt.Errorf("browser start incomplete: url=%d sealed=%d expires_in=%d",
			len(start.AuthorizeURL), len(start.Sealed), start.ExpiresIn)
	}
	var flow auth.SealedBrowserLogin
	if err := json.Unmarshal(start.Sealed, &flow); err != nil {
		return fmt.Errorf("unseal browser flow (NoOp encryptor keeps plaintext): %w", err)
	}
	if flow.State == "" || flow.Verifier == "" || flow.RedirectURI != origin || flow.Next != "/dashboard" {
		return fmt.Errorf("sealed flow mismatch: redirect=%q next=%q", flow.RedirectURI, flow.Next)
	}
	if !flow.ExpiresAt.After(time.Now().Add(9 * time.Minute)) {
		return fmt.Errorf("flow expires too early: %v", flow.ExpiresAt)
	}
	if gw.authorizeCalls != 1 {
		return fmt.Errorf("authorize URL built %d times, want 1", gw.authorizeCalls)
	}

	result, err := svc.ExchangeBrowserLogin(ctx, flow.State, "auth-code-lifecycle", start.Sealed)
	if err != nil {
		return fmt.Errorf("browser exchange: %w", err)
	}
	if result.Grant == nil || result.Email != gw.email || result.Next != "/dashboard" {
		return fmt.Errorf("browser exchange result incomplete: grant=%v email=%q", result.Grant != nil, result.Email)
	}
	active, err := svc.VerifySessionToken(result.Grant.Token)
	if err != nil {
		return fmt.Errorf("verify browser-minted session: %w", err)
	}
	if active.Installation == nil {
		return errors.New("browser-minted session has no installation")
	}
	credential, err := repo.AiandCredentials.GetByInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read browser credential: %w", err)
	}
	if credential.Origin != auth.AiandOriginDevice || len(credential.RefreshTokenCiphertext) == 0 {
		return fmt.Errorf("credential origin=%s refresh_len=%d, want device + refresh", credential.Origin, len(credential.RefreshTokenCiphertext))
	}
	if credential.OrgID != gw.orgID {
		return fmt.Errorf("credential org = %q, want gateway org %q", credential.OrgID, gw.orgID)
	}
	keys, err := repo.ExternalAPIKeys.GetForInstallation(ctx, active.Installation.ID)
	if err != nil {
		return fmt.Errorf("read propagated external keys: %w", err)
	}
	if len(keys) != 1 || keys[0].Provider != "aiand" {
		return fmt.Errorf("propagated keys = %d, want exactly one aiand key", len(keys))
	}
	if active.Account.AiandOrgID != gw.orgID {
		return fmt.Errorf("account org = %q, want gateway org %q", active.Account.AiandOrgID, gw.orgID)
	}

	t.accountIDs = append(t.accountIDs, active.Account.ID)
	t.installationIDs = append(t.installationIDs, active.Installation.ID)
	t.tokenHashes = append(t.tokenHashes, auth.HashAPIKeySHA256(result.Grant.Token))

	// A replayed exchange (same sealed blob, same code) must be refused by
	// the one-time-code gateway and must not mint a second session.
	if _, err := svc.ExchangeBrowserLogin(ctx, flow.State, "auth-code-lifecycle", start.Sealed); !errors.Is(err, auth.ErrAiandInvalidGrant) {
		return fmt.Errorf("replayed exchange: want ErrAiandInvalidGrant, got %v", err)
	}
	fmt.Println("browser flow: start → exchange authorized, session/account/credential/external key created; replay refused")
	return nil
}

// cleanup removes every row the check created, in FK order: sessions, then
// accounts, then installations, then attempts (no FK). Raw SQL on purpose:
// the check must not invent SQLC queries that production code would never
// use. Best-effort; failures are logged, not fatal.
func cleanup(pool *pgxpool.Pool, t *testAccount) {
	ctx := context.Background()
	for _, hash := range t.tokenHashes {
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.account_sessions WHERE token_hash = $1::varchar", hash); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete session %s…: %v\n", hash[:12], err)
		}
	}
	// Credential/key rows reference the installation; remove them before the
	// installation and account deletes.
	for _, id := range t.installationIDs {
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.aiand_credentials WHERE installation_id = $1::uuid", id); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete credential %s: %v\n", id, err)
		}
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.model_router_external_api_keys WHERE installation_id = $1::uuid", id); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete external key %s: %v\n", id, err)
		}
	}
	for _, id := range t.accountIDs {
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.accounts WHERE id = $1::varchar", id); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete account %s: %v\n", id, err)
		}
	}
	for _, id := range t.installationIDs {
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.model_router_installations WHERE id = $1::uuid", id); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete installation %s: %v\n", id, err)
		}
	}
	for _, id := range t.attemptIDs {
		if _, err := pool.Exec(ctx,
			"DELETE FROM router.account_login_attempts WHERE id = $1::uuid", id); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: delete attempt %s: %v\n", id, err)
		}
	}
}

type token struct {
	hash   string
	prefix string
}

// newToken mints a fresh opaque cookie token: 32 random bytes, hashed with
// SHA-256 for the row, first 12 chars as the display prefix.
func newToken(t *testAccount) token {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		fail("generate token", err)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	t.tokenHashes = append(t.tokenHashes, hash)
	return token{hash: hash, prefix: hash[:12]}
}

// fail reports and exits 1. os.Exit skips deferred cleanups, so a mid-run
// failure can leave rows behind; this is acceptable for a dev-DB check (the
// rows are inert and identified by lifecycle-check/lcchk prefixes), and the
// happy path always cleans up.
func fail(stage string, err error) {
	fmt.Fprintf(os.Stderr, "account auth lifecycle check: %s: %v\n", stage, err)
	os.Exit(1)
}
