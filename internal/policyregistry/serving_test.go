package policyregistry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/policyregistry"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/hmm/rosterdata"
	"aiand-os/router/internal/subscriptions/entitlement"
)

const profileKeyOne = "10000000-0000-4000-8000-000000000001"
const profileKeyTwo = "10000000-0000-4000-8000-000000000002"

var servingEpoch = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

func artifactRef(label string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/fixtures/" + label, SHA256: policyregistry.Digest([]byte(label)), Generation: 1}
}

func servingPayload(t *testing.T, manifest policyregistry.ServingManifest) []byte {
	t.Helper()
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	return payload
}

func servingRef(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	payload := servingPayload(t, manifest)
	_, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, kind)
	require.NoError(t, err)
	return servingStoredRef(t, kind, payload)
}

func servingStoredRef(t *testing.T, kind policyregistry.ServingKind, payload []byte) policyregistry.ObjectRef {
	t.Helper()
	digest := policyregistry.Digest(payload)
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
}

// driftedPayload re-encodes canonical manifest bytes the way the drifted registry objects
// were written: a non-Go producer emitted semantically identical JSON with sorted object
// keys instead of Go struct order, plus a trailing newline.
func driftedPayload(t *testing.T, canonical []byte) []byte {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(canonical, &decoded))
	payload, err := json.Marshal(decoded)
	require.NoError(t, err)
	return append(payload, '\n')
}

func namespaceRef(kind policyregistry.ServingKind, label string) policyregistry.ObjectRef {
	digest := policyregistry.Digest([]byte(label))
	return policyregistry.ObjectRef{URI: testRegistryRoot + "/router_serving/v1/" + string(kind) + "/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
}

func fixtureSet(label string) policyregistry.SelectionSet {
	return policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: policyregistry.TargetStable, Default: policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, label), Binding: namespaceRef(policyregistry.ServingBindings, label)}, Profiles: map[string]policyregistry.ServingSelection{}}
}

// fixtureSetV2 is a self-contained v2 selection set whose candidate is referenced but not stored;
// it exercises publish/read plumbing, not lane resolution.
func fixtureSetV2(label string) policyregistry.SelectionSetV2 {
	image := "sha256:" + strings.Repeat("1", 64)
	binding := policyregistry.LaneBinding{Project: "test-project", Region: "test-region",
		Router:      policyregistry.RevisionBinding{Name: "worker-0001", URL: "https://worker-0001.example", Audience: "https://worker.example", ImageDigest: image, Configuration: artifactRef("worker-config")},
		Classifier:  policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: image, Configuration: artifactRef("classifier-config")},
		Attestation: artifactRef("binding-attestation")}
	return policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: policyregistry.TargetStable, Default: policyregistry.ServingLane{Candidate: artifactStoredRef([]byte(label)), LaneBinding: binding}, Profiles: map[string]policyregistry.ServingLane{}}
}

