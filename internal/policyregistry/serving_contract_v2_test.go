package policyregistry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
)

// artifactStoredRef addresses published bytes in the flat v2 layout.
func artifactStoredRef(payload []byte) policyregistry.ObjectRef {
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/artifacts/" + digest + ".json", SHA256: digest, Generation: 1}
}

// publishArtifact stores a v2 manifest under artifacts/ after proving it decodes for its family kind.
func (s *servingMemoryStore) publishArtifact(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	payload := servingPayload(t, manifest)
	_, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, kind)
	require.NoError(t, err)
	ref := artifactStoredRef(payload)
	s.putRaw(ref, payload)
	return ref
}

// v2Fixture expresses the controller fixture's v1 composition (release + classifier bundle +
// deployment binding, plus one registered profile) as a v2 candidate and selection set.
type v2Fixture struct {
	v1        policyregistry.SelectionSet
	candidate policyregistry.ObjectRef
	set       policyregistry.SelectionSetV2
	setRef    policyregistry.ObjectRef
}

func (f v2Fixture) selection(profileKey string) policyregistry.ServingSelection {
	if profileKey == "" {
		return policyregistry.ServingSelection{Release: f.set.Default.Candidate, Binding: f.setRef}
	}
	profile := f.setRef
	return policyregistry.ServingSelection{Release: f.set.Profiles[profileKey].Candidate, Binding: f.setRef, Profile: &profile}
}

// foldCandidate publishes the v2 candidate equivalent to a v1 release and its classifier bundle.
func foldCandidate(t *testing.T, store *servingMemoryStore, releaseRef policyregistry.ObjectRef) policyregistry.ObjectRef {
	t.Helper()
	release := store.object(t, policyregistry.ServingReleases, releaseRef).(*policyregistry.ServingRelease)
	bundle := store.object(t, policyregistry.ServingClassifiers, release.Classifier).(*policyregistry.ClassifierBundle)
	candidate := policyregistry.CandidateV2{SchemaVersion: policyregistry.ServingCandidateV2, CandidateComposition: policyregistry.CandidateComposition{
		RouterImageDigest: release.RouterImageDigest,
		Policy:            release.Policy,
		Classifier:        policyregistry.ClassifierComponent{Identity: bundle.Identity, Package: bundle.Package, AuxiliaryModels: bundle.AuxiliaryModels, Configuration: bundle.Configuration},
		Requirements:      release.Requirements,
		Provenance:        release.Provenance,
	}}
	return store.publishArtifact(t, policyregistry.ServingCandidate, candidate)
}

// foldLane expresses a v1 selection (release + binding + optional profile objects) as a v2 lane.
func foldLane(t *testing.T, store *servingMemoryStore, selection policyregistry.ServingSelection) policyregistry.ServingLane {
	t.Helper()
	binding := store.object(t, policyregistry.ServingBindings, selection.Binding).(*policyregistry.DeploymentBinding)
	lane := policyregistry.ServingLane{Candidate: foldCandidate(t, store, selection.Release), LaneBinding: policyregistry.LaneBinding{Project: binding.Project, Region: binding.Region, Router: binding.Router, Classifier: binding.Classifier, Attestation: binding.Attestation}}
	if selection.Profile != nil {
		profile := store.object(t, policyregistry.ServingProfiles, *selection.Profile).(*policyregistry.RoutingProfile)
		policy, requirements := profile.Policy, profile.Requirements
		lane.ProfileKey, lane.ProfilePolicy, lane.ProfileRequirements = profile.ProfileKey, &policy, &requirements
	}
	return lane
}

// foldSelectionSet publishes the v2 selection set equivalent to a v1 set whose objects are stored.
func foldSelectionSet(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) (policyregistry.SelectionSetV2, policyregistry.ObjectRef) {
	t.Helper()
	v2 := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Default: foldLane(t, store, set.Default), Profiles: make(map[string]policyregistry.ServingLane, len(set.Profiles))}
	for key, selection := range set.Profiles {
		v2.Profiles[key] = foldLane(t, store, selection)
	}
	return v2, store.publishArtifact(t, policyregistry.ServingSelectionSet, v2)
}

