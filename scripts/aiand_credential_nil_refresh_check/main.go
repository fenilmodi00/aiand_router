// Command aiand_credential_nil_refresh_check is a live-DB regression check
// for issue #111: a paste (or device-without-refresh) credential whose
// refresh ciphertext is nil must persist as empty bytea, not SQL NULL
// (refresh_token_ciphertext is BYTEA NOT NULL; NULL raised SQLSTATE 23502
// and nothing persisted). It exercises the postgres AiandCredentialRepo
// upsert and read paths directly, then cleans up its rows.
//
// Gated on ROUTER_TEST_DATABASE_URL; no-op when unset.
//
//	ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5433/router?search_path=router" \
//	    go run ./scripts/aiand_credential_nil_refresh_check
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Println("ROUTER_TEST_DATABASE_URL not set; skipping live-DB aiand nil-refresh check (see file header for usage)")
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

	encryptor := newTinkEncryptor()
	repo := postgres.NewRepository(pool, encryptor)

	// Throwaway installation; deleted (cascade) at the end.
	install, err := repo.Installations.Create(ctx, auth.CreateInstallationParams{
		ExternalID: "org_aiand_nil_refresh_" + uuid.NewString()[:8],
		Name:       "Aiand nil-refresh regression",
	})
	if err != nil {
		fail("create installation", err)
	}
	fmt.Printf("installation_id=%s\n", install.ID)
	defer deleteInstallation(ctx, repo, install)

	if err := checkNilRefreshPaste(ctx, repo, encryptor, install); err != nil {
		fail("paste-shaped nil refresh upsert", err)
	}
	if err := checkDeviceRefreshRoundTrip(ctx, repo, encryptor, install); err != nil {
		fail("device-shaped refresh round trip", err)
	}
	if err := checkListExpiring(ctx, repo, install); err != nil {
		fail("ListExpiring round trip", err)
	}
	fmt.Println("OK: nil refresh persists as empty bytea; device refresh preserved; ListExpiring returns only refresh-bearing rows")
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", step, err)
	os.Exit(1)
}

// deleteInstallation soft-deletes the throwaway installation at the end.
func deleteInstallation(ctx context.Context, repo *postgres.Repository, install *auth.Installation) {
	if err := repo.Installations.SoftDelete(ctx, install.ExternalID, install.ID); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: cleanup: soft-delete installation %s: %v\n", install.ID, err)
	}
}

// checkNilRefreshPaste covers (b): paste-shaped upsert with a nil refresh
// ciphertext must succeed and store empty bytea.
func checkNilRefreshPaste(ctx context.Context, repo *postgres.Repository, encryptor auth.Encryptor, install *auth.Installation) error {
	access, err := encryptor.Encrypt([]byte("sk-aiand-paste-access"), install.ExternalID, "aiand")
	if err != nil {
		return fmt.Errorf("seal access token: %w", err)
	}
	credential, err := repo.AiandCredentials.Upsert(ctx, auth.UpsertAiandCredentialParams{
		InstallationID:         install.ID,
		AccessTokenCiphertext:  access,
		RefreshTokenCiphertext: nil, // paste shape: the pre-fix bug fed nil to pgx → 23502
		ExpiresAt:              time.Now().Add(365 * 24 * time.Hour),
		Origin:                 auth.AiandOriginPaste,
	})
	if err != nil {
		return fmt.Errorf("upsert with nil refresh: %w", err)
	}
	stored, err := repo.AiandCredentials.GetByInstallation(ctx, install.ID)
	if err != nil {
		return fmt.Errorf("read back after nil-refresh upsert: %w", err)
	}
	if stored.RefreshTokenCiphertext == nil {
		return errors.New("stored refresh ciphertext is SQL NULL (read back as nil); expected empty bytea")
	}
	if len(stored.RefreshTokenCiphertext) != 0 {
		return fmt.Errorf("stored refresh ciphertext is %d bytes; expected empty bytea", len(stored.RefreshTokenCiphertext))
	}
	if string(stored.AccessTokenCiphertext) != string(access) {
		return errors.New("access ciphertext changed on read back")
	}
	if stored.Origin != auth.AiandOriginPaste {
		return fmt.Errorf("origin = %q, want paste", stored.Origin)
	}
	fmt.Printf("paste_upsert_id=%s refresh_len=%d\n", credential.ID, len(stored.RefreshTokenCiphertext))
	return nil
}

