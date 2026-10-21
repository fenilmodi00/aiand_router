package policyregistry_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

const alternateRosterArm = "openai/gpt-6-sol"

func publishRosterArm(t *testing.T, store *servingMemoryStore, base policyregistry.PolicyObject, arm string) policyregistry.PolicyObject {
	t.Helper()
	roster := store.policies[policyregistry.ObjectRef{URI: base.URI, SHA256: base.SHA256, Generation: base.Generation}]
	payload, err := rosterdata.CanonicalBytes(roster)
	require.NoError(t, err)
	payload = bytes.ReplaceAll(payload, []byte(roster.AllArms()[0]), []byte(arm))
	changed, err := rosterdata.ParseValidated(payload)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{URI: testRegistryRoot + "/router_policy/v1/policies/sha256/" + digest + ".json", SHA256: digest, Generation: 1}
	store.policies[ref] = changed
	return policyregistry.PolicyObject{URI: ref.URI, SHA256: ref.SHA256, Generation: ref.Generation, SchemaVersion: changed.SchemaVersion}
}

func cohortFixture(t *testing.T) (*servingMemoryStore, policyregistry.SelectionSetV3) {
	t.Helper()
	store, _, legacy := controllerFixture(t)
	lane := foldLane(t, store, legacy.Default)
	candidate := store.object(t, policyregistry.ServingCandidate, lane.Candidate).(*policyregistry.CandidateV2)
	set := policyregistry.SelectionSetV3{SchemaVersion: policyregistry.ServingSelectionSetV3, Target: legacy.Target,
		Code: policyregistry.CodeBinding{Candidate: lane.Candidate, LaneBinding: lane.LaneBinding}, DefaultPolicy: candidate.Policy,
		Profiles: map[string]policyregistry.PolicyObject{profileKeyOne: candidate.Policy, profileKeyTwo: publishRosterArm(t, store, candidate.Policy, alternateRosterArm)}}
	return store, set
}

func cohortProposal(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSetV3, previous *policyregistry.ObjectRef, scope policyregistry.ChangeScope, profile string) policyregistry.DeploymentProposalV2 {
	t.Helper()
	return policyregistry.DeploymentProposalV2{SchemaVersion: policyregistry.ServingProposalV2, Target: set.Target, PreviousSelectionSet: previous,
		SelectionSet: store.publishArtifact(t, policyregistry.ServingSelectionSet, set), SourceCandidate: set.Code.Candidate, Scope: scope, ProfileKey: profile,
		Actor: "test-operator", Reason: "cohort configuration", RequestID: "synthetic-cohort-release", CreatedAt: servingEpoch,
		Evidence: []policyregistry.ObjectRef{artifactRef("evidence")}, WithdrawActivations: []string{}}
}
