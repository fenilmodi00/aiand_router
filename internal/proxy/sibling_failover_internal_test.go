package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/auth"
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
		Model:    "z-ai/glm-5.2",
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
		RescueModels:        []string{"z-ai/glm-5.2"},
		SelectedArmID:       "fresh-arm",
		SelectedRosterArmID: "fresh-arm:high",
	}
	turn := turnLoopResult{StickyHit: true, Fresh: router.Decision{Model: "z-ai/glm-5.2", Metadata: fresh}}

	basis := rescueBasisForTurn(primary, turn)

	assert.Equal(t, primary.Model, basis.Model)
	assert.Equal(t, primary.Provider, basis.Provider)
	assert.Equal(t, primary.Reason, basis.Reason)
	assert.Nil(t, primary.Metadata, "the selected primary must retain its own dispatch identity")
	assert.NotSame(t, fresh, basis.Metadata)
	assert.Equal(t, []string{"z-ai/glm-5.2"}, basis.Metadata.RescueModels, "the held pin must retain eligible rescue models")
	assert.Equal(t, []string{"z-ai/glm-5.2"}, fresh.RescueModels, "fresh policy metadata must remain unchanged")
	assert.Nil(t, rescueBasisForTurn(primary, turnLoopResult{Fresh: turn.Fresh}).Metadata)
	turn.HardPinned = true
	assert.Nil(t, rescueBasisForTurn(primary, turn).Metadata)
}

