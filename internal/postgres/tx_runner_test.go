package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePgxTx records transaction control calls and replays fixed rows for the
// queries the credential + external-key unit performs.
type fakePgxTx struct {
	committed  bool
	rolledBack bool
	queries    []string
	// err answers every QueryRow with a failure, simulating the second
	// write dying mid-transaction.
	err error

	aiandRow    sqlc.RouterAiandCredential
	externalRow sqlc.RouterModelRouterExternalAPIKey
}

func (t *fakePgxTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return t, nil
}

func (t *fakePgxTx) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return t, nil
}

func (t *fakePgxTx) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakePgxTx) Rollback(context.Context) error {
	// Mirror pgx: a rollback after a successful commit is a safe no-op
	// reporting ErrTxClosed; only a real rollback rolls back.
	if t.committed {
		return pgx.ErrTxClosed
	}
	t.rolledBack = true
	return nil
}

func (t *fakePgxTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	t.queries = append(t.queries, queryNameOf(query))
	return pgconn.CommandTag{}, nil
}

func (t *fakePgxTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (t *fakePgxTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	name := queryMethodName(queryNameOf(query))
	t.queries = append(t.queries, name)
	if t.err != nil {
		return errorRow{err: t.err}
	}
	switch name {
	case "UpsertAiandCredential":
		return externalKeyAwareRow{row: t.aiandRow}
	case "CreateExternalAPIKey":
		return externalKeyAwareRow{row: t.externalRow}
	default:
		return errorRow{err: errors.New("unexpected query " + name)}
	}
}

func (t *fakePgxTx) Conn() *pgx.Conn { return nil }
func (t *fakePgxTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unexpected CopyFrom")
}
func (t *fakePgxTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}
func (t *fakePgxTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }
func (t *fakePgxTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unexpected Prepare")
}

// externalKeyAwareRow scans either the aiand credential or the external key
// row into the generated scan targets by field order.
type externalKeyAwareRow struct {
	row any
}

func (r externalKeyAwareRow) Scan(dest ...any) error {
	switch row := r.row.(type) {
	case sqlc.RouterAiandCredential:
		return copyRowInto(&row, dest)
	case sqlc.RouterModelRouterExternalAPIKey:
		return copyExternalKeyRowInto(&row, dest)
	default:
		return errors.New("unsupported row type")
	}
}

// copyExternalKeyRowInto copies the stub external-key row into sqlc's
// generated scan targets, following RouterModelRouterExternalAPIKey's
// CreateExternalAPIKey RETURNING order.
func copyExternalKeyRowInto(row *sqlc.RouterModelRouterExternalAPIKey, dest []any) error {
	values := []any{
		row.ID,
		row.InstallationID,
		row.ExternalID,
		row.Provider,
		row.KeyCiphertext,
		row.KeyPrefix,
		row.KeySuffix,
		row.KeyFingerprint,
		row.Name,
		row.LastUsedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.DeletedAt,
		row.CreatedBy,
		row.BaseURL,
		row.ModelAliases,
		row.IdentityHeaderName,
		row.IdentityHeaderFormat,
		row.AuthType,
		row.AuthAccount,
		row.AuthUser,
		row.DeletedBy,
		row.ForwardedClientHeaders,
		row.BaggageHeader,
	}
	if len(dest) != len(values) {
		return errors.New("scan target count mismatch")
	}
	for i, v := range values {
		switch target := dest[i].(type) {
		case *uuid.UUID:
			*target = v.(uuid.UUID)
		case *[]byte:
			*target = v.([]byte)
		case *string:
			*target = v.(string)
		case **string:
			*target = v.(*string)
		case *[]string:
			*target = v.([]string)
		case *pgtype.Timestamp:
			*target = v.(pgtype.Timestamp)
		default:
			return errors.New("unsupported scan target type")
		}
	}
	return nil
}

func externalKeyRowStub() sqlc.RouterModelRouterExternalAPIKey {
	name := "aiand"
	created := pgtype.Timestamp{Time: time.Unix(0, 0), Valid: true}
	return sqlc.RouterModelRouterExternalAPIKey{
		ID:             uuid.MustParse("00000000-0000-0000-0000-00000000000a"),
		InstallationID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ExternalID:     "ekid-x",
		Provider:       "aiand",
		KeyCiphertext:  []byte("sealed"),
		KeyPrefix:      "sk-aiand",
		KeySuffix:      "6789",
		KeyFingerprint: "fp",
		Name:           &name,
		CreatedAt:      created,
		UpdatedAt:      created,
		AuthType:       "bearer",
	}
}

// TestTxRunner_CompletesCredentialAndExternalKeyOnOneTransaction drives the
// service's completion unit through the postgres TxRunner against a fake
// pgx.Tx and asserts both writes land on the same transaction and commit.
func TestTxRunner_CompletesCredentialAndExternalKeyOnOneTransaction(t *testing.T) {
	fake := &fakePgxTx{
		aiandRow:    aiandRowStub(),
		externalRow: externalKeyRowStub(),
	}
	runner := NewTxRunner(fake, auth.NoOpEncryptor{})
	encryptor := auth.NoOpEncryptor{}

	var got *auth.AiandCredential
	err := runner.InTx(context.Background(), func(tx auth.Tx) error {
		upserted, err := tx.AiandCredentials.Upsert(context.Background(), auth.UpsertAiandCredentialParams{
			InstallationID:        uuid.MustParse("00000000-0000-0000-0000-000000000002").String(),
			AccessTokenCiphertext: []byte("sk-aiand-test"),
			ExpiresAt:             time.Unix(0, 0).Add(time.Hour),
			Origin:                auth.AiandOriginDevice,
		})
		if err != nil {
			return err
		}
		ciphertext, err := encryptor.Encrypt([]byte("sk-aiand-test"), "ekid", providers.ProviderAIAND)
		if err != nil {
			return err
		}
		_, err = tx.ExternalAPIKeys.Create(context.Background(), auth.CreateExternalAPIKeyParams{
			InstallationID: uuid.MustParse("00000000-0000-0000-0000-000000000002").String(),
			ExternalID:     "ekid",
			Provider:       providers.ProviderAIAND,
			KeyCiphertext:  ciphertext,
			KeyPrefix:      "sk-aiand",
			KeySuffix:      "test",
			KeyFingerprint: "fp",
		})
		if err != nil {
			return err
		}
		got = upserted
		return nil
	})
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", got.InstallationID)
	assert.Contains(t, fake.queries, "UpsertAiandCredential")
	assert.Contains(t, fake.queries, "CreateExternalAPIKey", "both writes must run inside the transaction")
	assert.True(t, fake.committed, "successful completion must commit")
	assert.False(t, fake.rolledBack)
}

// TestTxRunner_FailureBetweenWritesRollsBack simulates the external-key
// write dying after the credential upsert succeeded: both writes must be
// rolled back (no commit), and the error surfaces to the caller.
func TestTxRunner_FailureBetweenWritesRollsBack(t *testing.T) {
	fake := &fakePgxTx{
		aiandRow:    aiandRowStub(),
		externalRow: externalKeyRowStub(),
		err:         errors.New("external key write failed"),
	}
	runner := NewTxRunner(fake, auth.NoOpEncryptor{})

	err := runner.InTx(context.Background(), func(tx auth.Tx) error {
		if _, err := tx.AiandCredentials.Upsert(context.Background(), auth.UpsertAiandCredentialParams{
			InstallationID:        uuid.MustParse("00000000-0000-0000-0000-000000000002").String(),
			AccessTokenCiphertext: []byte("sk-aiand-test"),
			ExpiresAt:             time.Unix(0, 0).Add(time.Hour),
			Origin:                auth.AiandOriginDevice,
		}); err != nil {
			return err
		}
		_, err := tx.ExternalAPIKeys.Create(context.Background(), auth.CreateExternalAPIKeyParams{
			InstallationID: uuid.MustParse("00000000-0000-0000-0000-000000000002").String(),
			ExternalID:     "ekid",
			Provider:       providers.ProviderAIAND,
		})
		return err
	})
	require.Error(t, err)
	assert.True(t, fake.rolledBack, "a failure between the writes must roll the unit back")
	assert.False(t, fake.committed, "no partial state may commit")
}
