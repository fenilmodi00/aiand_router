package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		Model:    "zai-org/glm-5.3",
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
	primary := router.Decision{Provider: providers.ProviderAIAND, Model: "qwen/qwen3.8-27b", Reason: "held_pin"}
	fresh := &router.RoutingMetadata{
		RescueModels:        []string{"deepseek-ai/deepseek-v4-pro"},
		SelectedArmID:       "fresh-arm",
		SelectedRosterArmID: "fresh-arm:high",
	}
	turn := turnLoopResult{StickyHit: true, Fresh: router.Decision{Model: "deepseek-ai/deepseek-v4-pro", Metadata: fresh}}

	basis := rescueBasisForTurn(primary, turn)

	assert.Equal(t, primary.Model, basis.Model)
	assert.Equal(t, primary.Provider, basis.Provider)
	assert.Equal(t, primary.Reason, basis.Reason)
	assert.Nil(t, primary.Metadata, "the selected primary must retain its own dispatch identity")
	assert.NotSame(t, fresh, basis.Metadata)
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, basis.Metadata.RescueModels, "the held pin must retain eligible rescue models")
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, fresh.RescueModels, "fresh policy metadata must remain unchanged")
	assert.Nil(t, rescueBasisForTurn(primary, turnLoopResult{Fresh: turn.Fresh}).Metadata)
	turn.HardPinned = true
	assert.Nil(t, rescueBasisForTurn(primary, turn).Metadata)
}

func TestRescueBasisForHeldPinPreservesTierAndPolicyOrder(t *testing.T) {
	primary := router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3", Reason: "held_pin"}
	freshMetadata := &router.RoutingMetadata{
		RosterFailover: true,
		RescueModels:   []string{"zai-org/glm-5.3-flash", "deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3", "deepseek-ai/deepseek-v4.1-flash"},
		CandidateModels: []string{
			"zai-org/glm-5.3-flash", "deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3", "deepseek-ai/deepseek-v4.1-flash",
		},
		PairedModel: "zai-org/glm-5.3-flash",
	}
	turn := turnLoopResult{
		StickyHit: true,
		Fresh:     router.Decision{Model: "deepseek-ai/deepseek-v4.1-flash", Metadata: freshMetadata},
	}

	basis := rescueBasisForTurn(primary, turn)
	service := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
	decisions := service.siblingFailoverDecisions(context.Background(), basis, 1_000, 0, 0)

	require.Equal(t, []string{"deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3"}, siblingModels(decisions))
	for _, decision := range decisions {
		assert.GreaterOrEqual(t, catalog.TierFor(decision.Model), catalog.TierFor(primary.Model))
	}
	assert.Equal(t, "zai-org/glm-5.3-flash", freshMetadata.PairedModel, "fresh metadata remains unchanged")
	assert.Equal(t, []string{"zai-org/glm-5.3-flash", "deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3", "deepseek-ai/deepseek-v4.1-flash"}, freshMetadata.RescueModels)
}

