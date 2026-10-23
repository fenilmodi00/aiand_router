package proxy

import (
	"context"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func siblingService(keyed ...string) *Service {
	s := &Service{deploymentKeyedProviders: map[string]struct{}{}}
	for _, p := range keyed {
		s.deploymentKeyedProviders[p] = struct{}{}
	}
	return s
}

func overloadedDecision(md *router.RoutingMetadata) router.Decision {
	return router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.3",
		Metadata: md,
	}
}

// firstSibling is the head of the rescue walk: the candidate the turn tries first.
func firstSibling(s *Service, ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	decisions := s.siblingFailoverDecisions(ctx, failed, est, sigSavings, outputReserve)
	if len(decisions) == 0 {
		return router.Decision{}, false
	}
	return decisions[0], true
}

func siblingModels(decisions []router.Decision) []string {
	models := make([]string, 0, len(decisions))
	for _, d := range decisions {
		models = append(models, d.Model)
	}
	return models
}

func TestRescueBasisForTurnKeepsHeldPrimaryIdentity(t *testing.T) {
	primary := router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-6-luna", Reason: "held_pin"}
	fresh := &router.RoutingMetadata{
		RescueModels:        []string{"z-ai/glm-5.3"},
		SelectedArmID:       "fresh-arm",
		SelectedRosterArmID: "fresh-arm:high",
	}
	turn := turnLoopResult{StickyHit: true, Fresh: router.Decision{Model: "z-ai/glm-5.3", Metadata: fresh}}

	basis := rescueBasisForTurn(primary, turn)

	assert.Equal(t, primary.Model, basis.Model)
	assert.Equal(t, primary.Provider, basis.Provider)
	assert.Equal(t, primary.Reason, basis.Reason)
	assert.Nil(t, primary.Metadata, "the selected primary must retain its own dispatch identity")
	assert.NotSame(t, fresh, basis.Metadata)
	assert.Equal(t, []string{"z-ai/glm-5.3"}, basis.Metadata.RescueModels, "the held pin must retain eligible rescue models")
	assert.Equal(t, []string{"z-ai/glm-5.3"}, fresh.RescueModels, "fresh policy metadata must remain unchanged")
	assert.Nil(t, rescueBasisForTurn(primary, turnLoopResult{Fresh: turn.Fresh}).Metadata)
	turn.HardPinned = true
	assert.Nil(t, rescueBasisForTurn(primary, turn).Metadata)
}

// Without the readmission list the struck-out pool stays empty, as before.
func TestRescueDecisions_StruckOutPoolWithoutReadmitListStaysEmpty(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"z-ai/glm-5.3", "deepseek/deepseek-v4-pro"},
		CandidateProviders: map[string]string{"deepseek/deepseek-v4-pro": providers.ProviderAIAND},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))
}

// Struck-out arms are a last resort behind everything else: an eligible
// candidate keeps them out of the walk entirely, and so does a cooling arm.
func TestRescueDecisions_StruckOutArmsOnlyWhenNothingElseIsLeft(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels: []string{"z-ai/glm-5.3", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"},
		CandidateProviders: map[string]string{
			"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			"z-ai/glm-5.3-flash":       providers.ProviderAIAND,
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))

	ctx = context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"z-ai/glm-5.3-flash": time.Now().Add(time.Minute)})

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

// Lifting a session strike never lifts the deployment-wide exclusion or a
// hard (org) exclusion on the same model.
func TestRescueDecisions_StruckOutReadmissionKeepsGlobalExclusion(t *testing.T) {
	s := siblingService(providers.ProviderAIAND).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{byModel: map[string]string{"deepseek/deepseek-v4-pro": "disabled"}})
	md := &router.RoutingMetadata{
		CandidateModels: []string{"z-ai/glm-5.3", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"},
		CandidateProviders: map[string]string{
			"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			"z-ai/glm-5.3-flash":       providers.ProviderAIAND,
		},
	}
	struck := []string{"deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)

	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

// Dispatching a readmitted struck-out arm is recorded as rescue-pool
// exhaustion on the completion line.
func TestNoteRescueReadmission_RecordsStruckOutArm(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	ctx := context.WithValue(context.Background(), SessionStrikeReadmitModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx, turn := withRateLimitTurn(ctx)
	failed := overloadedDecision(&router.RoutingMetadata{})

	s.noteRescueReadmission(ctx, failed, router.Decision{Model: "z-ai/glm-5.3-flash"})
	assert.NotContains(t, turn.completionLogFields(), true)

	s.noteRescueReadmission(ctx, failed, router.Decision{Model: "deepseek/deepseek-v4-pro"})
	fields := turn.completionLogFields()
	assert.Contains(t, fields, "rescue_pool_exhausted")
	assert.Contains(t, fields, true)
	assert.Contains(t, fields, []string{"deepseek/deepseek-v4-pro"})
}

func TestImageSafeModels_DropsTextOnlyArmsOnImageTurns(t *testing.T) {
	// z-ai/glm-5.2 is text-only in the catalog; z-ai/glm-5.3-flash takes images.
	models := []string{"z-ai/glm-5.3", "z-ai/glm-5.3-flash"}
	assert.Equal(t, models, imageSafeModels(models, false))
	assert.Equal(t, []string{"z-ai/glm-5.3-flash"}, imageSafeModels(models, true))
}

// A struck arm is readmitted by the sibling rescue: the session still gets
// one rescue attempt on an arm that failed on an earlier turn.
func TestRescueDecisions_StrikerArmIsReadmitted(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	failed := overloadedDecision(&router.RoutingMetadata{})
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})

	got := s.rescueDecisions(ctx, failed, []string{"deepseek/deepseek-v4-pro"}, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek/deepseek-v4-pro"}, siblingModels(got))
}

// A struck model outside this turn's scored pool is never readmitted: the last
// resort stays within the quality band the turn was scored for.
func TestRescueDecisions_StruckOutReadmissionStaysInScoredPool(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"z-ai/glm-5.3"},
		CandidateProviders: map[string]string{"z-ai/glm-5.3-flash": providers.ProviderAIAND},
	}
	struck := []string{"z-ai/glm-5.3-flash"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))

	md.ScorerRescuePool = struck
	assert.Equal(t, struck, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

func TestHarnessSafeModels_KeepsCatalogKnownArmsOnToolTurns(t *testing.T) {
	// With deepseek-v4-flash removed no catalog row is ToolUseLow or
	// AgenticLow, so a tool-bearing turn keeps every harness-capable arm.
	models := []string{"z-ai/glm-5.3", "deepseek/deepseek-v4.1-flash"}
	assert.Equal(t, models, harnessSafeModels(models, false))
	assert.Equal(t, models, harnessSafeModels(models, true))
}
