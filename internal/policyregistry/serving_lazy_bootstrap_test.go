package policyregistry_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

func TestGCSCompareAndSwapServingStateMigratesLegacyStateToNewPathOnce(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	set := fixtureSet("active")
	legacySnapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	firstSnapshot, _ := activateFixture(t, legacySnapshot, fixtureSet("first"), servingEpoch.Add(time.Hour))
	secondSnapshot, _ := activateFixture(t, firstSnapshot, fixtureSet("second"), servingEpoch.Add(2*time.Hour))
	legacyName := "weave_registry/runtime_state/router_serving/v1/targets/prod-01/prod/stable/state.json"
	newName := "weave_registry/state/prod-01/prod/stable.json"
	legacyPayload, err := json.Marshal(legacySnapshot.State)
	require.NoError(t, err)
	fixture.mu.Lock()
	fixture.generation = 41
	fixture.objects[legacyName] = map[int64][]byte{41: legacyPayload}
	fixture.current[legacyName] = 41
	fixture.mu.Unlock()

	observed, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.True(t, observed.LegacyPath)
	require.EqualValues(t, 41, observed.Generation)
	require.EqualValues(t, 0, observed.WriteGeneration())

	_, err = registry.CompareAndSwapServingState(ctx, firstSnapshot.State, observed.Generation)
	require.ErrorIs(t, err, policyregistry.ErrConflict, "the legacy generation never matches the not-yet-created new object")

	migrated, err := registry.CompareAndSwapServingState(ctx, firstSnapshot.State, observed.WriteGeneration())
	require.NoError(t, err)
	require.False(t, migrated.LegacyPath)
	fixture.mu.Lock()
	assert.Equal(t, fixture.current[newName], migrated.Generation)
	assert.Equal(t, map[int64][]byte{41: legacyPayload}, fixture.objects[legacyName], "the legacy object is neither rewritten nor removed")
	assert.Len(t, fixture.objects[newName], 1)
	fixture.mu.Unlock()

	_, err = registry.CompareAndSwapServingState(ctx, firstSnapshot.State, 0)
	require.ErrorIs(t, err, policyregistry.ErrConflict, "a concurrent legacy reader cannot bootstrap twice")

	current, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	require.Equal(t, migrated, current)
	advanced, err := registry.CompareAndSwapServingState(ctx, secondSnapshot.State, current.WriteGeneration())
	require.NoError(t, err)
	require.Greater(t, advanced.Generation, migrated.Generation)

	_, err = registry.CompareAndSwapServingState(ctx, secondSnapshot.State, migrated.Generation)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
	fixture.mu.Lock()
	assert.Equal(t, map[int64][]byte{41: legacyPayload}, fixture.objects[legacyName])
	assert.Len(t, fixture.objects[newName], 2)
	fixture.mu.Unlock()
	final, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: secondSnapshot.State, Generation: advanced.Generation}, final)
}