func TestSiblingFailoverDecision(t *testing.T) {
	ctx := context.Background()

	t.Run("prefers a candidate off the failed provider", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
		failed := router.Decision{
			Provider: providers.ProviderAnthropic,
			Model:    "zai-org/glm-5.3",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"},
				CandidateProviders: map[string]string{
					"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
					"deepseek-ai/deepseek-v4-pro":     providers.ProviderOpenAI,
				},
			},
		}
		got, ok := firstSibling(s, ctx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek-ai/deepseek-v4-pro", got.Model)
		assert.Equal(t, providers.ProviderOpenAI, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
	})

	t.Run("walks the ranked group fallback before the rest of the scored pool", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
		failed := router.Decision{
			Provider: providers.ProviderOpenAI,
			Model:    "moonshotai/kimi-k3",
			Metadata: &router.RoutingMetadata{
				// Catalog order puts haiku first; the roster's fallback is opus.
				CandidateModels: []string{"zai-org/glm-5.3-flash", "zai-org/glm-5.3", "moonshotai/kimi-k3", "qwen/qwen3.8-27b"},
				RescueModels:    []string{"moonshotai/kimi-k3", "zai-org/glm-5.3", "qwen/qwen3.8-27b"},
				CandidateProviders: map[string]string{
					"zai-org/glm-5.3-flash": providers.ProviderAnthropic,
					"zai-org/glm-5.3":       providers.ProviderAnthropic,
					"qwen/qwen3.8-27b":      providers.ProviderOpenAI,
				},
			},
		}
		got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)
		assert.Equal(t, []string{"zai-org/glm-5.3", "zai-org/glm-5.3-flash", "qwen/qwen3.8-27b"}, siblingModels(got),
			"ranked fallback first, then the pool, with same-provider candidates last")
		for _, d := range got {
			assert.Equal(t, ReasonSiblingFailover, d.Reason)
		}
	})

	t.Run("returns every eligible candidate so a failed rescuer hands off to the next", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
		got := s.siblingFailoverDecisions(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
				"deepseek-ai/deepseek-v4-pro":     providers.ProviderOpenAI,
			},
			PairedModel: "deepseek-ai/deepseek-v4.1-flash",
		}), 1_000, 0, 0)
		assert.Equal(t, []string{"deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"}, siblingModels(got),
			"the failed model is dropped and the paired-model duplicate collapses")
	})

	t.Run("falls back to a same-provider candidate when nothing else is keyed", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
				"deepseek-ai/deepseek-v4-pro":     providers.ProviderOpenAI,
			},
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek-ai/deepseek-v4.1-flash", got.Model)
		assert.Equal(t, providers.ProviderAnthropic, got.Provider)
	})

	t.Run("uses the pin's runner-up when the pin carries no candidate vector", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			PairedModel: "deepseek-ai/deepseek-v4.1-flash",
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek-ai/deepseek-v4.1-flash", got.Model)
	})

	t.Run("drops the arm selection so binding resolution re-resolves", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		md := &router.RoutingMetadata{
			CandidateModels:     []string{"deepseek-ai/deepseek-v4.1-flash"},
			SelectedArmID:       "arm-opus",
			SelectedRosterArmID: "arm-opus:high",
			SelectedUpstreamID:  "zai-org/glm-5.3-20260101",
			BindingIndex:        2,
		}
		got, ok := firstSibling(s, ctx, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Empty(t, got.Metadata.SelectedArmID)
		assert.Empty(t, got.Metadata.SelectedRosterArmID)
		assert.Empty(t, got.Metadata.SelectedUpstreamID)
		assert.Zero(t, got.Metadata.BindingIndex)
		assert.Equal(t, "arm-opus", md.SelectedArmID, "the source decision's metadata is not mutated")
	})

	t.Run("skips candidates whose context window can't hold the turn", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		_, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek-ai/deepseek-v4.1-flash"},
		}), 1_100_000, 0, 0)
		assert.False(t, ok, "zai-org/glm-5.3-flash's 1M window still can't serve a 1.1M-token turn")
	})

	t.Run("counts the output reserve against the candidate window", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		md := &router.RoutingMetadata{CandidateModels: []string{"qwen/qwen3.8-27b"}}
		_, ok := firstSibling(s, ctx, overloadedDecision(md), 250_000, 0, 32_000)
		assert.False(t, ok, "250K of history plus a 32K reserve overflows the 262K window")

		_, ok = firstSibling(s, ctx, overloadedDecision(md), 250_000, 0, 4_000)
		assert.True(t, ok)
	})

	t.Run("skips the failed model and installation-excluded candidates", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		excluded := context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4.1-flash"})
		_, ok := firstSibling(s, excluded, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4.1-flash"},
		}), 1_000, 0, 0)
		assert.False(t, ok)
	})

	t.Run("skips a model this session demoted after a committed stream failure", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
		md := &router.RoutingMetadata{
			CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
				"deepseek-ai/deepseek-v4-pro":     providers.ProviderOpenAI,
			},
		}
		demoted := context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
		got, ok := firstSibling(s, demoted, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek-ai/deepseek-v4.1-flash", got.Model, "the demoted arm is skipped even though it ranks first")
	})

	t.Run("no metadata and legacy unkeyed deploys have no candidate", func(t *testing.T) {
		s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND)
		_, ok := firstSibling(s, ctx, overloadedDecision(nil), 1_000, 0, 0)
		assert.False(t, ok)

		legacy := &Service{}
		_, ok = firstSibling(legacy, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek-ai/deepseek-v4.1-flash"},
		}), 1_000, 0, 0)
		assert.False(t, ok, "an unset keyed-provider set can't prove a candidate is dispatchable")
	})
}