// fixtureProposal proposes the fixture set as a new activation must: the activated selection set
// is addressed in the artifacts/ layout the floor requires, while the bound incumbent keeps
// whatever layout the recorded history used. The proposed set is not stored; transition tests
// never read it.
func fixtureProposal(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposalV2 {
	t.Helper()
	proposal := policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, SelectionSet: artifactStoredRef(servingPayload(t, set)), SourceCandidate: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: now, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	if snapshot.Generation > 0 {
		previous := snapshot.State.Activations[snapshot.State.CurrentActivationID].SelectionSet
		proposal.PreviousSelectionSet = &previous
	}
	return proposal
}

// proposalRef addresses a proposal where every new one is published.
func proposalRef(t *testing.T, proposal policyregistry.DeploymentProposalV2) policyregistry.ObjectRef {
	t.Helper()
	return artifactStoredRef(servingPayload(t, proposal))
}

func activateFixture(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time, withdraw ...string) (policyregistry.ServingStateSnapshot, policyregistry.DeploymentProposalV2) {
	t.Helper()
	proposal := fixtureProposal(t, snapshot, set, now)
	proposal.WithdrawActivations = withdraw
	activated, err := policyregistry.NextServingActivation(snapshot, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "test-workflow", now)
	require.NoError(t, err)
	activated.Snapshot.Generation++
	return activated.Snapshot, proposal
}

// storedProposal folds the fixture set into the artifacts/ layout, publishes the v2 objects a new
// activation must name, and proposes them for the given incumbent snapshot.
func storedProposal(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposalV2 {
	t.Helper()
	proposal := fixtureProposal(t, snapshot, set, now)
	_, setRef := foldSelectionSet(t, store, set)
	proposal.SelectionSet = setRef
	proposal.SourceCandidate = foldCandidate(t, store, set.Default.Release)
	return proposal
}

// variantSet publishes a successor set that differs from its predecessor only in the shared Router
// revision, so consecutive activations bind distinct selection sets.
func variantSet(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet, name string) policyregistry.SelectionSet {
	t.Helper()
	binding := *store.object(t, policyregistry.ServingBindings, set.Default.Binding).(*policyregistry.DeploymentBinding)
	router := binding.Router
	router.Name = name
	router.URL = "https://" + name + ".example"
	next := set
	next.Profiles = make(map[string]policyregistry.ServingSelection, len(set.Profiles))
	next.Default = publishSelection(t, store, set.Default, *store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease), router, binding.Classifier, set.Default.Profile)
	for key, lane := range set.Profiles {
		laneBinding := *store.object(t, policyregistry.ServingBindings, lane.Binding).(*policyregistry.DeploymentBinding)
		next.Profiles[key] = publishSelection(t, store, lane, *store.object(t, policyregistry.ServingReleases, lane.Release).(*policyregistry.ServingRelease), router, laneBinding.Classifier, lane.Profile)
	}
	return next
}

// v1Proposal builds the historical v1 proposal shape, which validation still accepts for
// pre-floor history even though new activations must name an artifacts/ selection set.
func v1Proposal(t *testing.T, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) policyregistry.DeploymentProposal {
	t.Helper()
	proposal := policyregistry.DeploymentProposal{SchemaVersion: policyregistry.ServingProposalV1, Target: set.Target, ExpectedGeneration: snapshot.Generation, SelectionSet: servingRef(t, policyregistry.ServingSelectionSets, set), SourceRelease: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "test-operator", Reason: "release validation", RequestID: uuid.NewString(), CreatedAt: now, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	if snapshot.Generation > 0 {
		previous := snapshot.State.Activations[snapshot.State.CurrentActivationID].SelectionSet
		proposal.PreviousSelectionSet = &previous
	}
	return proposal
}

// storedActivateFixture records an activation whose objects the controller can read back, which
// history validation and admission both need.
func storedActivateFixture(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, now time.Time) (policyregistry.ServingStateSnapshot, policyregistry.DeploymentProposalV2) {
	t.Helper()
	proposal := storedProposal(t, store, snapshot, set, now)
	activated, err := policyregistry.NextServingActivation(snapshot, servingPayload(t, proposal), store.publishArtifact(t, policyregistry.ServingProposal, proposal), testRegistryRoot, "test-workflow", now)
	require.NoError(t, err)
	activated.Snapshot.Generation++
	return activated.Snapshot, proposal
}

// storedViews projects stored v2 selection sets for admission, keyed as activations declare them.
func storedViews(t *testing.T, store *servingMemoryStore, refs ...policyregistry.ObjectRef) map[string]policyregistry.SelectionSetView {
	t.Helper()
	views := make(map[string]policyregistry.SelectionSetView, len(refs))
	for _, ref := range refs {
		set := store.object(t, policyregistry.ServingSelectionSet, ref).(*policyregistry.SelectionSetV2)
		views[ref.SHA256] = set.View(ref)
	}
	return views
}

func TestServingContractsRejectMutableTargetsAndReferences(t *testing.T) {
	for _, target := range []policyregistry.ServingTarget{"beta", "prod/beta", "stable", "staging/aiand-internal", "../stable"} {
		_, err := target.Environment()
		require.Error(t, err)
	}
	for target, environment := range map[policyregistry.ServingTarget]policyregistry.Environment{policyregistry.TargetStaging: policyregistry.EnvironmentStaging, policyregistry.TargetStable: policyregistry.EnvironmentProd, policyregistry.TargetInternal: policyregistry.EnvironmentProd} {
		actual, err := target.Environment()
		require.NoError(t, err)
		assert.Equal(t, environment, actual)
	}
	ref := namespaceRef(policyregistry.ServingReleases, "release")
	require.NoError(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingReleases))
	assert.Error(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingBindings))
	ref.Generation = 0
	assert.Error(t, policyregistry.ValidateServingRef(ref, testRegistryRoot, policyregistry.ServingReleases))
}

