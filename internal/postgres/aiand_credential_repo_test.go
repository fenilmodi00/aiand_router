package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiandRowStub builds the row GetAiandCredentialByInstallation returns.
func aiandRowStub() sqlc.RouterAiandCredential {
	return sqlc.RouterAiandCredential{
		ID:                     uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		InstallationID:         uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		AccessTokenCiphertext:  []byte("sealed-access"),
		RefreshTokenCiphertext: []byte("sealed-refresh"),
		ExpiresAt:              pgtype.Timestamptz{Time: time.Unix(1700000000, 0), Valid: true},
		Origin:                 "device",
		UserEmail:              new("ops@example.com"),
		OrgID:                  nil,
		OrgName:                new("Acme"),
	}
}

func TestToAiandCredential_MapsRowAndKeepsCiphertextSealed(t *testing.T) {
	row := aiandRowStub()

	credential := toAiandCredential(row)

	assert.Equal(t, row.ID.String(), credential.ID)
	assert.Equal(t, row.InstallationID.String(), credential.InstallationID)
	assert.Equal(t, []byte("sealed-access"), credential.AccessTokenCiphertext, "repo returns ciphertext untouched; only the auth service opens it")
	assert.Equal(t, []byte("sealed-refresh"), credential.RefreshTokenCiphertext)
	assert.Empty(t, credential.Plaintext, "the adapter never decrypts")
	assert.Equal(t, auth.AiandOriginDevice, credential.Origin)
	assert.Equal(t, "ops@example.com", credential.UserEmail)
	assert.Equal(t, "Acme", credential.OrgName)
	assert.Equal(t, "", credential.OrgID, "NULL columns read back as empty strings")
	assert.Equal(t, row.ExpiresAt.Time, credential.ExpiresAt)
	assert.Equal(t, row.Origin, string(credential.Origin))
}

// errRowStub answers every QueryRow with a fixed error.
type errRowStub struct {
	err error
}

func (*errRowStub) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (*errRowStub) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (*errRowStub) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return errorRow{err: pgx.ErrNoRows}
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

func TestAiandCredentialRepo_GetByInstallation_MapsNoRowsToSentinel(t *testing.T) {
	repo := NewAiandCredentialRepo(&errRowStub{})

	_, err := repo.GetByInstallation(context.Background(), uuid.NewString())
	assert.ErrorIs(t, err, auth.ErrAiandCredentialNotFound,
		"callers distinguish 'not connected yet' from storage failure")
}

func TestAiandCredentialRepo_GetByInstallation_RejectsNonUUID(t *testing.T) {
	repo := NewAiandCredentialRepo(&errRowStub{})
	_, err := repo.GetByInstallation(context.Background(), "not-a-uuid")
	assert.Error(t, err)
}

// execCapture records the SQLC call the repo issued.
type execCapture struct {
	queryName string
	args      []interface{}
	// row, when set, is replayed through QueryRow for :one queries.
	row *sqlc.RouterAiandCredential
}

func (e *execCapture) Exec(_ context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	e.queryName = queryNameOf(query)
	e.args = args
	return pgconn.CommandTag{}, nil
}

func (e *execCapture) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (e *execCapture) QueryRow(_ context.Context, query string, args ...interface{}) pgx.Row {
	e.queryName = queryNameOf(query)
	e.args = args
	if e.row == nil {
		return errorRow{err: errors.New("unexpected QueryRow")}
	}
	return singleRow{row: *e.row}
}

// singleRow scans a pre-built sqlc row once.
type singleRow struct {
	row sqlc.RouterAiandCredential
}

func (r singleRow) Scan(dest ...any) error {
	return copyRowInto(&r.row, dest)
}

// copyRowInto copies the stub row's fields into sqlc's generated Scan
// targets, following RouterAiandCredential's field order.
func copyRowInto(row *sqlc.RouterAiandCredential, dest []any) error {
	if len(dest) != 11 {
		return fmt.Errorf("scan targets %d, want 11", len(dest))
	}
	values := []any{
		row.ID,
		row.InstallationID,
		row.AccessTokenCiphertext,
		row.RefreshTokenCiphertext,
		row.ExpiresAt,
		row.Origin,
		row.UserEmail,
		row.OrgID,
		row.OrgName,
		row.CreatedAt,
		row.UpdatedAt,
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
		case *pgtype.Timestamptz:
			*target = v.(pgtype.Timestamptz)
		default:
			return fmt.Errorf("unsupported scan target at %d: %T", i, dest[i])
		}
	}
	return nil
}

func TestAiandCredentialRepo_Delete_ParsesAndForwards(t *testing.T) {
	capture := &execCapture{}
	repo := NewAiandCredentialRepo(capture)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	require.NoError(t, repo.Delete(context.Background(), id.String()))
	assert.Equal(t, "DeleteAiandCredential", queryMethodName(capture.queryName))
	parsed, ok := capture.args[0].(uuid.UUID)
	require.True(t, ok, "installation id must reach SQLC as uuid.UUID")
	assert.Equal(t, id, parsed)

	assert.Error(t, repo.Delete(context.Background(), "garbage"), "unparseable ids must not reach SQLC")
}

