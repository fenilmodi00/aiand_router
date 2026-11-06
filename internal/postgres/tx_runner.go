package postgres

import (
	"context"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/postgres/dbbudget"
	"aiand-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5"
)

// TxRunner implements auth.TxRunner over pgx transactions. fn's writes run on
// one pgx.Tx: every repo in the handed-out set is built on that tx, so a
// returned error rolls the whole unit back. auth.LoginService reuses this
// seam for the larger login transaction (account, installation, credential,
// external key, attempt consumption, session).
type TxRunner struct {
	// tx is the dbbudget-bounded handle BEGIN/COMMIT/ROLLBACK go through;
	// fn's statements carry their own per-operation budget via the repos.
	tx dbbudget.DBTX
	// encryptor seals the propagated external key's ciphertext; it matches
	// the one ExternalAPIKeyRepo outside transactions was built with.
	encryptor auth.Encryptor
}

// NewTxRunner constructs a TxRunner. tx is the pool (or any handle
// supporting pgx transactions); encryptor must be the same one the
// non-transactional ExternalAPIKeyRepo uses, so ciphertexts stay
// interchangeable.
func NewTxRunner(tx sqlc.DBTX, encryptor auth.Encryptor) *TxRunner {
	return &TxRunner{tx: dbbudget.NewDBTX(tx), encryptor: encryptor}
}

// InTx begins a transaction, runs fn against a repo set bound to it, and
// commits on success. A fn error (or a commit failure) rolls everything back.
func (r *TxRunner) InTx(ctx context.Context, fn func(tx auth.Tx) error) error {
	return pgx.BeginTxFunc(ctx, r.tx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(pgxTx pgx.Tx) error {
		return fn(auth.Tx{
			ExternalAPIKeys:  NewExternalAPIKeyRepo(pgxTx, r.encryptor),
			AiandCredentials: NewAiandCredentialRepo(pgxTx),
			Accounts:         NewAccountRepo(pgxTx),
			Sessions:         NewAccountSessionRepo(pgxTx),
			LoginAttempts:    NewLoginAttemptRepo(pgxTx),
			Installations:    &installationRepo{tx: pgxTx},
		})
	})
}