func TestServingDecoderRejectsUnknownFieldsSchemaAndTrailingValues(t *testing.T) {
	set := fixtureSet("one")
	payload, err := policyregistry.CanonicalBytes(set)
	require.NoError(t, err)
	for _, invalid := range [][]byte{[]byte(strings.Replace(string(payload), `"schema_version":`, `"unknown":true,"schema_version":`, 1)), []byte(strings.Replace(string(payload), string(policyregistry.ServingSelectionSetV1), "future_v2", 1)), append(append([]byte(nil), payload...), []byte(`{}`)...)} {
		_, err := policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
		require.Error(t, err)
	}
	set.Profiles = nil
	assert.Error(t, set.Validate(testRegistryRoot))
}

func TestServingDecoderAcceptsAnyValidEncodingButNotSemanticDrift(t *testing.T) {
	set := fixtureSet("drifted")
	canonical := servingPayload(t, set)
	drifted := driftedPayload(t, canonical)
	require.NotEqual(t, canonical, drifted)

	for _, payload := range [][]byte{canonical, drifted, append([]byte("  \n"), canonical...)} {
		manifest, err := policyregistry.DecodeServingManifest(payload, testRegistryRoot, policyregistry.ServingSelectionSets)
		require.NoError(t, err)
		require.Equal(t, &set, manifest)
	}

	invalid := bytes.Replace(drifted, []byte(string(policyregistry.ServingSelectionSetV1)), []byte("future_v2"), 1)
	_, err := policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
	require.Error(t, err, "encoding freedom does not extend to schema")

	invalid = bytes.Replace(drifted, []byte(`"schema_version":`), []byte(`"unknown":true,"schema_version":`), 1)
	_, err = policyregistry.DecodeServingManifest(invalid, testRegistryRoot, policyregistry.ServingSelectionSets)
	require.Error(t, err)
}

func TestActivationBindsStoredProposalPayloadDigest(t *testing.T) {
	set := fixtureSet("one")
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	drifted := driftedPayload(t, servingPayload(t, proposal))
	ref := artifactStoredRef(drifted)
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, drifted, ref, testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	require.Equal(t, proposal.RequestID, transition.Activation.RequestID)
	require.Equal(t, ref, transition.Activation.Proposal)

	mismatched := artifactStoredRef(servingPayload(t, proposal))
	_, err = policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, drifted, mismatched, testRegistryRoot, "workflow", servingEpoch)
	require.ErrorContains(t, err, "digest", "replay is keyed on the recorded proposal ref, so its digest must match the activated bytes")
}

// The layout floor is decided by the proposed selection set's layout alone: identical proposals
// differing only in where the set is stored are rejected and accepted respectively.
func TestNextServingActivationFloorsSelectionSetsOutsideTheArtifactsLayout(t *testing.T) {
	set := fixtureSet("one")
	proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	proposal.SelectionSet = servingStoredRef(t, policyregistry.ServingSelectionSets, servingPayload(t, set))
	_, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.ErrorIs(t, err, policyregistry.ErrSelectionSetLayoutFloor)
	require.ErrorContains(t, err, proposal.SelectionSet.SHA256)

	proposal.SelectionSet = artifactStoredRef(servingPayload(t, set))
	transition, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
	require.NoError(t, err)
	require.Equal(t, proposal.SelectionSet, transition.Activation.SelectionSet)
}

