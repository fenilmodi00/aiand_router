package postgres

import (
	"context"
	"errors"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// LoginAttemptRepo implements auth.LoginAttemptRepository over SQLC.
type LoginAttemptRepo struct {
	tx sqlc.DBTX
}

// NewLoginAttemptRepo constructs a LoginAttemptRepo.
func NewLoginAttemptRepo(tx sqlc.DBTX) *LoginAttemptRepo {
	return &LoginAttemptRepo{tx: tx}
}

func (r *LoginAttemptRepo) InsertLoginAttempt(ctx context.Context, params auth.InsertLoginAttemptParams) (*auth.LoginAttempt, error) {
	// device_code_ciphertext is NOT NULL: bind an empty bytea for an empty
	// seal, never SQL NULL (nil []byte would otherwise arrive as NULL).
	ciphertext := params.DeviceCodeCiphertext
	if ciphertext == nil {
		ciphertext = []byte{}
	}
	attemptID, err := optionalUUID(params.ID)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.InsertLoginAttempt(ctx, sqlc.InsertLoginAttemptParams{
		ID:                     attemptID,
		DeviceCodeCiphertext:   ciphertext,
		GatewayIntervalSeconds: int32(params.Interval / time.Second),
		ExpiresAt:              pgtype.Timestamptz{Time: params.ExpiresAt, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	return toLoginAttempt(row), nil
}

func (r *LoginAttemptRepo) GetActiveLoginAttempt(ctx context.Context, id string) (*auth.LoginAttempt, error) {
	attemptUUID, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetActiveLoginAttempt(ctx, attemptUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrLoginAttemptNotFound
		}
		return nil, err
	}
	return toLoginAttempt(row), nil
}

func (r *LoginAttemptRepo) MarkLoginAttemptConsumed(ctx context.Context, id string) error {
	attemptUUID, err := parseUUID(id)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.MarkLoginAttemptConsumed(ctx, attemptUUID)
}

func (r *LoginAttemptRepo) DeleteLoginAttempt(ctx context.Context, id string) error {
	attemptUUID, err := parseUUID(id)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.DeleteLoginAttempt(ctx, attemptUUID)
}

func (r *LoginAttemptRepo) DeleteExpiredLoginAttempts(ctx context.Context, now time.Time) (int64, error) {
	q := dbbudget.Queries(r.tx)
	return q.DeleteExpiredLoginAttempts(ctx, pgtype.Timestamptz{Time: now, Valid: true})
}

func toLoginAttempt(row sqlc.RouterAccountLoginAttempt) *auth.LoginAttempt {
	return &auth.LoginAttempt{
		ID:         row.ID.String(),
		DeviceCode: row.DeviceCodeCiphertext,
		Interval:   time.Duration(row.GatewayIntervalSeconds) * time.Second,
		ExpiresAt:  timestamptzOrZero(row.ExpiresAt),
		ConsumedAt: timestamptzPtr(row.ConsumedAt),
		CreatedAt:  timestamptzOrZero(row.CreatedAt),
	}
}