func TestRescueBasisForHeldPinPreservesTierAndPolicyOrder(t *testing.T) {
	primary := router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.2", Reason: "held_pin"}
	freshMetadata := &router.RoutingMetadata{
		RosterFailover: true,
		RescueModels:   []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.2", "gpt-6-astra", "deepseek/deepseek-v4-pro"},
		CandidateModels: []string{
			"z-ai/glm-5.3-flash", "z-ai/glm-5.2", "gpt-6-astra", "deepseek/deepseek-v4-pro",
		},
		PairedModel: "z-ai/glm-5.3-flash",
	}
	turn := turnLoopResult{
		StickyHit: true,
		Fresh:     router.Decision{Model: "deepseek/deepseek-v4-pro", Metadata: freshMetadata},
	}

	basis := rescueBasisForTurn(primary, turn)
	service := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
	decisions := service.siblingFailoverDecisions(context.Background(), basis, 1_000, 0, 0)

	require.Equal(t, []string{"z-ai/glm-5.2", "gpt-6-astra"}, siblingModels(decisions))
	for _, decision := range decisions {
		assert.GreaterOrEqual(t, catalog.TierFor(decision.Model), catalog.TierFor(primary.Model))
	}
	assert.Equal(t, "z-ai/glm-5.3-flash", freshMetadata.PairedModel, "fresh metadata remains unchanged")
	assert.Equal(t, []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.2", "gpt-6-astra", "deepseek/deepseek-v4-pro"}, freshMetadata.RescueModels)
}

func TestSiblingFailoverDecision(t *testing.T) {
	ctx := context.Background()

	t.Run("prefers a candidate off the failed provider", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Model)
		assert.Equal(t, providers.ProviderAIAND, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
	})

	t.Run("walks the ranked group fallback before the rest of the scored pool", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
		failed := router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "gpt-6-astra",
			Metadata: &router.RoutingMetadata{
				// Catalog order puts haiku first; the roster's fallback is opus.
				CandidateModels: []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.2", "gpt-6-astra", "gpt-5"},
				RescueModels:    []string{"gpt-6-astra", "z-ai/glm-5.2", "gpt-5"},
				CandidateProviders: map[string]string{
					"z-ai/glm-5.3-flash": providers.ProviderAIAND,
					"z-ai/glm-5.2":       providers.ProviderAIAND,
					"gpt-5":              providers.ProviderAIAND,
				},
			},
		}
		got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)
		assert.Equal(t, []string{"z-ai/glm-5.2", "z-ai/glm-5.3-flash", "gpt-5"}, siblingModels(got),
			"ranked fallback first, then the pool, with same-provider candidates last")
		for _, d := range got {
			assert.Equal(t, ReasonSiblingFailover, d.Reason)
		}
	})

	t.Run("returns every eligible candidate so a failed rescuer hands off to the next", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
		got := s.siblingFailoverDecisions(ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
			PairedModel: "deepseek/deepseek-v4-pro",
		}), 1_000, 0, 0)
		assert.Equal(t, []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"}, siblingModels(got),
			"the failed model is dropped and the paired-model duplicate collapses")
	})

	t.Run("falls back to a same-provider candidate when nothing else is keyed", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Model)
		assert.Equal(t, providers.ProviderAIAND, got.Provider)
	})

	t.Run("uses the pin's runner-up when the pin carries no candidate vector", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		got, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			PairedModel: "deepseek/deepseek-v4-pro",
		}), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Model)
	})

	t.Run("drops the arm selection so binding resolution re-resolves", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		md := &router.RoutingMetadata{
			CandidateModels:     []string{"deepseek/deepseek-v4-pro"},
			SelectedArmID:       "arm-opus",
			SelectedRosterArmID: "arm-opus:high",
			SelectedUpstreamID:  "claude-opus-5-20260101",
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
		s := siblingService(providers.ProviderAIAND)
		_, ok := firstSibling(s, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek/deepseek-v4-pro"},
		}), 1_100_000, 0, 0)
		assert.False(t, ok, "claude-sonnet-5's extended window still can't serve a 1.1M-token turn")
	})

	t.Run("counts the output reserve against the candidate window", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		md := &router.RoutingMetadata{CandidateModels: []string{"deepseek/deepseek-v4-pro"}}
		_, ok := firstSibling(s, ctx, overloadedDecision(md), 990_000, 0, 32_000)
		assert.False(t, ok, "990K of history plus a 32K reserve overflows the window")

		_, ok = firstSibling(s, ctx, overloadedDecision(md), 990_000, 0, 4_000)
		assert.True(t, ok)
	})

	t.Run("skips the failed model and installation-excluded candidates", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		excluded := context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
		_, ok := firstSibling(s, excluded, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro"},
		}), 1_000, 0, 0)
		assert.False(t, ok)
	})

	t.Run("skips a model this session demoted after a committed stream failure", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
		md := &router.RoutingMetadata{
			CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
			CandidateProviders: map[string]string{
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			},
		}
		demoted := context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
		got, ok := firstSibling(s, demoted, overloadedDecision(md), 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Model, "the demoted arm is skipped even though it ranks first")
	})

	t.Run("gateway BYOK rescues via a sibling behind a held gateway key", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6"},
			},
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"z-ai/glm-5.2": "z-ai/glm-5.2"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"grok-4.6", "z-ai/glm-5.2"},
			},
		}
		got, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "z-ai/glm-5.2", got.Model)
		assert.Equal(t, providers.ProviderAIAND, got.Provider)
		assert.Equal(t, ReasonSiblingFailover, got.Reason)
		assert.True(t, s.gatewaySiblingAllowed(gwCtx, got))
	})

	t.Run("gateway BYOK walks the ranked fallback before other gateway aliases", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"gpt-6-astra": "gpt-6-astra", "gpt-5": "gpt-5"},
			},
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"z-ai/glm-5.3-flash": "z-ai/glm-5.3-flash", "z-ai/glm-5.2": "z-ai/glm-5.2"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "gpt-6-astra",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.2", "gpt-6-astra", "gpt-5"},
				RescueModels:    []string{"gpt-6-astra", "z-ai/glm-5.2", "gpt-5"},
			},
		}
		got := s.siblingFailoverDecisions(gwCtx, failed, 1_000, 0, 0)
		assert.Equal(t, []string{"z-ai/glm-5.2", "z-ai/glm-5.3-flash", "gpt-5"}, siblingModels(got))
		assert.Equal(t, providers.ProviderAIAND, got[0].Provider)
	})

	t.Run("gateway BYOK never rescues onto a provider without a held gateway key", func(t *testing.T) {
		s := &Service{deploymentKeyedProviders: map[string]struct{}{providers.ProviderAIAND: {}}}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				// opus is deployment-keyed on the vendor binding, but the tenant
				// mandated its gateway: no alias, no rescue.
				CandidateModels: []string{"z-ai/glm-5.2"},
			},
		}
		_, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
		assert.False(t, ok)
		assert.False(t, s.gatewaySiblingAllowed(gwCtx, router.Decision{Provider: providers.ProviderAIAND}))
	})

	t.Run("gateway BYOK rescues on the same gateway when it aliases a sibling", func(t *testing.T) {
		s := &Service{}
		gwCtx := context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{
				Provider:     providers.ProviderAIAND,
				Plaintext:    []byte("pat"),
				ModelAliases: map[string]string{"grok-4.6": "grok-4.6", "gpt-5": "gpt-5"},
			},
		})
		failed := router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    "grok-4.6",
			Metadata: &router.RoutingMetadata{
				CandidateModels: []string{"gpt-5"},
			},
		}
		got, ok := firstSibling(s, gwCtx, failed, 1_000, 0, 0)
		require.True(t, ok)
		assert.Equal(t, "gpt-5", got.Model)
		assert.Equal(t, providers.ProviderAIAND, got.Provider)
	})

	t.Run("no metadata and legacy unkeyed deploys have no candidate", func(t *testing.T) {
		s := siblingService(providers.ProviderAIAND)
		_, ok := firstSibling(s, ctx, overloadedDecision(nil), 1_000, 0, 0)
		assert.False(t, ok)

		legacy := &Service{}
		_, ok = firstSibling(legacy, ctx, overloadedDecision(&router.RoutingMetadata{
			CandidateModels: []string{"deepseek/deepseek-v4-pro"},
		}), 1_000, 0, 0)
		assert.False(t, ok, "an unset keyed-provider set can't prove a candidate is dispatchable")
	})
}

