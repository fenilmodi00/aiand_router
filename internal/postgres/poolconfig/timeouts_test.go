package poolconfig

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestConfigureTimeoutsReplacesLongerConnectionDefaults(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://router:router@localhost/router?sslmode=disable")
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["lock_timeout"] = "10min"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10min"

	ConfigureTimeouts(config)

	require.Equal(t, lockTimeout, config.ConnConfig.RuntimeParams["lock_timeout"])
	require.Equal(t, statementTimeout, config.ConnConfig.RuntimeParams["statement_timeout"])
}

func TestConfigureTimeoutsPoolerCompatOmitsStartupParams(t *testing.T) {
	t.Setenv("ROUTER_PG_POOLER_COMPAT", "1")
	config, err := pgxpool.ParseConfig("postgres://router:router@localhost/router?sslmode=disable")
	require.NoError(t, err)

	ConfigureTimeouts(config)

	// A transaction-mode pooler rejects non-allowlisted startup parameters
	// (FATAL 08P01), so pooler-compat must not put the GUCs on the wire at
	// connect time; they ride in the AfterConnect hook instead.
	require.NotContains(t, config.ConnConfig.RuntimeParams, "lock_timeout")
	require.NotContains(t, config.ConnConfig.RuntimeParams, "statement_timeout")
	require.NotNil(t, config.AfterConnect)
	require.Contains(t, TimeoutStatements(), "SET lock_timeout TO '1s'")
	require.Contains(t, TimeoutStatements(), "SET statement_timeout TO '10s'")
}

func TestConfigureTimeoutsPoolerCompatWrapsExistingHook(t *testing.T) {
	t.Setenv("ROUTER_PG_POOLER_COMPAT", "1")
	config, err := pgxpool.ParseConfig("postgres://router:router@localhost/router?sslmode=disable")
	require.NoError(t, err)
	called := false
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		called = true
		return nil
	}

	ConfigureTimeouts(config)

	require.NotNil(t, config.AfterConnect)
	// The wrapper must call through to the pre-existing hook (search_path pin)
	// before applying the timeout SETs; the nil conn panics inside Exec only
	// after prev has run, proving the composition order.
	require.Panics(t, func() { _ = config.AfterConnect(context.Background(), nil) })
	require.True(t, called)
}
