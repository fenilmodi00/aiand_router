package policyregistry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/subscriptions/entitlement"
)

type testLaunchRepo struct {
	identity      policyregistry.TestPlanIdentity
	launch        policyregistry.TestPlanLaunch
	hash, session string
	revoked       bool
	missingBudget bool
}

func (r *testLaunchRepo) ListTestIdentities(context.Context) ([]policyregistry.TestPlanIdentity, error) {
	return []policyregistry.TestPlanIdentity{r.identity}, nil
}
func (r *testLaunchRepo) GetTestIdentity(context.Context, string) (policyregistry.TestPlanIdentity, error) {
	if r.missingBudget {
		return policyregistry.TestPlanIdentity{}, errors.New("missing isolated budget")
	}
	return r.identity, nil
}
func (r *testLaunchRepo) SaveTestLaunch(_ context.Context, launch policyregistry.TestPlanLaunch, hash string) error {
	r.launch = launch
	r.hash = hash
	return nil
}
func (r *testLaunchRepo) AuthorizeTestLaunch(_ context.Context, hash, installation, key, session string) (policyregistry.TestPlanLaunch, error) {
	if hash != r.hash || r.revoked || installation != r.identity.InstallationID || key != "test-key" || (r.session != "" && r.session != session) {
		return policyregistry.TestPlanLaunch{}, errors.New("unauthorized grant")
	}
	r.session = session
	return r.launch, nil
}
func (r *testLaunchRepo) RevokeTestLaunch(context.Context, string) error {
	r.revoked = true
	return nil
}

func testPlanFixture(t *testing.T) (*policyregistry.TestPlanTools, *testLaunchRepo, *servingMemoryStore, *time.Time, policyregistry.SelectionSet) {
	t.Helper()
	store, _, set := controllerFixture(t)
	release := store.object(t, policyregistry.ServingReleases, set.Default.Release).(*policyregistry.ServingRelease)
	for _, plan := range []entitlement.Plan{entitlement.PlanMax, entitlement.PlanBoost} {
		profile, _ := entitlement.ServingProfileFor(plan)
		set.Profiles[profile.Key] = registerProfileFixture(t, store, set.Default, profile.Key, release.Policy)
	}
	snapshot, _ := storedActivateFixture(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	store.states[policyregistry.TargetStable] = snapshot
	now := servingEpoch.Add(time.Minute)
	repo := &testLaunchRepo{identity: policyregistry.TestPlanIdentity{SubjectID: uuid.NewString(), InstallationID: "test-installation", Label: "Synthetic enrolled internal identity", BalanceMicros: 1000000, EnrollmentGeneration: 3}}
	return &policyregistry.TestPlanTools{Repository: repo, Store: store, Clock: func() time.Time { return now }}, repo, store, &now, set
}