func TestSiblingFailover_ClusterScorerExhaustsModelTierBeforeAscending(t *testing.T) {
	s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND, providers.ProviderAIAND)
	failed := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.3-flash",
		Metadata: &router.RoutingMetadata{
			ClusterRouterVersion: "v-test",
			CandidateModels: []string{
				"z-ai/glm-5.3-flash", "gpt-4.1-mini", "gpt-4.1-nano",
				"deepseek/deepseek-v4-pro", "gpt-5",
			},
			CandidateScores: map[string]float32{
				"z-ai/glm-5.3-flash": 0.95, "gpt-4.1-mini": 0.8,
				"gpt-4.1-nano": 0.9, "deepseek/deepseek-v4-pro": 0.99, "gpt-5": 0.98,
			},
			CandidateProviders: map[string]string{
				"gpt-4.1-mini":             providers.ProviderAIAND,
				"gpt-4.1-nano":             providers.ProviderAIAND,
				"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
				"gpt-5":                    providers.ProviderAIAND,
			},
		},
	}

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"gpt-4.1-nano", "gpt-4.1-mini", "deepseek/deepseek-v4-pro", "gpt-5"}, siblingModels(got))
	assert.Empty(t, failed.Metadata.RescueModels, "rescue must not mutate the scorer's decision")

	failed.Model = "deepseek/deepseek-v4-pro"
	got = s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"gpt-5"}, siblingModels(got), "a mid-tier failure cannot fall down to low")
}

func TestSiblingFailover_RosterOrderBeatsProviderPreferenceAndExcludesUnlistedModels(t *testing.T) {
	s := siblingService(providers.ProviderAIAND, providers.ProviderAIAND)
	failed := overloadedDecision(&router.RoutingMetadata{
		PolicyGroup:    "low",
		RosterFailover: true,
		RescueModels:   []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"},
		CandidateModels: []string{
			"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash",
		},
		CandidateProviders: map[string]string{
			"deepseek/deepseek-v4-pro": providers.ProviderAIAND,
			"z-ai/glm-5.3-flash":       providers.ProviderAIAND,
		},
	})

	got := s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro"}, siblingModels(got))
}

