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

	// Re-login fast path: an existing identity is reused without touching the
	// partial unique index. Without this, the insert raises 23505 inside the
	// login transaction, which aborts it; the follow-up identity re-read then
	// fails with 25P02 ("current transaction is aborted").
	existing, err := r.GetAccountByIdentity(ctx, params.AiandUserID, params.AiandOrgID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, auth.ErrAccountNotFound) {
		return nil, err
	}

	q := dbbudget.Queries(r.tx)
	// The insert still needs a savepoint for the concurrent-first-login race:
	// a 23505 poisons the enclosing transaction, so roll back to the
	// savepoint before the identity re-read. A non-transactional handle
	// (pool / test fake) needs none.
	tx, inTx := r.tx.(pgx.Tx)
	const savepoint = "account_login_insert"
	if inTx {
		if _, err := tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
			return nil, err
		}
	}
	row, err := q.InsertAccount(ctx, sqlc.InsertAccountParams{
		ID:             stringPtrOrNil(params.ID),
		GeneratedID:    auth.GenerateID(auth.AccountIDPrefix),
		AiandUserID:    params.AiandUserID,
		AiandOrgID:     params.AiandOrgID,
		Email:          stringPtrOrNil(params.Email),
		DisplayName:    stringPtrOrNil(params.DisplayName),
		InstallationID: installationUUID,
	})
	if err == nil {
		if inTx {
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
				return nil, err
			}
		}
		return toAccount(row), nil
	}

	// Concurrent first logins of the same identity arbitrate on the partial
	// unique index accounts_identity_active_unique: the loser re-reads by
	// identity and reuses the winner's account + installation.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if inTx {
			if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); rbErr != nil {
				return nil, rbErr
			}
		}
		return r.GetAccountByIdentity(ctx, params.AiandUserID, params.AiandOrgID)
	}
	return nil, err
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
