package planner_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/planner"
	"aiand-os/router/internal/router/sessionpin"
)

const (
	// modelTop is the expensive, high-tier model: $2.00 input / $10.00 output
	// per 1M, cache-read multiplier 0.20/2.00.
	modelTop = "moonshotai/kimi-k3.1-pro"
	// modelMid is the mid-tier model: $1.00 input / $2.50 output per 1M,
	// cache-read multiplier 0.25.
	modelMid = "deepseek/deepseek-v4-pro"
	// modelLow is the cheap, low-tier model: $0.30 input / $0.60 output per
	// 1M, cache-read multiplier 0.02/0.30.
	modelLow = "deepseek/deepseek-v4.1-flash-1017"
	// modelAlt is a second, differently-priced model ($1.00 input / $4.00
	// output, cache-read multiplier 0.30) used where a case needs a model
	// other than modelTop on the fresh side.
	modelAlt     = "z-ai/glm-5.3"
	modelUnknown = "fictional-foo-1.0" // intentionally absent from the pricing table
)

// defaultCfg mirrors production defaults (threshold $0.001, horizon 3 turns).
var defaultCfg = planner.EVConfig{
	ThresholdUSD:           0.001,
	ExpectedRemainingTurns: 3,
}

// availableAll covers every model the EV cases reference; used everywhere
// except pin_model_missing.
var availableAll = map[string]struct{}{
	modelTop: {},
	modelMid: {},
	modelLow: {},
	modelAlt: {},
}

// tierUpgradeCfg mirrors defaultCfg with the tier guard on.
var tierUpgradeCfg = planner.EVConfig{
	ThresholdUSD:           0.001,
	ExpectedRemainingTurns: 3,
	TierUpgradeEnabled:     true,
}

// pinWithUsage returns a populated pin that has completed at least one
// turn, so the planner's LastTurnEndedAt-zero guard does not fire.
func pinWithUsage(model string) sessionpin.Pin {
	return sessionpin.Pin{
		Model:           model,
		LastTurnEndedAt: time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
	}
}

// The catalog carries a single provider (ai&), so the pin's named provider
// must resolve to the ai& binding and nothing else. A pin naming a provider
// the catalog does not know falls back to the model's primary binding — which
// is that same ai& binding — and the fallback is reported explicitly.
func TestDecide_UsesNamedProviderBindings(t *testing.T) {
	t.Parallel()

	base := planner.Inputs{
		Pin: sessionpin.Pin{
			Provider:        providers.ProviderAIAND,
			Model:           modelLow,
			LastTurnEndedAt: time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		},
		Fresh: router.Decision{
			Provider: providers.ProviderAIAND,
			Model:    modelTop,
		},
		EstimatedInputTokens: 1_000_000,
		AvailableModels: map[string]struct{}{
			modelLow: {},
			modelTop: {},
		},
	}

	// modelLow cached at 0.30*0.0667=$0.02/Mtok vs modelTop at 2.00*0.10=$0.20/Mtok:
	//   savingsPerTurn=-$0.18 x3 turns = -$0.54, evictionCost=2.00*0.90=$1.80.
	named := planner.Decide(base, planner.EVConfig{ExpectedRemainingTurns: 3})
	assert.InDelta(t, -0.54, named.ExpectedSavingsUSD, 1e-9)
	assert.InDelta(t, 1.80, named.EvictionCostUSD, 1e-9)
	assert.False(t, named.PinPriceFallback)
	assert.False(t, named.FreshPriceFallback)

	unknownProvider := base
	unknownProvider.Pin.Provider = "custom-provider"
	fallback := planner.Decide(unknownProvider, planner.EVConfig{ExpectedRemainingTurns: 3})
	assert.InDelta(t, named.ExpectedSavingsUSD, fallback.ExpectedSavingsUSD, 1e-12,
		"the primary binding IS the ai& binding, so the fallback prices identically")
	assert.InDelta(t, named.EvictionCostUSD, fallback.EvictionCostUSD, 1e-12)
	assert.True(t, fallback.PinPriceFallback, "the primary-price fallback must be reported")
}

func TestDecide_PrimaryPriceFallbackIsExplicit(t *testing.T) {
	t.Parallel()

	in := planner.Inputs{
		Pin: sessionpin.Pin{
			Provider:        "custom-provider",
			Model:           modelLow,
			LastTurnEndedAt: time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		},
		Fresh:                router.Decision{Provider: providers.ProviderAIAND, Model: modelTop},
		EstimatedInputTokens: 50_000,
		AvailableModels:      availableAll,
	}

	got := planner.Decide(in, defaultCfg)
	assert.True(t, got.PinPriceFallback, "custom bindings use the documented primary-price fallback")
	assert.False(t, got.FreshPriceFallback)

	missing := in
	missing.Fresh.Model = modelUnknown
	missing.AvailableModels = map[string]struct{}{modelLow: {}, modelUnknown: {}}
	got = planner.Decide(missing, defaultCfg)
	require.Equal(t, planner.ReasonPricingMissing, got.Reason)
	assert.True(t, got.PinPriceFallback, "successful fallback pricing must be retained on a missing-price decision")
	assert.False(t, got.FreshPriceFallback)
}