func TestActivationIncarnationsNeverResetEarlierRetirement(t *testing.T) {
	firstSet, secondSet := fixtureSet("one"), fixtureSet("two")
	first, firstProposal := activateFixture(t, policyregistry.ServingStateSnapshot{}, firstSet, servingEpoch)
	second, _ := activateFixture(t, first, secondSet, servingEpoch.Add(time.Hour))
	third, _ := activateFixture(t, second, firstSet, servingEpoch.Add(2*time.Hour))
	firstID, thirdID := first.State.CurrentActivationID, third.State.CurrentActivationID
	assert.NotEqual(t, firstID, thirdID)
	assert.Equal(t, third.State.Activations[firstID].SelectionSet, third.State.Activations[thirdID].SelectionSet)
	assert.Equal(t, servingEpoch.Add(time.Hour), *third.State.Activations[firstID].SupersededAt)
	assert.Nil(t, first.State.Activations[firstID].SupersededAt, "pure transition must not mutate the input snapshot")
	assert.Equal(t, int64(3), third.State.Sequence)
	retry, err := policyregistry.NextServingActivation(third, servingPayload(t, firstProposal), proposalRef(t, firstProposal), testRegistryRoot, "retry-workflow", servingEpoch.Add(3*time.Hour))
	require.NoError(t, err)
	assert.True(t, retry.Replayed)
	assert.Equal(t, policyregistry.ActivationSuperseded, retry.Outcome)
	assert.Equal(t, firstID, retry.Activation.ID)
	assert.Equal(t, thirdID, retry.Snapshot.State.CurrentActivationID)
}

func TestActivationRejectsStalePreview(t *testing.T) {
	set := fixtureSet("one")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	stale := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	_, err := policyregistry.NextServingActivation(first, servingPayload(t, stale), proposalRef(t, stale), testRegistryRoot, "workflow", servingEpoch)
	require.ErrorIs(t, err, policyregistry.ErrConflict)
}

func TestActivationReplayIsKeyedOnProposalRefNotRequestID(t *testing.T) {
	set := fixtureSet("one")
	first, firstProposal := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	firstID := first.State.CurrentActivationID

	secondProposal := fixtureProposal(t, first, fixtureSet("two"), servingEpoch.Add(time.Hour))
	secondProposal.RequestID = firstProposal.RequestID
	secondRef := proposalRef(t, secondProposal)
	second, err := policyregistry.NextServingActivation(first, servingPayload(t, secondProposal), secondRef, testRegistryRoot, "workflow", servingEpoch.Add(time.Hour))
	require.NoError(t, err, "a different proposal sharing a request ID is a second activation, not a conflict")
	require.False(t, second.Replayed)
	require.NotEqual(t, firstID, second.Activation.ID)
	require.Equal(t, int64(2), second.Activation.Sequence)
	require.Equal(t, firstProposal.RequestID, second.Activation.RequestID)
	require.Equal(t, firstProposal.RequestID, second.Snapshot.State.Activations[firstID].RequestID)
	require.NoError(t, second.Snapshot.State.Validate(testRegistryRoot, set.Target), "shared request IDs are audit metadata, not a state invariant")

	second.Snapshot.Generation++
	replay, err := policyregistry.NextServingActivation(second.Snapshot, servingPayload(t, secondProposal), secondRef, testRegistryRoot, "retry-workflow", servingEpoch.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, second.Activation.ID, replay.Activation.ID)
	require.Equal(t, policyregistry.ActivationCurrent, replay.Outcome)
	require.Equal(t, second.Snapshot, replay.Snapshot, "replay must not build a new transition")
}

