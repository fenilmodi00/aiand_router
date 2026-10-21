package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyCandidateError_NamesTheGatewayCase(t *testing.T) {
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "moonshotai/kimi-k3", Reason: ExclusionGatewayNotServed},
		{CatalogID: "z-ai/glm-5.2", Reason: ExclusionGatewayNotServed},
	})

	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel)
	assert.ErrorIs(t, err, ErrNoRoutableModels)
}

func TestEmptyCandidateError_MixedReasonsStayGeneric(t *testing.T) {
	// One non-gateway drop means aliases are not the whole story, so pointing
	// the caller at them would send them to fix the wrong setting.
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "moonshotai/kimi-k3", Reason: ExclusionGatewayNotServed},
		{CatalogID: "z-ai/glm-5.2", Reason: ExclusionRequested},
	})

	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrGatewayServesNoDeployedModel)
}

func TestEmptyCandidateError_NoDiagnostics(t *testing.T) {
	assert.ErrorIs(t, emptyCandidateError(nil), ErrNoRoutableModels)
}

func TestEmptyCandidateError_SizeIsNotConfiguration(t *testing.T) {
	// A pool emptied partly by size answers with the client's native
	// prompt-too-long, the only error a harness compacts on.
	err := emptyCandidateError([]Diagnostic{
		{CatalogID: "moonshotai/kimi-k3", Reason: ExclusionContextWindow},
		{CatalogID: "z-ai/glm-5.2", Reason: ExclusionRequested},
	})
	assert.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.NotErrorIs(t, err, ErrNoRoutableModels)

	// An overflow-admitted model that is unavailable for another reason is
	// not a context-window failure; compaction cannot fix its missing roster.
	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "z-ai/glm-5.3-flash", Reason: ExclusionUnmappedRoster},
	})
	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrContextWindowExceeded)

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "z-ai/glm-5.3", Reason: ExclusionUnsignedHistory},
	})
	assert.ErrorIs(t, err, ErrNoRoutableModels)
	assert.NotErrorIs(t, err, ErrContextWindowExceeded)

	err = emptyCandidateError([]Diagnostic{
		{CatalogID: "z-ai/glm-5.3-flash", Reason: ExclusionGatewayNotServed},
	})
	assert.ErrorIs(t, err, ErrGatewayServesNoDeployedModel, "a gateway serving nothing is empty at any size")
}

func TestCandidateLogFields_GroupsExclusionsByReason(t *testing.T) {
	fields := candidateLogFields(ResolvedCandidates{
		Candidates: []Candidate{{CatalogID: "z-ai/glm-5.2"}, {CatalogID: "z-ai/glm-5.3-flash"}},
		Diagnostics: []Diagnostic{
			{CatalogID: "moonshotai/kimi-k3", Reason: ExclusionRequested},
			{CatalogID: "z-ai/glm-5", Reason: ExclusionContextWindow},
			{CatalogID: "z-ai/glm-5.2", Reason: ExclusionRequested},
		},
	})

	assert.Equal(t, []any{
		"candidate_count", 2,
		"candidates", "z-ai/glm-5.2,z-ai/glm-5.3-flash",
		"excluded_" + string(ExclusionContextWindow), "z-ai/glm-5",
		"excluded_" + string(ExclusionRequested), "moonshotai/kimi-k3,z-ai/glm-5.2",
	}, fields)
}