func TestSiblingFailover_ClusterScorerExhaustsModelTierBeforeAscending(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderAIAND, providers.ProviderOpenAI)
	failed := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "zai-org/glm-5.3-flash",
		Metadata: &router.RoutingMetadata{
			ClusterRouterVersion: "v-test",
			CandidateModels: []string{
				"zai-org/glm-5.3-flash", "motif-technologies/motif-3", "deepseek-ai/deepseek-v4-flash",
				"deepseek-ai/deepseek-v4.1-flash", "qwen/qwen3.8-27b",
			},
			CandidateScores: map[string]float32{
				"zai-org/glm-5.3-flash": 0.95, "motif-technologies/motif-3": 0.8,
				"deepseek-ai/deepseek-v4-flash": 0.9, "deepseek-ai/deepseek-v4.1-flash": 0.99, "qwen/qwen3.8-27b": 0.98,
			},
			CandidateProviders: map[string]string{
				"motif-technologies/motif-3":      providers.ProviderOpenAI,
				"deepseek-ai/deepseek-v4-flash":   providers.ProviderOpenAI,
				"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
				"qwen/qwen3.8-27b":                providers.ProviderOpenAI,
			},
		},
	}

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-flash", "deepseek-ai/deepseek-v4.1-flash", "qwen/qwen3.8-27b", "motif-technologies/motif-3"}, siblingModels(got))
	assert.Empty(t, failed.Metadata.RescueModels, "rescue must not mutate the scorer's decision")

	failed.Model = "deepseek-ai/deepseek-v4.1-flash"
	got = s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"qwen/qwen3.8-27b", "motif-technologies/motif-3"}, siblingModels(got), "a mid-tier failure cannot fall down to low")
}

func TestSiblingFailover_RosterOrderBeatsProviderPreferenceAndExcludesUnlistedModels(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderAIAND)
	failed := overloadedDecision(&router.RoutingMetadata{
		PolicyGroup:    "low",
		RosterFailover: true,
		RescueModels:   []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"},
		CandidateModels: []string{
			"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4.1-flash", "zai-org/glm-5.3-flash",
		},
		CandidateProviders: map[string]string{
			"deepseek-ai/deepseek-v4.1-flash": providers.ProviderAnthropic,
			"deepseek-ai/deepseek-v4-pro":     providers.ProviderOpenAI,
			"zai-org/glm-5.3-flash":           providers.ProviderAnthropic,
		},
	})

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4.1-flash", "deepseek-ai/deepseek-v4-pro"}, siblingModels(got))
}

// A session that has struck out every arm still rescues onto a previously
// demoted arm instead of surfacing this turn's failure: with no eligible or
// cooling candidate left, the session-lifetime demotion is lifted for the
// walk, and the arm that just failed is never re-served.
func TestRescueDecisions_AllArmsStruckOutReadmitsDemotedSibling(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	struck := []string{"zai-org/glm-5.3-flash", "motif-technologies/motif-3"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)
	failed := overloadedDecision(&router.RoutingMetadata{
		RosterFailover:  true,
		RescueModels:    []string{"zai-org/glm-5.3-flash"},
		CandidateModels: []string{"zai-org/glm-5.3-flash", "motif-technologies/motif-3"},
		CandidateProviders: map[string]string{
			"zai-org/glm-5.3-flash":      providers.ProviderAIAND,
			"motif-technologies/motif-3": providers.ProviderAIAND,
		},
	})

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"zai-org/glm-5.3-flash"}, siblingModels(got))
}

// Without the readmission list the struck-out pool stays empty, as before.
func TestRescueDecisions_StruckOutPoolWithoutReadmitListStaysEmpty(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro"},
		CandidateProviders: map[string]string{"deepseek-ai/deepseek-v4-pro": providers.ProviderAIAND},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))
}

