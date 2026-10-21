package policyregistry_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func publishChangedPolicy(t *testing.T, store *servingMemoryStore, base *rosterdata.Roster, bonus float64) policyregistry.PolicyObject {
	t.Helper()
	payload, err := rosterdata.CanonicalBytes(base)
	require.NoError(t, err)
	changed, err := rosterdata.ParseValidated(payload)
	require.NoError(t, err)
	changed.Preferences.PreferredModelBonus = bonus
	payload, err = rosterdata.CanonicalBytes(changed)
	require.NoError(t, err)
	digest := policyregistry.Digest(payload)
	ref := policyregistry.ObjectRef{
		URI:        testRegistryRoot + "/router_policy/v1/policies/sha256/" + digest + ".json",
		SHA256:     digest,
		Generation: 1,
	}
	store.policies[ref] = changed
	return policyregistry.PolicyObject{URI: ref.URI, SHA256: ref.SHA256, Generation: ref.Generation, SchemaVersion: changed.SchemaVersion}
}

func publishSelection(
	t *testing.T,
	store *servingMemoryStore,
	template policyregistry.ServingSelection,
	release policyregistry.ServingRelease,
	router policyregistry.RevisionBinding,
	classifier policyregistry.RevisionBinding,
	profile *policyregistry.ObjectRef,
) policyregistry.ServingSelection {
	t.Helper()
	releaseRef := store.publish(t, policyregistry.ServingReleases, release)
	binding := *store.object(t, policyregistry.ServingBindings, template.Binding).(*policyregistry.DeploymentBinding)
	binding.Release = releaseRef
	binding.Router = router
	binding.Classifier = classifier
	binding.ClassifierBundleSHA256 = release.Classifier.SHA256
	bindingRef := store.publish(t, policyregistry.ServingBindings, binding)
	return policyregistry.ServingSelection{Release: releaseRef, Binding: bindingRef, Profile: profile}
}

// proposalShape selects the history a matrix row activates onto: the v1 state a target recorded
// before the layout floor (the layout transition), or a v2 history. Every new activation itself is
// v2 — the floor leaves no v1 shape to activate.
type proposalShape string

const (
	shapeV2Mixed proposalShape = "v2_on_v1_history"
	shapeV2      proposalShape = "v2"
)

var proposalShapes = []proposalShape{shapeV2Mixed, shapeV2}

// seedHistory gives the row an incumbent: a pre-floor v1 activation, or a v2 one the controller
// commits itself.
func seedHistory(t *testing.T, store *servingMemoryStore, controller *policyregistry.ServingController, shape proposalShape, set policyregistry.SelectionSet) policyregistry.ServingStateSnapshot {
	t.Helper()
	if shape == shapeV2Mixed {
		snapshot, _ := legacyV1Snapshot(t, store, set)
		return snapshot
	}
	proposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, set, servingEpoch)
	activated, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, proposal), "workflow")
	require.NoError(t, err)
	return activated.Snapshot
}

