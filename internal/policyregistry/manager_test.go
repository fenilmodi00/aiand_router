package policyregistry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type fakeLoader struct {
	head       policyregistry.HeadSnapshot
	release    policyregistry.Release
	policy     *rosterdata.Roster
	releaseErr error
}

func (f *fakeLoader) RootURI() string { return testRegistryRoot }
func (f *fakeLoader) ReadHead(context.Context, policyregistry.Environment, policyregistry.Lane) (policyregistry.HeadSnapshot, error) {
	return f.head, nil
}
func (f *fakeLoader) ReadRelease(context.Context, policyregistry.ObjectRef) (policyregistry.Release, error) {
	return f.release, f.releaseErr
}
func (f *fakeLoader) ReadPolicy(context.Context, policyregistry.ObjectRef) (*rosterdata.Roster, error) {
	return f.policy, nil
}

type fixedRouter struct{ model string }

func (r fixedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Model: r.model}, nil
}

func validLoader(t *testing.T) *fakeLoader {
	t.Helper()
	policyJSON := []byte(`{
  "schema_version":"hmm_go_selection_policy_v1",
  "class_order":["low"],
  "ranking":{"alpha":{"low":0.4},"alpha_min":{"low":0.1},"alpha_max":{"low":0.8},"quality_bias_neutral":0.7,"wii_score_version":"wii-v1","wii_normalization_sha256":"wii","wpi_score_version":"wpi-v1","wpi_normalization_sha256":"wpi"},
  "preferences":{"preferred_model_bonus":0.5,"subscription_bonus":0.35},
  "clusters":{"low":{"complexity_label":"low","arms":["openai/gpt-5.6-sol"],"cost_ref_usd":1,"latency_ref_ms":1,"arm_scores":{"openai/gpt-5.6-sol":1},"arm_indices":{"openai/gpt-5.6-sol":{"wii_v1":80,"wpi_v1":40}}}}
}`)
	policy, err := rosterdata.ParseValidated(policyJSON)
	require.NoError(t, err)
	release := validRelease()
	release.Classifier.ClassOrder = []string{"low"}
	release.Classifier.TaxonomySHA256 = policyregistry.TaxonomyDigest(release.Classifier.ClassOrder)
	policySHA := strings.Repeat("a", 64)
	release.Policy.SHA256 = policySHA
	release.Policy.URI = testRegistryRoot + "/router_policy/v1/policies/sha256/" + policySHA + ".json"
	releaseSHA := strings.Repeat("b", 64)
	return &fakeLoader{
		head: policyregistry.HeadSnapshot{Generation: 1, Head: policyregistry.LaneHead{
			SchemaVersion: policyregistry.LaneHeadSchemaV1,
			Environment:   policyregistry.EnvironmentStaging, Lane: policyregistry.LaneStable,
			ReleaseURI:    testRegistryRoot + "/router_policy/v1/releases/sha256/" + releaseSHA + ".json",
			ReleaseSHA256: releaseSHA, ReleaseGeneration: 1,
		}},
		release: release,
		policy:  policy,
	}
}