// Struck-out arms are a last resort behind everything else: an eligible
// candidate keeps them out of the walk entirely, and so does a cooling arm.
func TestRescueDecisions_StruckOutArmsOnlyWhenNothingElseIsLeft(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b"},
		CandidateProviders: map[string]string{
			"deepseek-ai/deepseek-v4-pro": providers.ProviderAIAND,
			"qwen/qwen3.8-27b":            providers.ProviderAIAND,
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})

	assert.Equal(t, []string{"qwen/qwen3.8-27b"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))

	ctx = context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"qwen/qwen3.8-27b": time.Now().Add(time.Minute)})

	assert.Equal(t, []string{"qwen/qwen3.8-27b"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

// Lifting a session strike never lifts the deployment-wide exclusion or a
// hard (org) exclusion on the same model.
func TestRescueDecisions_StruckOutReadmissionKeepsGlobalExclusion(t *testing.T) {
	s := siblingService(providers.ProviderAIAND).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{byModel: map[string]string{"deepseek-ai/deepseek-v4-pro": "disabled"}})
	md := &router.RoutingMetadata{
		CandidateModels: []string{"zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b"},
		CandidateProviders: map[string]string{
			"deepseek-ai/deepseek-v4-pro": providers.ProviderAIAND,
			"qwen/qwen3.8-27b":            providers.ProviderAIAND,
		},
	}
	struck := []string{"deepseek-ai/deepseek-v4-pro", "qwen/qwen3.8-27b"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)

	assert.Equal(t, []string{"qwen/qwen3.8-27b"}, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

// Dispatching a readmitted struck-out arm is recorded as rescue-pool
// exhaustion on the completion line.
func TestNoteRescueReadmission_RecordsStruckOutArm(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	ctx := context.WithValue(context.Background(), SessionStrikeReadmitModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
	ctx, turn := withRateLimitTurn(ctx)
	failed := overloadedDecision(&router.RoutingMetadata{})

	s.noteRescueReadmission(ctx, failed, router.Decision{Model: "qwen/qwen3.8-27b"})
	assert.NotContains(t, turn.completionLogFields(), true)

	s.noteRescueReadmission(ctx, failed, router.Decision{Model: "deepseek-ai/deepseek-v4-pro"})
	fields := turn.completionLogFields()
	assert.Contains(t, fields, "rescue_pool_exhausted")
	assert.Contains(t, fields, true)
	assert.Contains(t, fields, []string{"deepseek-ai/deepseek-v4-pro"})
}

func TestImageSafeModels_DropsTextOnlyArmsOnImageTurns(t *testing.T) {
	models := []string{"moonshotai/kimi-k3", "zai-org/glm-5.3"}
	assert.Equal(t, models, imageSafeModels(models, false))
	assert.Equal(t, []string{"moonshotai/kimi-k3"}, imageSafeModels(models, true))
}

// Strike readmission belongs to sibling failover only: the cyber-refusal
// retry walks its own runner-up then configured fallback, and a struck arm
// must not jump ahead of that fallback.
func TestRescueDecision_CyberRefusalRetryNeverReadmitsStruckArm(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	failed := overloadedDecision(&router.RoutingMetadata{})
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})

	_, found := s.rescueDecision(ctx, failed, []string{"deepseek-ai/deepseek-v4-pro"}, ReasonCyberRefusalRetry, 1_000, 0, 0)
	assert.False(t, found)

	got := s.rescueDecisions(ctx, failed, []string{"deepseek-ai/deepseek-v4-pro"}, ReasonSiblingFailover, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek-ai/deepseek-v4-pro"}, siblingModels(got))
}

// A struck model outside this turn's scored pool is never readmitted: the last
// resort stays within the quality band the turn was scored for.
func TestRescueDecisions_StruckOutReadmissionStaysInScoredPool(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"zai-org/glm-5.3"},
		CandidateProviders: map[string]string{"qwen/qwen3.8-27b": providers.ProviderAIAND},
	}
	struck := []string{"qwen/qwen3.8-27b"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))

	md.ScorerRescuePool = struck
	assert.Equal(t, struck, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

func TestHarnessSafeModels_DropsWeakAgenticArmsOnToolTurns(t *testing.T) {
	models := []string{"moonshotai/kimi-k3", "zai-org/glm-5.3"}
	assert.Equal(t, models, harnessSafeModels(models, false))
	// No roster row is ToolUseLow/AgenticLow, so a tool turn keeps every arm.
	assert.Equal(t, models, harnessSafeModels(models, true))
}