func permissiveController(t *testing.T, store *servingMemoryStore) *policyregistry.ServingController {
	t.Helper()
	controller, err := policyregistry.NewServingController(
		store,
		preparedValidator(func(context.Context, policyregistry.PreparedSelection) error { return nil }),
		func() time.Time { return servingEpoch },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.NoError(t, err)
	return controller
}

// heterogeneousLaneFixture activates a Default lane plus two profiles whose policies differ from
// each other, mirroring a target where customers run lane-local rosters.
func heterogeneousLaneFixture(t *testing.T) (*servingMemoryStore, *policyregistry.ServingController, policyregistry.SelectionSet, policyregistry.ActivationResult) {
	t.Helper()
	store, _, initialSet := controllerFixture(t)
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	basePolicy := store.policies[policyregistry.ObjectRef{URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation}]
	initialSet.Profiles[profileKeyOne] = registerProfileFixture(t, store, initialSet.Default, profileKeyOne, base.Policy)
	initialSet.Profiles[profileKeyTwo] = registerProfileFixture(t, store, initialSet.Default, profileKeyTwo, publishChangedPolicy(t, store, basePolicy, 0.55))
	store.publish(t, policyregistry.ServingSelectionSets, initialSet)
	controller := permissiveController(t, store)
	initialProposal := storedProposal(t, store, policyregistry.ServingStateSnapshot{}, initialSet, servingEpoch)
	initial, err := controller.Activate(context.Background(), store.publishArtifact(t, policyregistry.ServingProposal, initialProposal), "workflow")
	require.NoError(t, err)
	return store, controller, initialSet, initial
}

// routerOnlySource publishes a source release that carries a new Router image and provenance
// alongside a foreign policy and classifier, which a Router-only proposal must ignore.
func routerOnlySource(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, digit string) (policyregistry.ServingRelease, policyregistry.ObjectRef) {
	t.Helper()
	base := *store.object(t, policyregistry.ServingReleases, initialSet.Default.Release).(*policyregistry.ServingRelease)
	basePolicy := store.policies[policyregistry.ObjectRef{URI: base.Policy.URI, SHA256: base.Policy.SHA256, Generation: base.Policy.Generation}]
	source := base
	source.RouterImageDigest = "sha256:" + strings.Repeat(digit, 64)
	source.Policy = publishChangedPolicy(t, store, basePolicy, 0.65)
	source.Provenance.RouterRevision = strings.Repeat(digit, 40)
	source.Provenance.WeaveRevision = strings.Repeat("a", 40)
	attestation := "build-attestation-" + digit
	source.Provenance.BuildAttestation = artifactRef(attestation)
	store.artifacts[source.Provenance.BuildAttestation] = []byte(attestation)
	return source, store.publish(t, policyregistry.ServingReleases, source)
}

func routerOnlyRelease(t *testing.T, store *servingMemoryStore, previous policyregistry.ServingSelection, source policyregistry.ServingRelease) policyregistry.ServingRelease {
	release := *store.object(t, policyregistry.ServingReleases, previous.Release).(*policyregistry.ServingRelease)
	release.RouterImageDigest = source.RouterImageDigest
	release.Provenance = source.Provenance
	return release
}

func sharedRouterRevision(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, source policyregistry.ServingRelease, name string) policyregistry.RevisionBinding {
	router := store.object(t, policyregistry.ServingBindings, initialSet.Default.Binding).(*policyregistry.DeploymentBinding).Router
	router.Name = name
	router.URL = "https://" + name + ".example"
	router.ImageDigest = source.RouterImageDigest
	return router
}

// routerOnlyLane publishes the canonical successor lane: same policy, classifier, requirements,
// project, region, and classifier revision; new release, binding, and shared Router revision.
func routerOnlyLane(t *testing.T, store *servingMemoryStore, previous policyregistry.ServingSelection, source policyregistry.ServingRelease, router policyregistry.RevisionBinding) policyregistry.ServingSelection {
	t.Helper()
	classifier := store.object(t, policyregistry.ServingBindings, previous.Binding).(*policyregistry.DeploymentBinding).Classifier
	return publishSelection(t, store, previous, routerOnlyRelease(t, store, previous, source), router, classifier, previous.Profile)
}

func routerOnlySet(t *testing.T, store *servingMemoryStore, initialSet policyregistry.SelectionSet, source policyregistry.ServingRelease, router policyregistry.RevisionBinding) policyregistry.SelectionSet {
	t.Helper()
	nextSet := initialSet
	nextSet.Profiles = make(map[string]policyregistry.ServingSelection, len(initialSet.Profiles))
	nextSet.Default = routerOnlyLane(t, store, initialSet.Default, source, router)
	for key, previous := range initialSet.Profiles {
		nextSet.Profiles[key] = routerOnlyLane(t, store, previous, source, router)
	}
	return nextSet
}

func routerOnlyProposal(t *testing.T, store *servingMemoryStore, snapshot policyregistry.ServingStateSnapshot, set policyregistry.SelectionSet, sourceRef policyregistry.ObjectRef) policyregistry.DeploymentProposalV2 {
	t.Helper()
	store.publish(t, policyregistry.ServingSelectionSets, set)
	proposal := storedProposal(t, store, snapshot, set, servingEpoch)
	proposal.Scope = policyregistry.ChangeRouter
	proposal.SourceCandidate = foldCandidate(t, store, sourceRef)
	return proposal
}
