package policyregistry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/policyregistry"
)

// artifactStoredRef addresses published bytes in the flat v2 layout.
func artifactStoredRef(payload []byte) policyregistry.ObjectRef {
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
}

func TestServingRefAcceptsBothLayoutsOnlyForTheKindThatFoldsThem(t *testing.T) {
	artifact := artifactStoredRef([]byte("object"))
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingCandidate, policyregistry.ServingSelectionSet, policyregistry.ServingProposal} {
		require.NoError(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), kind)
	}
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
		require.ErrorContains(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), "namespace", kind)
	}
	folds := map[policyregistry.ServingKind]policyregistry.ServingKind{policyregistry.ServingCandidate: policyregistry.ServingReleases, policyregistry.ServingSelectionSet: policyregistry.ServingSelectionSets, policyregistry.ServingProposal: policyregistry.ServingProposals}
	for family, legacy := range folds {
		require.NoError(t, policyregistry.ValidateServingRef(namespaceRef(legacy, "object"), testRegistryRoot, family))
		for _, other := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
			if other != legacy {
				require.Error(t, policyregistry.ValidateServingRef(namespaceRef(other, "object"), testRegistryRoot, family), "%s must not accept %s", family, other)
			}
		}
	}
	outside := artifact
	outside.URI = "gs://aiand_ml/other_registry/artifacts/" + artifact.SHA256 + ".json"
	assert.Error(t, policyregistry.ValidateServingRef(outside, testRegistryRoot, policyregistry.ServingCandidate))
	relabeled := artifact
	relabeled.URI = testRegistryRoot + "/artifacts/" + strings.Repeat("0", 64) + ".json"
	assert.Error(t, policyregistry.ValidateServingRef(relabeled, testRegistryRoot, policyregistry.ServingCandidate), "the URI must name the referenced digest")
}

func TestGCSReadServingStatePrefersNewPathAndFallsBackToLegacy(t *testing.T) {
	registry, fixture := newServingGCSFixture(t)
	ctx := context.Background()
	set := fixtureSet("active")
	legacySnapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	newSnapshot, _ := activateFixture(t, legacySnapshot, fixtureSet("next"), servingEpoch.Add(time.Hour))
	legacyName := "aiand_registry/runtime_state/router_serving/v1/targets/prod-01/prod/stable/state.json"
	newName := "aiand_registry/state/prod-01/prod/stable.json"
	seed := func(name string, state policyregistry.ServingControlState) int64 {
		payload, err := json.Marshal(state)
		require.NoError(t, err)
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.generation++
		fixture.objects[name] = map[int64][]byte{fixture.generation: payload}
		fixture.current[name] = fixture.generation
		return fixture.generation
	}

	_, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.ErrorIs(t, err, policyregistry.ErrNotFound)

	legacyGeneration := seed(legacyName, legacySnapshot.State)
	observed, err := registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: legacySnapshot.State, Generation: legacyGeneration, LegacyPath: true}, observed)

	newGeneration := seed(newName, newSnapshot.State)
	observed, err = registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.NoError(t, err)
	assert.Equal(t, policyregistry.ServingStateSnapshot{State: newSnapshot.State, Generation: newGeneration}, observed, "the new path is authoritative even while the legacy object still exists")

	fixture.mu.Lock()
	fixture.objects[newName] = map[int64][]byte{fixture.current[newName]: []byte(`{"schema_version":"router_serving_control_state_v1"}`)}
	fixture.mu.Unlock()
	_, err = registry.ReadServingState(ctx, policyregistry.TargetStable)
	require.Error(t, err, "an invalid new-path object is a failure, never a silent fall back to legacy state")
}
