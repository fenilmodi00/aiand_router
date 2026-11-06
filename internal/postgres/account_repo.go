package postgres

import (
	"context"
	"errors"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AccountRepo implements auth.AccountRepository over SQLC.
//
// external_id immutability / AAD lifetime: accounts.id initializes the
// installation's external_id and is never renamed afterwards. It is the
// additional authenticated data binding every sealed credential, external
// API key, and device-code ciphertext tied to the account; a rename would
// make all of that material unopenable. Treat it as append-only AAD for the
// lifetime of the account and its installation.
type AccountRepo struct {
	tx sqlc.DBTX
}

// NewAccountRepo constructs an AccountRepo.
func NewAccountRepo(tx sqlc.DBTX) *AccountRepo {
	return &AccountRepo{tx: tx}
}

func (r *AccountRepo) UpsertAccountOnLogin(ctx context.Context, params auth.UpsertAccountOnLoginParams) (*auth.Account, error) {
	installationUUID, err := parseUUID(params.InstallationID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.InsertAccount(ctx, sqlc.InsertAccountParams{
		ID:             stringPtrOrNil(params.ID),
		GeneratedID:    auth.GenerateID(auth.AccountIDPrefix),
		AiandUserID:    params.AiandUserID,
		AiandOrgID:     params.AiandOrgID,
		Email:          stringPtrOrNil(params.Email),
		DisplayName:    stringPtrOrNil(params.DisplayName),
		InstallationID: installationUUID,
	})
	if err != nil {
		// Concurrent first logins of the same identity arbitrate on the
		// partial unique index accounts_identity_active_unique: the loser
		// re-reads by identity and reuses the winner's account +
		// installation. No conflict error reaches the user.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return r.GetAccountByIdentity(ctx, params.AiandUserID, params.AiandOrgID)
		}
		return nil, err
	}
	return toAccount(row), nil
}

func (r *AccountRepo) GetAccountByIdentity(ctx context.Context, aiandUserID, aiandOrgID string) (*auth.Account, error) {
	q := dbbudget.Queries(r.tx)
	row, err := q.GetAccountByIdentity(ctx, sqlc.GetAccountByIdentityParams{
		AiandUserID: aiandUserID,
		AiandOrgID:  aiandOrgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrAccountNotFound
		}
		return nil, err
	}
	return toAccount(row), nil
}

func (r *AccountRepo) GetAccountByID(ctx context.Context, id string) (*auth.Account, error) {
	q := dbbudget.Queries(r.tx)
	row, err := q.GetAccountByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrAccountNotFound
		}
		return nil, err
	}
	return toAccount(row), nil
}

func (r *AccountRepo) SoftDeleteAccount(ctx context.Context, id string) error {
	q := dbbudget.Queries(r.tx)
	return q.SoftDeleteAccount(ctx, id)
}

func toAccount(row sqlc.RouterAccount) *auth.Account {
	return &auth.Account{
		ID:             row.ID,
		AiandUserID:    row.AiandUserID,
		AiandOrgID:     row.AiandOrgID,
		Email:          derefString(row.Email),
		DisplayName:    derefString(row.DisplayName),
		InstallationID: row.InstallationID.String(),
		CreatedAt:      timestamptzOrZero(row.CreatedAt),
		LastLoginAt:    timestamptzOrZero(row.LastLoginAt),
		DeletedAt:      timestamptzPtr(row.DeletedAt),
	}
}