// A gateway session that has struck out both of its arms (prod 2026-10:
// Cortex header timeouts demoted gpt-6-luna and claude-opus-5-5 in turn)
// still rescues onto the other arm instead of surfacing the 502: with no
// eligible or cooling candidate left, the session-lifetime demotion is
// lifted for the walk, and the arm that just failed is never re-served.
func TestGatewayRescueDecisions_AllArmsStruckOutReadmitsDemotedSibling(t *testing.T) {
	s := &Service{}
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("pat"), ModelAliases: map[string]string{catalog.ModelIDDeepSeekV4Pro.String(): catalog.ModelIDDeepSeekV4Pro.String()}},
		{Provider: providers.ProviderAIAND, Plaintext: []byte("pat"), ModelAliases: map[string]string{"z-ai/glm-5.2": "z-ai/glm-5.2"}},
	})
	struck := []string{catalog.ModelIDDeepSeekV4Pro.String(), "z-ai/glm-5.2"}
	ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)
	failed := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    catalog.ModelIDDeepSeekV4Pro.String(),
		Metadata: &router.RoutingMetadata{
			RosterFailover:    true,
			RescueModels:      []string{catalog.ModelIDDeepSeekV4Pro.String()},
			SidecarRescuePool: []string{"z-ai/glm-5.2"},
		},
	}

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"z-ai/glm-5.2"}, siblingModels(got))
	assert.Equal(t, providers.ProviderAIAND, got[0].Provider)
}

// Without the readmission list the struck-out pool stays empty, as before.
func TestRescueDecisions_StruckOutPoolWithoutReadmitListStaysEmpty(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro"},
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
		CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"},
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
		CandidateModels: []string{"z-ai/glm-5.2", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3-flash"},
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
	models := []string{"z-ai/glm-5.2", "z-ai/glm-5.1"}
	assert.Equal(t, models, imageSafeModels(models, false))
	assert.Equal(t, []string{"z-ai/glm-5.2"}, imageSafeModels(models, true))
}

// Strike readmission belongs to sibling failover only: the cyber-refusal
// retry walks its own runner-up then configured fallback, and a struck arm
// must not jump ahead of that fallback.
func TestRescueDecision_CyberRefusalRetryNeverReadmitsStruckArm(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	failed := overloadedDecision(&router.RoutingMetadata{})
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})

	_, found := s.rescueDecision(ctx, failed, []string{"deepseek/deepseek-v4-pro"}, ReasonCyberRefusalRetry, 1_000, 0, 0)
	assert.False(t, found)

	got := s.rescueDecisions(ctx, failed, []string{"deepseek/deepseek-v4-pro"}, ReasonSiblingFailover, 1_000, 0, 0)
	assert.Equal(t, []string{"deepseek/deepseek-v4-pro"}, siblingModels(got))
}

// A struck model outside this turn's scored pool is never readmitted: the last
// resort stays within the quality band the turn was scored for.
func TestRescueDecisions_StruckOutReadmissionStaysInScoredPool(t *testing.T) {
	s := siblingService(providers.ProviderAIAND)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"z-ai/glm-5.2"},
		CandidateProviders: map[string]string{"z-ai/glm-5.3-flash": providers.ProviderAIAND},
	}
	struck := []string{"z-ai/glm-5.3-flash"}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, struck)
	ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, struck)

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))

	md.ScorerRescuePool = struck
	assert.Equal(t, struck, siblingModels(s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)))
}

func TestHarnessSafeModels_DropsWeakAgenticArmsOnToolTurns(t *testing.T) {
	models := []string{"z-ai/glm-5.2", "gemini-3.5-flash-lite"}
	assert.Equal(t, models, harnessSafeModels(models, false))
	assert.Equal(t, []string{"z-ai/glm-5.2"}, harnessSafeModels(models, true))
}