// foldProposal republishes a v1 proposal as v2: its selection set and source fold to artifacts/
// while previous_selection_set keeps whatever (possibly v1) reference the history recorded.
func foldProposal(t *testing.T, store *servingMemoryStore, proposal policyregistry.DeploymentProposal) (policyregistry.DeploymentProposalV2, policyregistry.ObjectRef) {
	t.Helper()
	set := store.object(t, policyregistry.ServingSelectionSets, proposal.SelectionSet).(*policyregistry.SelectionSet)
	_, setRef := foldSelectionSet(t, store, *set)
	v2 := policyregistry.DeploymentProposalV2{
		SchemaVersion:        policyregistry.ServingProposalV2,
		Target:               proposal.Target,
		PreviousSelectionSet: proposal.PreviousSelectionSet,
		SelectionSet:         setRef,
		SourceCandidate:      foldCandidate(t, store, proposal.SourceRelease),
		Scope:                proposal.Scope,
		ProfileKey:           proposal.ProfileKey,
		Actor:                proposal.Actor,
		Reason:               proposal.Reason,
		RequestID:            proposal.RequestID,
		CreatedAt:            proposal.CreatedAt,
		Evidence:             proposal.Evidence,
		WithdrawActivations:  proposal.WithdrawActivations,
	}
	return v2, store.publishArtifact(t, policyregistry.ServingProposal, v2)
}

func newV2Fixture(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) v2Fixture {
	t.Helper()
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	set.Profiles[profileKeyOne] = registerProfileFixture(t, store, set.Default, profileKeyOne, release.Policy)
	store.publish(t, policyregistry.ServingSelectionSets, set)
	v2, setRef := foldSelectionSet(t, store, set)
	return v2Fixture{v1: set, candidate: v2.Default.Candidate, set: v2, setRef: setRef}
}

func fixtureProposalV2(fixture v2Fixture, previous *policyregistry.ObjectRef) policyregistry.DeploymentProposalV2 {
	return policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: fixture.set.Target, PreviousSelectionSet: previous, SelectionSet: fixture.setRef, SourceCandidate: fixture.candidate, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: servingEpoch, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
}

func TestServingRefAcceptsBothLayoutsOnlyForTheKindThatFoldsThem(t *testing.T) {
	artifact := artifactStoredRef([]byte("object"))
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingCandidate, policyregistry.ServingSelectionSet, policyregistry.ServingProposal} {
		assert.NoError(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), kind)
	}
	for _, kind := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
		assert.ErrorContains(t, policyregistry.ValidateServingRef(artifact, testRegistryRoot, kind), "namespace", kind)
	}
	folds := map[policyregistry.ServingKind]policyregistry.ServingKind{policyregistry.ServingCandidate: policyregistry.ServingReleases, policyregistry.ServingSelectionSet: policyregistry.ServingSelectionSets, policyregistry.ServingProposal: policyregistry.ServingProposals}
	for family, legacy := range folds {
		assert.NoError(t, policyregistry.ValidateServingRef(namespaceRef(legacy, "object"), testRegistryRoot, family))
		for _, other := range []policyregistry.ServingKind{policyregistry.ServingReleases, policyregistry.ServingClassifiers, policyregistry.ServingBindings, policyregistry.ServingProfiles, policyregistry.ServingSelectionSets, policyregistry.ServingProposals} {
			if other != legacy {
				assert.Error(t, policyregistry.ValidateServingRef(namespaceRef(other, "object"), testRegistryRoot, family), "%s must not accept %s", family, other)
			}
		}
	}
	outside := artifact
	outside.URI = "gs://weave_ml/other_registry/artifacts/" + artifact.SHA256 + ".json"
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
	legacyName := "weave_registry/runtime_state/router_serving/v1/targets/prod-01/prod/stable/state.json"
	newName := "weave_registry/state/prod-01/prod/stable.json"
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