// checkDeviceRefreshRoundTrip covers (c): a device-shaped upsert with a
// refresh token stores its ciphertext and returns it untouched.
func checkDeviceRefreshRoundTrip(ctx context.Context, repo *postgres.Repository, encryptor auth.Encryptor, install *auth.Installation) error {
	refresh, err := encryptor.Encrypt([]byte("rt_device_refresh_secret"), install.ExternalID, "aiand")
	if err != nil {
		return fmt.Errorf("seal refresh token: %w", err)
	}
	access, err := encryptor.Encrypt([]byte("sk-aiand-device-access"), install.ExternalID, "aiand")
	if err != nil {
		return fmt.Errorf("seal access token: %w", err)
	}
	_, err = repo.AiandCredentials.Upsert(ctx, auth.UpsertAiandCredentialParams{
		InstallationID:         install.ID,
		AccessTokenCiphertext:  access,
		RefreshTokenCiphertext: refresh,
		ExpiresAt:              time.Now().Add(time.Hour),
		Origin:                 auth.AiandOriginDevice,
		UserEmail:              "ops@example.com",
	})
	if err != nil {
		return fmt.Errorf("upsert with refresh: %w", err)
	}
	stored, err := repo.AiandCredentials.GetByInstallation(ctx, install.ID)
	if err != nil {
		return fmt.Errorf("read back after device upsert: %w", err)
	}
	if string(stored.RefreshTokenCiphertext) != string(refresh) {
		return errors.New("device refresh ciphertext not preserved on read back")
	}
	// Also verify the service-facing contract: only a device row may decrypt.
	opened, err := encryptor.Decrypt(stored.RefreshTokenCiphertext, install.ExternalID, "aiand")
	if err != nil {
		return fmt.Errorf("decrypt stored refresh: %w", err)
	}
	if string(opened) != "rt_device_refresh_secret" {
		return errors.New("decrypted refresh token mismatch")
	}
	if stored.UserEmail != "ops@example.com" {
		return fmt.Errorf("user_email = %q, want ops@example.com", stored.UserEmail)
	}
	fmt.Println("device_upsert_ok refresh_preserved=true")
	return nil
}

// checkListExpiring covers (d): after the paste upsert (expired bookkeeping
// timestamp) and the device upsert (far-future expiry) the sweep list must
// contain exactly one row for this installation and it must be the
// refresh-bearing device row.
func checkListExpiring(ctx context.Context, repo *postgres.Repository, install *auth.Installation) error {
	rows, err := repo.AiandCredentials.ListExpiring(ctx, time.Now().Add(25*time.Hour))
	if err != nil {
		return fmt.Errorf("list expiring: %w", err)
	}
	found := 0
	for _, row := range rows {
		if row.Credential.InstallationID != install.ID {
			continue
		}
		found++
		if len(row.Credential.RefreshTokenCiphertext) == 0 {
			return errors.New("ListExpiring returned the paste row; only refresh-bearing rows must survive rotation cutoff")
		}
		if row.Credential.Origin != auth.AiandOriginDevice {
			return fmt.Errorf("ListExpiring origin = %q, want device", row.Credential.Origin)
		}
	}
	if found != 1 {
		return fmt.Errorf("ListExpiring returned %d rows for the installation, want 1 (device only)", found)
	}
	return nil
}

// newTinkEncryptor builds a throwaway in-memory Tink keyset; ciphertexts in
// this check never leave the process except through the real seal/open path.
func newTinkEncryptor() auth.Encryptor {
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	if err != nil {
		fail("generate tink keyset", err)
	}
	var buf bytes.Buffer
	if err := insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)); err != nil {
		fail("write tink keyset", err)
	}
	enc, err := auth.NewTinkEncryptor(buf.String())
	if err != nil {
		fail("construct tink encryptor", err)
	}
	return enc
}