func TestDecide_ShadowInclusiveCostModel(t *testing.T) {
	t.Parallel()

	in := planner.Inputs{
		Pin:                   pinWithUsage(modelTop),
		Fresh:                 router.Decision{Model: modelLow},
		EstimatedInputTokens:  100,
		CacheablePrefixTokens: 60,
		AvailableModels:       availableAll,
	}
	got := planner.Decide(in, planner.EVConfig{ExpectedRemainingTurns: 1})
	assert.True(t, got.ShadowComputed)
	assert.InDelta(t, got.ShadowStayCostUSD, got.ShadowExpectedSavingsUSD+got.ShadowSwitchCostUSD, 1e-12)
	assert.Greater(t, got.ShadowSwitchCostUSD, 0.0, "N=1 prices the current switch action exactly once")

	in.CacheablePrefixTokens = 0
	noPrefix := planner.Decide(in, planner.EVConfig{ExpectedRemainingTurns: 1})
	in.PinCacheCold = true
	noPrefixCold := planner.Decide(in, planner.EVConfig{ExpectedRemainingTurns: 1})
	assert.InDelta(t, noPrefix.ShadowStayCostUSD, noPrefixCold.ShadowStayCostUSD, 1e-12,
		"K=0 removes cache-write advantage")

	in.CacheablePrefixTokens = 100
	cold := planner.Decide(in, planner.EVConfig{ExpectedRemainingTurns: 1})
	assert.Greater(t, cold.ShadowStayCostUSD, got.ShadowStayCostUSD, "a cold pin pays its current cache write")
}

// With ColdPinFollowFresh enabled, a cold pin follows the scorer's fresh pick
// even when the raw-price EV is below threshold.
func TestDecide_ColdPinFollowFresh(t *testing.T) {
	t.Parallel()
	coldCfg := planner.EVConfig{
		ThresholdUSD:           0.001,
		ExpectedRemainingTurns: 3,
		ColdPinFollowFresh:     true,
	}
	// Cheap pin → expensive fresh: raw-price EV is strongly negative, so only
	// the cold-pin lever can flip it.
	base := planner.Inputs{
		Pin:                  pinWithUsage(modelLow),
		Fresh:                router.Decision{Model: modelTop},
		EstimatedInputTokens: 50_000,
		AvailableModels:      availableAll,
		PinCacheCold:         true,
	}

	got := planner.Decide(base, coldCfg)
	assert.Equal(t, planner.OutcomeSwitch, got.Outcome, "cold pin + lever on must follow the fresh pick")
	assert.Equal(t, planner.ReasonColdPinFresh, got.Reason)
	assert.True(t, got.PinCacheCold, "decision must echo the cold pricing assumption")

	off := planner.Decide(base, defaultCfg)
	assert.Equal(t, planner.OutcomeStay, off.Outcome, "lever off must preserve the EV-negative stay")
	assert.Equal(t, planner.ReasonEVNegative, off.Reason)

	warm := base
	warm.PinCacheCold = false
	stay := planner.Decide(warm, coldCfg)
	assert.Equal(t, planner.OutcomeStay, stay.Outcome, "warm pin must not follow fresh on the cold lever")
	assert.Equal(t, planner.ReasonEVNegative, stay.Reason)

	// A cold pin whose switch is already EV-positive keeps the more specific
	// ev_positive reason.
	evPositive := planner.Inputs{
		Pin:                  pinWithUsage(modelTop),
		Fresh:                router.Decision{Model: modelLow},
		EstimatedInputTokens: 50_000,
		AvailableModels:      availableAll,
		PinCacheCold:         true,
	}
	pos := planner.Decide(evPositive, coldCfg)
	assert.Equal(t, planner.OutcomeSwitch, pos.Outcome)
	assert.Equal(t, planner.ReasonEVPositive, pos.Reason, "EV-positive must take precedence over the cold-pin reason")
}