// TestAiandCredentialRepo_Upsert_NormalizesNilRefresh is the defense-in-depth
// layer for issue #111: even if a caller skips the service-level sentinel,
// the repo must bind empty bytea, never SQL NULL, for refresh_token_ciphertext.
func TestAiandCredentialRepo_Upsert_NormalizesNilRefresh(t *testing.T) {
	capture := &execCapture{}
	// Upsert uses QueryRow, so answer it with a mapped row.
	rowStub := aiandRowStub()
	capture.row = &rowStub
	repo := NewAiandCredentialRepo(capture)

	_, err := repo.Upsert(context.Background(), auth.UpsertAiandCredentialParams{
		InstallationID:         rowStub.InstallationID.String(),
		AccessTokenCiphertext:  []byte("sealed-access"),
		RefreshTokenCiphertext: nil, // paste shape reaching the repo unnormalized
		ExpiresAt:              time.Unix(1700000000, 0),
		Origin:                 auth.AiandOriginPaste,
	})
	require.NoError(t, err)

	require.Len(t, capture.args, 8)
	bound, ok := capture.args[2].([]byte)
	require.True(t, ok, "refresh ciphertext must reach SQLC as []byte, got %T", capture.args[2])
	assert.NotNil(t, bound, "nil slice binds as SQL NULL against BYTEA NOT NULL; must be empty bytea")
	assert.Empty(t, bound, "sentinel is empty bytea, not a real ciphertext")
}

