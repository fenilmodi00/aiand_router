package postgres

import (
	"context"
	"errors"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// AccountSessionRepo implements auth.SessionRepository over SQLC.
type AccountSessionRepo struct {
	tx sqlc.DBTX
}

// NewAccountSessionRepo constructs an AccountSessionRepo.
func NewAccountSessionRepo(tx sqlc.DBTX) *AccountSessionRepo {
	return &AccountSessionRepo{tx: tx}
}

func (r *AccountSessionRepo) InsertAccountSession(ctx context.Context, params auth.InsertSessionParams) (*auth.LoginSession, error) {
	accountID := params.AccountID
	q := dbbudget.Queries(r.tx)
	row, err := q.InsertAccountSession(ctx, sqlc.InsertAccountSessionParams{
		AccountID:   accountID,
		TokenHash:   params.TokenHash,
		TokenPrefix: params.TokenPrefix,
		ExpiresAt:   pgtype.Timestamptz{Time: params.ExpiresAt, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	return toLoginSession(row), nil
}

func (r *AccountSessionRepo) GetActiveSessionByTokenHash(ctx context.Context, params auth.GetActiveSessionParams) (*auth.ActiveSession, error) {
	q := dbbudget.Queries(r.tx)
	row, err := q.GetActiveSessionByTokenHash(ctx, params.TokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrSessionNotFound
		}
		return nil, err
	}
	return &auth.ActiveSession{
		Session:      *toLoginSession(row.RouterAccountSession),
		Account:      *toAccount(row.RouterAccount),
		Installation: toAuthInstallation(row.RouterModelRouterInstallation),
	}, nil
}

func (r *AccountSessionRepo) TouchSessionLastSeen(ctx context.Context, params auth.TouchSessionParams) error {
	q := dbbudget.Queries(r.tx)
	return q.TouchSessionLastSeen(ctx, sqlc.TouchSessionLastSeenParams{
		TokenHash: params.TokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: params.ExpiresAt, Valid: true},
	})
}

func (r *AccountSessionRepo) RevokeSessionByTokenHash(ctx context.Context, tokenHash string) error {
	q := dbbudget.Queries(r.tx)
	return q.RevokeSessionByTokenHash(ctx, tokenHash)
}

func (r *AccountSessionRepo) RevokeAllSessionsForAccount(ctx context.Context, accountID string) (int64, error) {
	q := dbbudget.Queries(r.tx)
	return q.RevokeAllSessionsForAccount(ctx, accountID)
}

func toLoginSession(row sqlc.RouterAccountSession) *auth.LoginSession {
	return &auth.LoginSession{
		ID:          row.ID.String(),
		AccountID:   row.AccountID,
		TokenHash:   row.TokenHash,
		TokenPrefix: row.TokenPrefix,
		IssuedAt:    timestamptzOrZero(row.IssuedAt),
		LastSeenAt:  timestamptzOrZero(row.LastSeenAt),
		ExpiresAt:   timestamptzOrZero(row.ExpiresAt),
		RevokedAt:   timestamptzPtr(row.RevokedAt),
	}
}
