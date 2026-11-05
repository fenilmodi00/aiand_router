package router_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/router"
)

func TestExclusionsZeroValueExcludesNothing(t *testing.T) {
	var e router.Exclusions
	assert.False(t, e.Excludes("m"))
	assert.False(t, e.SafetyExcludes("m"))
	assert.False(t, e.ContextWindowExcludes("m"))
	assert.False(t, e.ImageLimitExcludes("m"))
	assert.False(t, e.UnsignedHistoryExcludes("m"))
	assert.False(t, e.HasContextWindowExclusions())
	assert.True(t, e.IsEmpty())
	assert.Nil(t, e.Union())
}

func TestExclusionsRequestedAddsUnionOnly(t *testing.T) {
	e := router.RequestedExclusions(map[string]struct{}{"a": {}, "b": {}})
	assert.True(t, e.Excludes("a"))
	assert.True(t, e.Excludes("b"))
	assert.False(t, e.SafetyExcludes("a"))
	assert.False(t, e.ContextWindowExcludes("a"))
	assert.Equal(t, map[string]struct{}{"a": {}, "b": {}}, e.Union())
}

func TestExclusionsExcludeSetNilIsEmpty(t *testing.T) {
	base := router.RequestedExclusions(map[string]struct{}{"a": {}})
	assert.Equal(t, base, base.ExcludeSet(nil, router.ExclusionContextWindow))
	assert.Equal(t, base, base.ExcludeSet(map[string]struct{}{}, router.ExclusionContextWindow))
}

func TestExclusionsKindMarkersStayWithinUnion(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("policy", router.ExclusionRequested).
		ExcludeModel("overflow", router.ExclusionContextWindow).
		ExcludeModel("images", router.ExclusionImageLimit).
		ExcludeModel("unsigned", router.ExclusionUnsignedHistory).
		ExcludeModel("maxed", router.ExclusionSafety)
	assert.True(t, e.Excludes("maxed"))
	assert.True(t, e.SafetyExcludes("maxed"))
	assert.False(t, e.ContextWindowExcludes("maxed"), "the safety marker is not a context-window marker")
	assert.True(t, e.Excludes("overflow") && e.ContextWindowExcludes("overflow"))
	assert.True(t, e.ImageLimitExcludes("images"))
	assert.True(t, e.UnsignedHistoryExcludes("unsigned"))
	assert.False(t, e.SafetyExcludes("overflow"), "context-window exclusions carry no safety marker")
}

func TestExclusionsRemoveModelsClearsEveryKind(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("m", router.ExclusionContextWindow)
	e = e.ExcludeSet(map[string]struct{}{"m": {}}, router.ExclusionSafety)
	after := e.RemoveModels([]string{"m", "absent"})
	assert.False(t, after.Excludes("m"))
	assert.False(t, after.SafetyExcludes("m"))
	assert.False(t, after.ContextWindowExcludes("m"))
}

func TestExclusionsReadmitPreservesEstimateMarker(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("m", router.ExclusionContextWindow)
	e = e.ExcludeSet(map[string]struct{}{"m": {}}, router.ExclusionSafety)
	readmitted, lifted := e.ReadmitForcedContextEstimate("m")
	assert.True(t, lifted)
	assert.False(t, readmitted.Excludes("m"))
	assert.False(t, readmitted.SafetyExcludes("m"), "the estimate-only exclusion must not keep blocking usage bypass")
	assert.True(t, readmitted.ContextWindowExcludes("m"), "the estimate marker itself is preserved for diagnostics")
}

func TestExclusionsReadmitForcedContextEstimateRequiresMarker(t *testing.T) {
	e := router.Exclusions{}.ExcludeModel("m", router.ExclusionRequested)
	_, lifted := e.ReadmitForcedContextEstimate("m")
	assert.False(t, lifted, "an explicit policy exclusion is never lifted by a force")
	assert.True(t, e.Excludes("m"))
}

func TestExclusionsReadmitForcedImageLimitSignalsCountClear(t *testing.T) {
	e := router.Exclusions{}.ExcludeModel("m", router.ExclusionImageLimit)
	readmitted, imageLimited := e.ReadmitForcedImageLimit("m")
	assert.True(t, imageLimited)
	assert.False(t, readmitted.Excludes("m"))
	assert.True(t, readmitted.ImageLimitExcludes("m"), "the marker survives for diagnostics")

	_, imageLimited = e.ReadmitForcedImageLimit("other")
	assert.False(t, imageLimited)
}

func TestExclusionsReadmitUnionOnlyKeepsSafetyMarker(t *testing.T) {
	e := router.Exclusions{}.ExcludeModel("m", router.ExclusionSafety)
	readmitted := e.ReadmitUnionOnly("m")
	assert.False(t, readmitted.Excludes("m"))
	assert.True(t, readmitted.SafetyExcludes("m"), "a concurrent physical constraint keeps blocking usage bypass")
}

func TestExclusionsReadmitForForceLiftsExactlyEligibleKinds(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("estimate", router.ExclusionContextWindow).
		ExcludeModel("capped", router.ExclusionImageLimit).
		ExcludeModel("policy", router.ExclusionRequested).
		ExcludeModel("unsigned", router.ExclusionUnsignedHistory)
	forced := e.ReadmitForForce("estimate")
	assert.False(t, forced.Excludes("estimate"))

	forced = e.ReadmitForForce("capped")
	assert.False(t, forced.Excludes("capped"))

	assert.True(t, e.ReadmitForForce("policy").Excludes("policy"))
	assert.True(t, e.ReadmitForForce("unsigned").Excludes("unsigned"))
}

func TestExclusionsWithoutContextWindowDropsEstimateExclusions(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("estimate", router.ExclusionContextWindow).
		ExcludeModel("policy", router.ExclusionRequested)
	after := e.WithoutContextWindow()
	assert.False(t, after.Excludes("estimate"))
	assert.False(t, after.ContextWindowExcludes("estimate"))
	assert.True(t, after.Excludes("policy"))
	empty := router.Exclusions{}.ExcludeModel("m", router.ExclusionRequested)
	assert.Equal(t, empty, empty.WithoutContextWindow(), "dropping an absent marker set is a no-op")
}

func TestExclusionsCopyOnWrite(t *testing.T) {
	e := router.RequestedExclusions(map[string]struct{}{"a": {}})
	original := e

	_ = e.ExcludeModel("b", router.ExclusionContextWindow)
	assert.True(t, original.Excludes("a"))
	assert.False(t, original.Excludes("b"), "mutating a copy must not affect the original")

	removed := e.RemoveModels([]string{"a"})
	assert.True(t, e.Excludes("a"), "RemoveModels must not mutate the receiver")
	assert.False(t, removed.Excludes("a"))

	readmit := e.Readmit("a")
	assert.True(t, e.Excludes("a"))
	assert.False(t, readmit.Excludes("a"))
}

func TestExclusionsExcludeSetTagsOnlyUntaggedMembers(t *testing.T) {
	e := router.Exclusions{}.
		ExcludeModel("m", router.ExclusionImageLimit)
	// Re-adding via a different kind keeps the original marker; union already holds m.
	merged := e.ExcludeSet(map[string]struct{}{"m": {}, "n": {}}, router.ExclusionContextWindow)
	assert.True(t, merged.ImageLimitExcludes("m"), "the original kind marker wins")
	assert.True(t, merged.ContextWindowExcludes("n"))
	assert.True(t, merged.Excludes("m") && merged.Excludes("n"))
}
