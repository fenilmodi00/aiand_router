package poolconfig

import (
	"context"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockTimeout      = "1s"
	statementTimeout = "10s"
)

// ConfigureTimeouts bounds lock waits and active statements on every pool connection.
//
// By default both GUCs ride as connection startup parameters. Behind a
// transaction-mode pooler (PgBouncer-style, e.g. build.io's schema-to-go)
// startup parameters other than a tiny allow-list are rejected with
// FATAL "unsupported startup parameter", so callers fronted by such a
// pooler set ROUTER_PG_POOLER_COMPAT=1: the GUCs are then applied with
// an ordinary SET in the AfterConnect hook, which poolers pass through.
func ConfigureTimeouts(config *pgxpool.Config) {
	if poolerCompat() {
		hookTimeoutsIntoAfterConnect(config)
		return
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["lock_timeout"] = lockTimeout
	config.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
}

func poolerCompat() bool {
	v, err := strconv.ParseBool(os.Getenv("ROUTER_PG_POOLER_COMPAT"))
	return err == nil && v
}

// TimeoutStatements returns the SET statements applying the configured
// timeouts, for embedding in an AfterConnect hook.
func TimeoutStatements() string {
	return "SET lock_timeout TO '" + lockTimeout + "'; SET statement_timeout TO '" + statementTimeout + "';"
}

func hookTimeoutsIntoAfterConnect(config *pgxpool.Config) {
	prev := config.AfterConnect
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if prev != nil {
			if err := prev(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, TimeoutStatements())
		return err
	}
}
