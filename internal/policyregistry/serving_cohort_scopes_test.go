package policyregistry_test

import (
	"testing"

	"aiand-os/router/internal/policyregistry"
)

func expandCohortV2(t *testing.T, store *servingMemoryStore, set policyregistry.SelectionSetV3) policyregistry.SelectionSetV2 {
	t.Helper()
	candidate := *store.object(t, policyregistry.ServingCandidate, set.Code.Candidate).(*policyregistry.CandidateV2)
	lane := func(key string, policy policyregistry.PolicyObject) policyregistry.ServingLane {
		configured := candidate
		configured.Policy = policy
		selection := policyregistry.ServingLane{Candidate: store.publishArtifact(t, policyregistry.ServingCandidate, configured), LaneBinding: set.Code.LaneBinding}
		if key != "" {
			selection.ProfileKey = key
			selection.ProfilePolicy = &policy
			requirements := candidate.Requirements
			selection.ProfileRequirements = &requirements
		}
		return selection
	}
	v2 := policyregistry.SelectionSetV2{SchemaVersion: policyregistry.ServingSelectionSetV2, Target: set.Target, Default: lane("", set.DefaultPolicy), Profiles: map[string]policyregistry.ServingLane{}}
	for key, policy := range set.Profiles {
		v2.Profiles[key] = lane(key, policy)
	}
	return v2
}
