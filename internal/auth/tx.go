package auth

import "context"

// Tx is the repository set one auth transaction operates on. Adapters build
// the members on the same transaction handle, so every write fn performs
// shares one commit boundary. Installations participates in the login unit
// of work: the account and its installation must commit together or not at
// all (accounts.installation_id is NOT NULL UNIQUE).
type Tx struct {
	ExternalAPIKeys  ExternalAPIKeyRepository
	AiandCredentials AiandCredentialRepository
	Accounts         AccountRepository
	Sessions         SessionRepository
	LoginAttempts    LoginAttemptRepository
	Installations    InstallationRepository
}

// TxRunner executes fn atomically: every write fn performs commits together
// or rolls back together. This is the general one-method transaction seam of
// the auth package — the upcoming LoginService ticket reuses it for the
// larger login transaction (account upsert, installation ensure, credential,
// external key, attempt consumption, session mint). internal/postgres
// implements it over pgx.Tx + SQLC WithTx; tests inject fakes.
type TxRunner interface {
	InTx(ctx context.Context, fn func(tx Tx) error) error
}

// WithTxRunner wires the transaction runner used by multi-write persistence
// paths (today the aiand credential + external-key pair). Pass nil to keep
// the legacy pool-backed, non-transactional writes.
func (s *Service) WithTxRunner(runner TxRunner) *Service {
	s.txRunner = runner
	return s
}