func TestDecide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   planner.Inputs
		cfg  planner.EVConfig
		want planner.Decision
		// expectEVMath asserts that ExpectedSavingsUSD / EvictionCostUSD
		// are populated against the hand-computed expectations.
		expectEVMath           bool
		wantExpectedSavingsUSD float64
		wantEvictionCostUSD    float64
	}{
		{
			name: "no_pin: zero-value pin always switches",
			in: planner.Inputs{
				Pin:                  sessionpin.Pin{},
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 1000,
				AvailableModels:      availableAll,
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonNoPin},
		},
		{
			name: "same_model: fresh recommendation matches the pin",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonSameModel},
		},
		{
			name: "pin_model_missing: pin model not in availability set",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 50_000,
				// modelTop deliberately absent — provider key was removed.
				AvailableModels: map[string]struct{}{modelLow: {}, modelMid: {}},
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonPinModelMissing},
		},
		{
			name: "no_prior_usage: pin populated but LastTurnEndedAt zero",
			in: planner.Inputs{
				// Model set but no completed turn → conservative stay.
				Pin:                  sessionpin.Pin{Model: modelTop},
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonNoPriorUsage},
		},
		{
			name: "pricing_missing: pin model unknown to price table",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelUnknown),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 50_000,
				AvailableModels:      map[string]struct{}{modelUnknown: {}, modelLow: {}},
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonPricingMissing},
		},
		{
			name: "pricing_missing: fresh model unknown to price table",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelUnknown},
				EstimatedInputTokens: 50_000,
				AvailableModels:      map[string]struct{}{modelTop: {}, modelUnknown: {}},
			},
			cfg:  defaultCfg,
			want: planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonPricingMissing},
		},
		{
			// kimi-k3.1-pro -> deepseek-v4.1-flash-1017, 50k tokens, 3 turns.
			//   cached pin 2.00*0.10=$0.20/Mtok, cached fresh 0.30*0.0667=$0.02/Mtok
			//   savingsPerTurn=(0.20-0.02)*50k=$0.009 -> expectedSavings=$0.027
			//   evictionCost=0.30*(1-0.0667)*50k=$0.014
			//   delta=$0.013 -> Switch.
			name: "ev_positive: kimi-k3.1-pro -> deepseek-v4.1-flash-1017 on a large prompt",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonEVPositive},
			expectEVMath:           true,
			wantExpectedSavingsUSD: 0.027,
			wantEvictionCostUSD:    0.014,
		},
		{
			// Symmetric flip: deepseek-v4.1-flash-1017 pin, kimi-k3.1-pro fresh.
			//   expectedSavings=-$0.027 evictionCost=2.00*(1-0.10)*50k=$0.09
			//   delta=-$0.117 -> Stay.
			name: "ev_negative: deepseek-v4.1-flash-1017 -> kimi-k3.1-pro is a huge net loss",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelLow),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.027,
			wantEvictionCostUSD:    0.09,
		},
		{
			// kimi-k3.1-pro -> deepseek-v4.1-flash-1017, tuned to land just
			// below threshold: warm rate gap $0.54/Mtok, eviction $0.28/Mtok,
			// so 3846 tokens nets 3846*(0.54-0.28)/1M=$0.00099996, a hair
			// below the $0.001 threshold -> Stay.
			name: "ev_near_threshold: just below threshold stays stable",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 3846,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath:           true,
			wantExpectedSavingsUSD: 0.00207684,
			wantEvictionCostUSD:    0.00107688,
		},
		{
			// Same math, one extra token (3847) nudges net to $0.00100022,
			// a hair above threshold -> Switch.
			name: "ev_near_threshold: just above threshold flips to switch",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 3847,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonEVPositive},
			expectEVMath:           true,
			wantExpectedSavingsUSD: 0.00207738,
			wantEvictionCostUSD:    0.00107716,
		},
		{
			// Second price class: kimi-k3.1-pro -> glm-5.3, 50k prompt. The
			// pin is cheaper in cache steady-state (cached $0.20 vs $0.30,
			// expectedSavings=(0.20-0.30)*50k*3=-$0.015), so no cache-driven
			// incentive; the eviction cost ($0.035) is irrelevant to the
			// negative verdict -> Stay.
			name: "ev_cross_provider: kimi-k3.1-pro -> glm-5.3 stays under per-model math",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelAlt},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.015,
			wantEvictionCostUSD:    0.035,
		},
		{
			// Mirror of ev_negative; pinned here so the next case can show
			// the tier knob is what flips the verdict.
			name: "tier_upgrade_disabled: low -> high still stays",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelLow),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:                    defaultCfg, // TierUpgradeEnabled = false
			want:                   planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.027,
			wantEvictionCostUSD:    0.09,
		},
		{
			// Same EV-loss as above; tier guard flips it since kimi-k3.1-pro
			// (High) outranks deepseek-v4.1-flash-1017 (Low).
			name: "tier_upgrade: low -> high flips stay into switch",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelLow),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
			},
			cfg:                    tierUpgradeCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonTierUpgrade},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.027,
			wantEvictionCostUSD:    0.09,
		},
		{
			// deepseek-v4-pro (Mid) -> deepseek-v4.1-flash-1017 (Low) is a
			// downgrade; guard must not fire, EV math governs.
			name: "tier_upgrade: downgrade does not trigger guard",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelMid),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 1000,
				AvailableModels:      availableAll,
			},
			cfg:          tierUpgradeCfg,
			want:         planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath: true,
			// cached pin 1.00*0.25=$0.25/Mtok, cached fresh $0.02/Mtok
			//   expectedSavings=(0.25-0.02)*1000*3=$0.00069
			//   evictionCost=0.30*(1-0.0667)*1000=$0.00028
			wantExpectedSavingsUSD: 0.00069,
			wantEvictionCostUSD:    0.00028,
		},
		{
			// Cold pin: cache TTL lapsed, both sides price uncached, so this
			// switches on raw input price rather than the cache-read delta.
			// expectedSavings=(2.00-0.30)*50k*3=$0.255, evictionCost=$0
			// (nothing warm to evict).
			name: "cold_ev_positive: kimi-k3.1-pro -> deepseek-v4.1-flash-1017 prices uncached",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelLow},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
				PinCacheCold:         true,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonEVPositive},
			expectEVMath:           true,
			wantExpectedSavingsUSD: 0.255,
			wantEvictionCostUSD:    0,
		},
		{
			// Cold twin of ev_cross_provider (which STAYS): with no warm cache
			// to preserve, the raw $2.00 vs $1.00 input price gap wins
			// (expectedSavings=(1.00-2.00)*50k*3=-$0.15 under the warm-rate
			// frame, but cold prices both sides uncached: (2.00-1.00)*50k*3=
			// $0.15) and it switches.
			name: "cold_cross_provider: kimi-k3.1-pro -> glm-5.3 switches once cache is cold",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelTop),
				Fresh:                router.Decision{Model: modelAlt},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
				PinCacheCold:         true,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonEVPositive},
			expectEVMath:           true,
			wantExpectedSavingsUSD: 0.15,
			wantEvictionCostUSD:    0,
		},
		{
			// Cold pin on the cheaper model: no cache to preserve, but switching
			// to the pricier fresh model is still a raw-price loss -> Stay.
			name: "cold_ev_negative: deepseek-v4.1-flash-1017 -> kimi-k3.1-pro stays on raw price",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelLow),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
				PinCacheCold:         true,
			},
			cfg:                    defaultCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeStay, Reason: planner.ReasonEVNegative},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.255,
			wantEvictionCostUSD:    0,
		},
		{
			// Cold pin does not disable the tier-upgrade guard: EV alone would
			// stay, but kimi-k3.1-pro (High) outranks deepseek-v4.1-flash-1017
			// (Low) so it still switches.
			name: "cold_tier_upgrade: guard still fires when cache is cold",
			in: planner.Inputs{
				Pin:                  pinWithUsage(modelLow),
				Fresh:                router.Decision{Model: modelTop},
				EstimatedInputTokens: 50_000,
				AvailableModels:      availableAll,
				PinCacheCold:         true,
			},
			cfg:                    tierUpgradeCfg,
			want:                   planner.Decision{Outcome: planner.OutcomeSwitch, Reason: planner.ReasonTierUpgrade},
			expectEVMath:           true,
			wantExpectedSavingsUSD: -0.255,
			wantEvictionCostUSD:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := planner.Decide(tc.in, tc.cfg)
			assert.Equal(t, tc.want.Outcome, got.Outcome, "outcome")
			assert.Equal(t, tc.want.Reason, got.Reason, "reason")

			if tc.expectEVMath {
				// The math is exact-rational on these inputs so a tight
				// tolerance is fine; we only allow float64 rounding noise.
				assert.InDelta(t, tc.wantExpectedSavingsUSD, got.ExpectedSavingsUSD, 1e-9, "expected_savings_usd")
				assert.InDelta(t, tc.wantEvictionCostUSD, got.EvictionCostUSD, 1e-9, "eviction_cost_usd")
				assert.Equal(t, tc.cfg.ThresholdUSD, got.ThresholdUSD, "threshold_usd echoed")
				assert.Equal(t, tc.in.PinCacheCold, got.PinCacheCold, "pin_cache_cold echoed")
			} else {
				// When the EV math never ran, all three USD fields are
				// left zero — this is what the orchestrator stamps for
				// non-EV reasons.
				assert.Zero(t, got.ExpectedSavingsUSD, "expected_savings_usd zero when EV unused")
				assert.Zero(t, got.EvictionCostUSD, "eviction_cost_usd zero when EV unused")
				assert.Zero(t, got.ThresholdUSD, "threshold_usd zero when EV unused")
			}
		})
	}
}
