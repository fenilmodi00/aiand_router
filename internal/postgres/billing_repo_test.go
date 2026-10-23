package postgres_test

import (
	"context"
	"testing"

	"aiand-os/router/internal/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A router booted before a billing migration lands must fail the table-count
// check, so a missing-migration state disables billing rather than 500ing on
// every request.
func TestBillingTablesExistDetectsMissingTable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	ok, err := postgres.NewBillingRepo(pool).BillingTablesExist(ctx)
	require.NoError(t, err)
	require.True(t, ok, "migrated database should report all billing tables present")

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "DROP TABLE router.organization_spend_limits")
	require.NoError(t, err)

	ok, err = postgres.NewBillingRepo(tx).BillingTablesExist(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "missing spend-limit table must fail billing readiness")
}
