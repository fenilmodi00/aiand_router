package policyregistry_test

import (
	"context"
	"testing"

	"aiand-os/router/internal/policyregistry"
	"aiand-os/router/internal/router"
	"github.com/stretchr/testify/require"
)

func cohortCache(t *testing.T, store *servingMemoryStore) *policyregistry.ServingRuntimeCache {
	t.Helper()
	cache, err := policyregistry.NewServingRuntimeCache(store, func(context.Context, policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		return map[router.Strategy]router.Router{router.StrategyHMM: stubRouter{}}, nil
	})
	require.NoError(t, err)
	return cache
}
