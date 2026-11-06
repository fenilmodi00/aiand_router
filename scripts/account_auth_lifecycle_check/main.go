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
	"errors"
	"fmt"
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

	fmt.Println("\n=== 2. Session lifecycle (insert / verify / touch / revoke / expire) ===")
	if err := checkSessionLifecycle(ctx, sessions, created); err != nil {
		fail("session lifecycle", err)
	}

	fmt.Println("\n=== 3. Login attempt lifecycle (insert / consume / expire / GC) ===")
	if err := checkAttemptLifecycle(ctx, attempts, created); err != nil {
		fail("attempt lifecycle", err)
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
