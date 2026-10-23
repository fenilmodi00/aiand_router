package policyregistry_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/policyregistry"
)

// legacyV1Snapshot seeds the state a target activated before the layout floor: a v1 selection set
// activated by a v1 proposal, which only history, admission and withdrawal may still read.
func legacyV1Snapshot(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSet) (policyregistry.ServingStateSnapshot, policyregistry.ObjectRef) {
	t.Helper()
	setRef := store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := policyregistry.DeploymentProposal{SchemaVersion: policyregistry.ServingProposalV1, Target: set.Target, SelectionSet: setRef, SourceRelease: set.Default.Release, Scope: policyregistry.ChangeFull, Actor: "legacy-operator", Reason: "activation predating the layout floor", RequestID: "legacy-run:lane-0", CreatedAt: servingEpoch, Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
	require.NoError(t, proposal.Validate(testRegistryRoot))
	ref := store.publish(t, policyregistry.ServingProposals, proposal)
	id := uuid.NewString()
	state := policyregistry.ServingControlState{SchemaVersion: policyregistry.ServingControlStateV1, Target: set.Target, CurrentActivationID: id, Sequence: 1, Activations: map[string]policyregistry.Activation{id: {ID: id, Sequence: 1, SelectionSet: setRef, ActivatedAt: servingEpoch, Proposal: ref, RequestID: proposal.RequestID, Actor: proposal.Actor, WorkflowActor: "legacy-workflow"}}}
	require.NoError(t, state.Validate(testRegistryRoot, set.Target))
	snapshot := policyregistry.ServingStateSnapshot{State: state, Generation: 1}
	store.states[set.Target] = snapshot
	return snapshot, setRef
}