// queryNameOf extracts the "-- name: X" marker from the emitted SQL comment.
func queryNameOf(query string) string {
	for line := range strings.SplitSeq(query, "\n") {
		if name, ok := strings.CutPrefix(line, "-- name: "); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// queryMethodName strips the ":exec" / ":one" result marker sqlc appends.
func queryMethodName(marker string) string {
	name, _, _ := strings.Cut(marker, " ")
	return name
}

// expiringCapture records the ListExpiring call and replays a fixed row set.
type expiringCapture struct {
	queryName string
	args      []interface{}

	rows []sqlc.ListExpiringAiandCredentialsRow
	err  error
}

func (e *expiringCapture) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (e *expiringCapture) Query(_ context.Context, query string, args ...interface{}) (pgx.Rows, error) {
	e.queryName = queryNameOf(query)
	e.args = args
	if e.err != nil {
		return nil, e.err
	}
	return &expiringRows{remaining: e.rows}, nil
}

func (e *expiringCapture) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return errorRow{err: errors.New("unexpected QueryRow")}
}

// expiringRows replays ListExpiringAiandCredentialsRow values through pgx.Rows.
type expiringRows struct {
	remaining []sqlc.ListExpiringAiandCredentialsRow
	current   sqlc.ListExpiringAiandCredentialsRow
}

func (rows *expiringRows) Next() bool {
	if len(rows.remaining) == 0 {
		return false
	}
	rows.current = rows.remaining[0]
	rows.remaining = rows.remaining[1:]
	return true
}

func (rows *expiringRows) Scan(dest ...any) error {
	row := rows.current
	values := []any{
		row.RouterAiandCredential.ID,
		row.RouterAiandCredential.InstallationID,
		row.RouterAiandCredential.AccessTokenCiphertext,
		row.RouterAiandCredential.RefreshTokenCiphertext,
		row.RouterAiandCredential.ExpiresAt,
		row.RouterAiandCredential.Origin,
		row.RouterAiandCredential.UserEmail,
		row.RouterAiandCredential.OrgID,
		row.RouterAiandCredential.OrgName,
		row.RouterAiandCredential.CreatedAt,
		row.RouterAiandCredential.UpdatedAt,
		row.RouterModelRouterInstallation.ID,
		row.RouterModelRouterInstallation.ExternalID,
		row.RouterModelRouterInstallation.Name,
		row.RouterModelRouterInstallation.CreatedAt,
		row.RouterModelRouterInstallation.UpdatedAt,
		row.RouterModelRouterInstallation.DeletedAt,
		row.RouterModelRouterInstallation.CreatedBy,
		row.RouterModelRouterInstallation.ExcludedModels,
		row.RouterModelRouterInstallation.ExcludedProviders,
		row.RouterModelRouterInstallation.RoutingQualityWeight,
		row.RouterModelRouterInstallation.PreferredModels,
		row.RouterModelRouterInstallation.RoutingStrategy,
		row.RouterModelRouterInstallation.RoutingRolloutID,
		row.RouterModelRouterInstallation.PolicyShadowStrategy,
		row.RouterModelRouterInstallation.PolicyDebugEnabled,
		row.RouterModelRouterInstallation.PolicyHeaderOverridesEnabled,
		row.RouterModelRouterInstallation.PolicyRoutingIntent,
		row.RouterModelRouterInstallation.AiTrainingAllowed,
		row.RouterModelRouterInstallation.ByokEnabled,
		row.RouterModelRouterInstallation.ContentCaptureMode,
		row.RouterModelRouterInstallation.HideTerminalSurfaces,
		row.RouterModelRouterInstallation.AllowedModels,
		row.RouterModelRouterInstallation.FirstRequestServedAt,
		row.RouterModelRouterInstallation.FlagOverrides,
		row.RouterModelRouterInstallation.ModelsWhenSubscriptionActive,
		row.RouterModelRouterInstallation.ModelsWhenSubscriptionInactive,
		row.RouterModelRouterInstallation.TrialCaptureEnabled,
		row.RouterModelRouterInstallation.TrialEnrollmentID,
		row.RouterModelRouterInstallation.TrialShadowSampleRate,
		row.RouterModelRouterInstallation.TrialShadowDailyCeilingUsdMicros,
		row.RouterModelRouterInstallation.ShowModelSelectionReasoning,
		row.RouterModelRouterInstallation.SubscriptionStateModelsEnabled,
		row.RouterModelRouterInstallation.ModelsExcludedWhenSubscriptionActive,
		row.RouterModelRouterInstallation.ModelsExcludedWhenSubscriptionInactive,
	}
	if len(dest) != len(values) {
		return errors.New("unexpected scan target count")
	}
	return scanExpiringTargets(dest, values)
}

// scanExpiringTargets copies stub values into the scan targets sqlc generated
// for the joined row. The subscriber_allowance helper covers fewer types than
// the installation columns need, so this file carries its own.
func scanExpiringTargets(dest, values []any) error {
	for i := range dest {
		switch target := dest[i].(type) {
		case *uuid.UUID:
			*target = values[i].(uuid.UUID)
		case *[]byte:
			*target = values[i].([]byte)
		case *string:
			*target = values[i].(string)
		case **string:
			*target = values[i].(*string)
		case *bool:
			*target = values[i].(bool)
		case **float64:
			*target = values[i].(*float64)
		case **int64:
			*target = values[i].(*int64)
		case *[]string:
			*target = values[i].([]string)
		case **[]byte:
			*target = values[i].(*[]byte)
		case *pgtype.Timestamp:
			*target = values[i].(pgtype.Timestamp)
		case *pgtype.Timestamptz:
			*target = values[i].(pgtype.Timestamptz)
		case *pgtype.UUID:
			*target = values[i].(pgtype.UUID)
		case *pgtype.Numeric:
			*target = values[i].(pgtype.Numeric)
		default:
			return fmt.Errorf("unsupported scan target at %d: %T", i, dest[i])
		}
	}
	return nil
}

func (*expiringRows) Close()                                       {}
func (*expiringRows) Err() error                                   { return nil }
func (*expiringRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*expiringRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*expiringRows) Values() ([]any, error)                       { return nil, nil }
func (*expiringRows) RawValues() [][]byte                          { return nil }
func (*expiringRows) Conn() *pgx.Conn                              { return nil }

func TestAiandCredentialRepo_ListExpiring_MapsRowsAndCarriesExternalID(t *testing.T) {
	capture := &expiringCapture{rows: []sqlc.ListExpiringAiandCredentialsRow{{
		RouterAiandCredential:         aiandRowStub(),
		RouterModelRouterInstallation: sqlc.RouterModelRouterInstallation{ExternalID: "ext-42"},
	}}}
	repo := NewAiandCredentialRepo(capture)

	got, err := repo.ListExpiring(context.Background(), time.Unix(2000000000, 0))
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "ListExpiringAiandCredentials", queryMethodName(capture.queryName))
	require.Len(t, capture.args, 1)
	threshold, ok := capture.args[0].(pgtype.Timestamptz)
	require.True(t, ok, "threshold must reach SQLC as a timestamptz")
	assert.True(t, threshold.Valid)
	assert.Equal(t, time.Unix(2000000000, 0), threshold.Time)
	assert.Equal(t, "ext-42", got[0].InstallationExternalID, "rotation needs the AAD identity joined in")
	assert.Equal(t, "00000000-0000-0000-0000-000000000002", got[0].Credential.InstallationID)
	assert.Equal(t, []byte("sealed-refresh"), got[0].Credential.RefreshTokenCiphertext, "repo returns ciphertext untouched")
}

func TestAiandCredentialRepo_ListExpiring_EmptyIsNilErrorFree(t *testing.T) {
	repo := NewAiandCredentialRepo(&expiringCapture{})

	got, err := repo.ListExpiring(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, got, "no expiring rows is an empty result, not an error")
}

func TestAiandCredentialRepo_ListExpiring_PropagatesQueryFailure(t *testing.T) {
	repo := NewAiandCredentialRepo(&expiringCapture{err: errors.New("pool closed")})

	_, err := repo.ListExpiring(context.Background(), time.Now())
	assert.ErrorContains(t, err, "pool closed")
}
