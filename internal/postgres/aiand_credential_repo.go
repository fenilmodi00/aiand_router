package postgres

import (
	"context"
	"errors"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// AiandCredentialRepo implements auth.AiandCredentialRepository over SQLC.
// The service layer seals/opens the token ciphertexts with the shared Tink
// Encryptor and (installation external ID, "aiand") AAD; this adapter stores
// and returns those ciphertexts untouched.
type AiandCredentialRepo struct {
	tx sqlc.DBTX
}

// NewAiandCredentialRepo constructs an AiandCredentialRepo.
func NewAiandCredentialRepo(tx sqlc.DBTX) *AiandCredentialRepo {
	return &AiandCredentialRepo{tx: tx}
}

func (r *AiandCredentialRepo) Upsert(ctx context.Context, params auth.UpsertAiandCredentialParams) (*auth.AiandCredential, error) {
	installationUUID, err := uuid.Parse(params.InstallationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.UpsertAiandCredential(ctx, sqlc.UpsertAiandCredentialParams{
		InstallationID:         installationUUID,
		AccessTokenCiphertext:  params.AccessTokenCiphertext,
		RefreshTokenCiphertext: params.RefreshTokenCiphertext,
		ExpiresAt:              pgtype.Timestamptz{Time: params.ExpiresAt, Valid: true},
		Origin:                 string(params.Origin),
		UserEmail:              stringPtrOrNil(params.UserEmail),
		OrgID:                  stringPtrOrNil(params.OrgID),
		OrgName:                stringPtrOrNil(params.OrgName),
	})
	if err != nil {
		return nil, err
	}
	return toAiandCredential(row), nil
}

func (r *AiandCredentialRepo) GetByInstallation(ctx context.Context, installationID string) (*auth.AiandCredential, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetAiandCredentialByInstallation(ctx, installationUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrAiandCredentialNotFound
		}
		return nil, err
	}
	return toAiandCredential(row), nil
}

func (r *AiandCredentialRepo) Delete(ctx context.Context, installationID string) error {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	_, err = q.DeleteAiandCredential(ctx, installationUUID)
	return err
}

// ListExpiring returns the sweep candidates whose access token expires before
// the given threshold. A :many query with no matches yields an empty slice,
// not an error; the sweep treats that as nothing to do.
func (r *AiandCredentialRepo) ListExpiring(ctx context.Context, before time.Time) ([]auth.AiandExpiringCredential, error) {
	q := dbbudget.Queries(r.tx)
	rows, err := q.ListExpiringAiandCredentials(ctx, pgtype.Timestamptz{Time: before, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]auth.AiandExpiringCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, auth.AiandExpiringCredential{
			Credential:             *toAiandCredential(row.RouterAiandCredential),
			InstallationExternalID: row.RouterModelRouterInstallation.ExternalID,
		})
	}
	return out, nil
}

func toAiandCredential(row sqlc.RouterAiandCredential) *auth.AiandCredential {
	return &auth.AiandCredential{
		ID:                     row.ID.String(),
		InstallationID:         row.InstallationID.String(),
		AccessTokenCiphertext:  row.AccessTokenCiphertext,
		RefreshTokenCiphertext: row.RefreshTokenCiphertext,
		ExpiresAt:              timestamptzOrZero(row.ExpiresAt),
		Origin:                 auth.AiandCredentialOrigin(row.Origin),
		UserEmail:              derefString(row.UserEmail),
		OrgID:                  derefString(row.OrgID),
		OrgName:                derefString(row.OrgName),
		CreatedAt:              timestamptzOrZero(row.CreatedAt),
		UpdatedAt:              timestamptzOrZero(row.UpdatedAt),
	}
}
