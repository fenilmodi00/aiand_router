package policyregistry_test

import (
	"context"
	"time"

	"weave-os/router/internal/policyregistry"
)

// slowServingStore delays every authoritative read so a recorded duration is
// distinguishable from an unrecorded zero.
type slowServingStore struct {
	*servingMemoryStore
	delay time.Duration
}

func (s slowServingStore) ReadServingState(ctx context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	time.Sleep(s.delay)
	return s.servingMemoryStore.ReadServingState(ctx, target)
}

func (s slowServingStore) ReadServingObject(ctx context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	time.Sleep(s.delay)
	return s.servingMemoryStore.ReadServingObject(ctx, kind, ref)
}