func TestServingRequestIDIsAnyNonEmptyBoundedString(t *testing.T) {
	set := fixtureSet("one")
	for _, requestID := range []string{"run-123456789:lane-0", "a", strings.Repeat("x", policyregistry.MaxServingRequestIDLength)} {
		proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		proposal.RequestID = requestID
		require.NoError(t, proposal.Validate(testRegistryRoot))
		activated, err := policyregistry.NextServingActivation(policyregistry.ServingStateSnapshot{}, servingPayload(t, proposal), proposalRef(t, proposal), testRegistryRoot, "workflow", servingEpoch)
		require.NoError(t, err)
		require.Equal(t, requestID, activated.Activation.RequestID)
		require.NoError(t, activated.Snapshot.State.Validate(testRegistryRoot, set.Target))
	}
	for _, requestID := range []string{"", "   ", strings.Repeat("x", policyregistry.MaxServingRequestIDLength+1)} {
		proposal := fixtureProposal(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		proposal.RequestID = requestID
		require.ErrorContains(t, proposal.Validate(testRegistryRoot), "request ID")
		activated, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
		current := activated.State.Activations[activated.State.CurrentActivationID]
		current.RequestID = requestID
		activated.State.Activations[current.ID] = current
		require.ErrorContains(t, activated.State.Validate(testRegistryRoot, set.Target), "request identity")
	}
}

func TestSessionReleaseIdleAndSupersessionBoundaries(t *testing.T) {
	firstSet, secondSet := fixtureSet("one"), fixtureSet("two")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, firstSet, servingEpoch)
	second, _ := activateFixture(t, first, secondSet, servingEpoch.Add(time.Hour))
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, firstSet).SHA256: firstSet.View(), servingRef(t, policyregistry.ServingSelectionSets, secondSet).SHA256: secondSet.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
	initial, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	initial.RoutingModel = router.ModelQwen
	for _, tc := range []struct {
		name          string
		now           time.Time
		lastAdmission time.Time
		retained      bool
	}{
		{"before_idle", servingEpoch.Add(24*time.Hour - time.Nanosecond), servingEpoch, true},
		{"at_idle", servingEpoch.Add(24 * time.Hour), servingEpoch, false},
		{"before_retirement", servingEpoch.Add(time.Hour + 7*24*time.Hour - time.Nanosecond), servingEpoch.Add(7 * 24 * time.Hour), true},
		{"at_retirement", servingEpoch.Add(time.Hour + 7*24*time.Hour), servingEpoch.Add(7 * 24 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := initial
			previous.LastAdmittedAt = tc.lastAdmission
			admitted, err := policyregistry.SelectSessionRelease(&previous, projection, second, sets, testRegistryRoot, tc.now)
			require.NoError(t, err)
			if tc.retained {
				assert.Equal(t, first.State.CurrentActivationID, admitted.ActivationID)
				assert.Equal(t, int64(1), admitted.BindingGeneration)
			} else {
				assert.Equal(t, second.State.CurrentActivationID, admitted.ActivationID)
				assert.Equal(t, int64(2), admitted.BindingGeneration)
			}
			assert.Equal(t, tc.now, admitted.LastAdmittedAt)
			assert.Equal(t, servingEpoch, admitted.CreatedAt)
			assert.Equal(t, router.ModelQwen, admitted.RoutingModel, "same-target release replacement preserves the classifier selection")
		})
	}
}

func TestEmergencyWithdrawalRebindsNextAdmissionButDoesNotMutateInFlight(t *testing.T) {
	set := fixtureSet("one")
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
	inFlight, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	rollback, _ := activateFixture(t, first, set, servingEpoch.Add(time.Minute), first.State.CurrentActivationID)
	next, err := policyregistry.SelectSessionRelease(&inFlight, projection, rollback, sets, testRegistryRoot, servingEpoch.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, first.State.CurrentActivationID, inFlight.ActivationID)
	assert.Equal(t, rollback.State.CurrentActivationID, next.ActivationID)
	assert.Equal(t, inFlight.Selection, next.Selection)
	assert.Equal(t, int64(2), next.BindingGeneration)
	assert.Equal(t, rollback.State.CurrentActivationID, rollback.State.Activations[inFlight.ActivationID].ReplacementID)
}

func TestEmergencyWithdrawalPreservesSubscriberPlanAndProfile(t *testing.T) {
	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		t.Run(string(plan), func(t *testing.T) {
			profile, ok := entitlement.ServingProfileFor(plan)
			require.True(t, ok)
			previousSet, replacementSet := fixtureSet("old-worker"), fixtureSet("ready-worker")
			profileRef := namespaceRef(policyregistry.ServingProfiles, "unchanged-profile")
			previousSet.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "old-profile-worker"), Binding: namespaceRef(policyregistry.ServingBindings, "old-profile-worker"), Profile: &profileRef}
			replacementSet.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "ready-profile-worker"), Binding: namespaceRef(policyregistry.ServingBindings, "ready-profile-worker"), Profile: &profileRef}
			first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, previousSet, servingEpoch)
			sets := map[string]policyregistry.SelectionSetView{
				servingRef(t, policyregistry.ServingSelectionSets, previousSet).SHA256:    previousSet.View(),
				servingRef(t, policyregistry.ServingSelectionSets, replacementSet).SHA256: replacementSet.View(),
			}
			projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, ProfileKey: profile.Key, ProfileName: profile.Name, Plan: plan, EntitlementVersion: 3, EnrollmentGeneration: 2, AssignmentGeneration: 4}
			inFlight, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
			require.NoError(t, err)
			replacement, _ := activateFixture(t, first, replacementSet, servingEpoch.Add(time.Minute), first.State.CurrentActivationID)
			next, err := policyregistry.SelectSessionRelease(&inFlight, projection, replacement, sets, testRegistryRoot, servingEpoch.Add(2*time.Minute))
			require.NoError(t, err)
			assert.Equal(t, first.State.CurrentActivationID, inFlight.ActivationID)
			assert.Equal(t, previousSet.Profiles[profile.Key], inFlight.Selection)
			assert.Equal(t, replacement.State.CurrentActivationID, next.ActivationID)
			assert.Equal(t, replacementSet.Profiles[profile.Key], next.Selection)
			assert.Equal(t, profile.Key, next.ProfileKey)
			assert.Equal(t, profile.Name, next.ProfileName)
			assert.Equal(t, plan, next.Plan)
			assert.Equal(t, int64(3), next.EntitlementVersion)
			assert.Equal(t, int64(2), next.EnrollmentGeneration)
			assert.Equal(t, int64(4), next.AssignmentGeneration)
			assert.Equal(t, int64(2), next.BindingGeneration)
			assert.Equal(t, servingEpoch, next.CreatedAt)
		})
	}
}

func TestProfileAssignmentsDoNotFallBackAndGenerationChangesRebind(t *testing.T) {
	set := fixtureSet("one")
	profile := namespaceRef(policyregistry.ServingProfiles, "profile")
	set.Profiles[profileKeyOne] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "custom"), Binding: namespaceRef(policyregistry.ServingBindings, "custom"), Profile: &profile}
	first, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, ProfileKey: profileKeyOne, AssignmentGeneration: 1}
	initial, err := policyregistry.SelectSessionRelease(nil, projection, first, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	assert.Equal(t, set.Profiles[profileKeyOne], initial.Selection)
	projection.AssignmentGeneration++
	next, err := policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), next.BindingGeneration)
	assert.Equal(t, profileKeyOne, next.ProfileKey)
	projection.ProfileKey = profileKeyTwo
	_, err = policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.ErrorContains(t, err, "default fallback is forbidden")
	initial.ActivationID = uuid.NewString()
	_, err = policyregistry.SelectSessionRelease(&initial, projection, first, sets, testRegistryRoot, servingEpoch.Add(time.Minute))
	require.ErrorContains(t, err, "unknown activation")
}

func TestSubscriberPlanProfileMustMatchServerOwnedMapping(t *testing.T) {
	profile, ok := entitlement.ServingProfileFor(entitlement.PlanMax)
	require.True(t, ok)
	set := fixtureSet("one")
	profileRef := namespaceRef(policyregistry.ServingProfiles, "max-profile")
	set.Profiles[profile.Key] = policyregistry.ServingSelection{Release: namespaceRef(policyregistry.ServingReleases, "max"), Binding: namespaceRef(policyregistry.ServingBindings, "max"), Profile: &profileRef}
	snapshot, _ := activateFixture(t, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	sets := map[string]policyregistry.SelectionSetView{servingRef(t, policyregistry.ServingSelectionSets, set).SHA256: set.View()}
	projection := policyregistry.AdmissionProjection{
		Target:               policyregistry.TargetStable,
		ProfileKey:           profile.Key,
		ProfileName:          profile.Name,
		Plan:                 entitlement.PlanMax,
		EntitlementVersion:   3,
		AssignmentGeneration: 3,
	}

	admitted, err := policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.NoError(t, err)
	assert.Equal(t, profile.Name, admitted.ProfileName)
	assert.Equal(t, entitlement.PlanMax, admitted.Plan)
	assert.Equal(t, int64(3), admitted.EntitlementVersion)

	projection.ProfileName = "customer-profile"
	_, err = policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.ErrorContains(t, err, "subscriber plan profile projection is invalid")
	projection.ProfileName = profile.Name
	projection.ProfileKey = profileKeyOne
	_, err = policyregistry.SelectSessionRelease(nil, projection, snapshot, sets, testRegistryRoot, servingEpoch)
	require.ErrorContains(t, err, "subscriber plan profile projection is invalid")
}

type servingMemoryStore struct {
	mu       sync.Mutex
	objects  map[policyregistry.ObjectRef][]byte
	policies map[policyregistry.ObjectRef]*rosterdata.Roster
	states   map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot
	// legacyStates models runtime_state/... objects: read only when no state/ object exists, never written.
	legacyStates map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot
	readErr      error
	casErr       error
	casCalls     int
	artifacts    map[policyregistry.ObjectRef][]byte
}

func newServingMemoryStore() *servingMemoryStore {
	artifacts := make(map[policyregistry.ObjectRef][]byte)
	for _, label := range []string{"build-attestation", "binding-attestation", "evidence"} {
		artifacts[artifactRef(label)] = []byte(label)
	}
	return &servingMemoryStore{objects: make(map[policyregistry.ObjectRef][]byte), policies: make(map[policyregistry.ObjectRef]*rosterdata.Roster), states: make(map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot), legacyStates: make(map[policyregistry.ServingTarget]policyregistry.ServingStateSnapshot), artifacts: artifacts}
}

func (s *servingMemoryStore) VerifyServingArtifact(_ context.Context, ref policyregistry.ObjectRef) error {
	payload, exists := s.artifacts[ref]
	if !exists {
		return policyregistry.ErrNotFound
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("artifact digest mismatch")
	}
	return nil
}

func (s *servingMemoryStore) RootURI() string { return testRegistryRoot }
func (s *servingMemoryStore) ReadServingObject(_ context.Context, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) (policyregistry.ServingManifest, []byte, error) {
	payload, exists := s.objects[ref]
	if !exists {
		return nil, nil, policyregistry.ErrNotFound
	}
	manifest, err := policyregistry.DecodeServingObject(payload, testRegistryRoot, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}
func (s *servingMemoryStore) ReadServingPolicy(_ context.Context, ref policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	policy, exists := s.policies[ref]
	if !exists {
		return nil, policyregistry.ErrNotFound
	}
	return policy, nil
}
func (s *servingMemoryStore) ReadServingState(_ context.Context, target policyregistry.ServingTarget) (policyregistry.ServingStateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return policyregistry.ServingStateSnapshot{}, s.readErr
	}
	if snapshot, exists := s.states[target]; exists {
		return snapshot, nil
	}
	if snapshot, exists := s.legacyStates[target]; exists {
		snapshot.LegacyPath = true
		return snapshot, nil
	}
	return policyregistry.ServingStateSnapshot{}, policyregistry.ErrNotFound
}
func (s *servingMemoryStore) CompareAndSwapServingState(_ context.Context, next policyregistry.ServingControlState, expected int64) (policyregistry.ServingStateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casCalls++
	if s.casErr != nil {
		return policyregistry.ServingStateSnapshot{}, s.casErr
	}
	if s.states[next.Target].Generation != expected {
		return policyregistry.ServingStateSnapshot{}, policyregistry.ErrConflict
	}
	snapshot := policyregistry.ServingStateSnapshot{State: next, Generation: expected + 1}
	s.states[next.Target] = snapshot
	return snapshot, nil
}
func (s *servingMemoryStore) publish(t *testing.T, kind policyregistry.ServingKind, manifest policyregistry.ServingManifest) policyregistry.ObjectRef {
	t.Helper()
	ref := servingRef(t, kind, manifest)
	payload, err := policyregistry.CanonicalBytes(manifest)
	require.NoError(t, err)
	s.putRaw(ref, payload)
	return ref
}

// putRaw stores arbitrary bytes under a reference without publish-time validation.
func (s *servingMemoryStore) putRaw(ref policyregistry.ObjectRef, payload []byte) {
	s.objects[ref] = payload
}

// object decodes a stored payload for test assertions without going through ServingStore reads.
func (s *servingMemoryStore) object(t *testing.T, kind policyregistry.ServingKind, ref policyregistry.ObjectRef) policyregistry.ServingManifest {
	t.Helper()
	manifest, _, err := s.ReadServingObject(context.Background(), kind, ref)
	require.NoError(t, err)
	return manifest
}

type preparedValidator func(context.Context, policyregistry.PreparedSelection) error

func (f preparedValidator) ValidatePreparedSelection(ctx context.Context, selection policyregistry.PreparedSelection) error {
	return f(ctx, selection)
}

func controllerFixture(t *testing.T) (*servingMemoryStore, *policyregistry.ServingController, policyregistry.SelectionSet) {
	t.Helper()
	store := newServingMemoryStore()
	legacy := validLoader(t)
	policyBytes, err := rosterdata.CanonicalBytes(legacy.policy)
	require.NoError(t, err)
	policyDigest := policyregistry.Digest(policyBytes)
	policyRef := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_policy/v1/policies/sha256/" + policyDigest + ".json", SHA256: policyDigest, Generation: 1}
	store.policies[policyRef] = legacy.policy
	packageRef := artifactRef("classifier-package")
	identity := legacy.release.Classifier
	identity.PackageSHA256 = packageRef.SHA256
	bundle := policyregistry.ClassifierBundle{SchemaVersion: policyregistry.ServingClassifierV1, Identity: identity, Package: packageRef, Configuration: artifactRef("classifier-config"), AuxiliaryModels: map[string]policyregistry.ObjectRef{"escalation": artifactRef("escalation")}}
	bundleRef := store.publish(t, policyregistry.ServingClassifiers, bundle)
	release := policyregistry.ServingRelease{SchemaVersion: policyregistry.ServingReleaseV1, RouterImageDigest: "sha256:" + strings.Repeat("1", 64), Policy: policyregistry.PolicyObject{URI: policyRef.URI, SHA256: policyRef.SHA256, Generation: policyRef.Generation, SchemaVersion: legacy.policy.SchemaVersion}, Classifier: bundleRef, Requirements: policyregistry.ServingRequirements{RuntimeContract: policyregistry.ManagedRuntimeContractV1, PolicySchema: legacy.policy.SchemaVersion, ClassifierWireSchema: identity.WireSchema, TaxonomySHA256: identity.TaxonomySHA256}, Provenance: policyregistry.ServingProvenance{RouterRevision: strings.Repeat("2", 40), AiandRevision: strings.Repeat("3", 40), BuildAttestation: artifactRef("build-attestation")}}
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	binding := policyregistry.DeploymentBinding{SchemaVersion: policyregistry.ServingBindingV1, Target: policyregistry.TargetStable, Project: "test-project", Region: "test-region", Release: releaseRef, Router: policyregistry.RevisionBinding{Name: "worker-0001", URL: "https://worker-0001.example", Audience: "https://worker.example", ImageDigest: release.RouterImageDigest, Configuration: artifactRef("worker-config")}, Classifier: policyregistry.RevisionBinding{Name: "classifier-0001", URL: "https://classifier-0001.example", Audience: "https://classifier.example", ImageDigest: identity.ImageDigest, Configuration: bundle.Configuration}, ClassifierBundleSHA256: bundleRef.SHA256, Attestation: artifactRef("binding-attestation")}
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	set := policyregistry.SelectionSet{SchemaVersion: policyregistry.ServingSelectionSetV1, Target: policyregistry.TargetStable, Default: policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef}, Profiles: map[string]policyregistry.ServingSelection{}}
	store.publish(t, policyregistry.ServingSelectionSets, set)
	validator := preparedValidator(func(_ context.Context, selection policyregistry.PreparedSelection) error {
		if selection.Binding.Router.ImageDigest != "sha256:"+strings.Repeat("1", 64) {
			return errors.New("worker attestation mismatch")
		}
		if selection.Candidate.Classifier.AuxiliaryModels["escalation"] != artifactRef("escalation") {
			return errors.New("auxiliary model mismatch")
		}
		return nil
	})
	controller, err := policyregistry.NewServingController(store, validator, func() time.Time { return servingEpoch }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return store, controller, set
}

func registerProfileFixture(t *testing.T, store *servingMemoryStore, base policyregistry.ServingSelection, key string, policy policyregistry.PolicyObject) policyregistry.ServingSelection {
	t.Helper()
	release := *store.object(t, policyregistry.ServingReleases, base.Release).(*policyregistry.ServingRelease)
	release.Policy = policy
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	profileRef := store.publish(t, policyregistry.ServingProfiles, policyregistry.RoutingProfile{SchemaVersion: policyregistry.ServingProfileV1, ProfileKey: key, Policy: policy, Requirements: release.Requirements})
	binding := *store.object(t, policyregistry.ServingBindings, base.Binding).(*policyregistry.DeploymentBinding)
	binding.Release = releaseRef
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	return policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef, Profile: &profileRef}
}
