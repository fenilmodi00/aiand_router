package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func TestModelSelectionReasoningIsOptInAndUsesRecordedClassification(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationShowModelSelectionReasoningContextKey{}, true)
	turn := turnLoopResult{Decision: router.Decision{Model: "chosen-model", Metadata: &router.RoutingMetadata{
		ClassifierPredictedLabel: "high", PolicyGroup: "low",
	}}}
	marker := routingMarkerFor(turn)
	assert.NotContains(t, modelSelectionMarkerForRequest(context.Background(), turn, marker, turn.Decision.Model, ""), "REASONING:")
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""),
		"\nREASONING: This part of the conversation was classified as high difficulty.\n")

	turn.Decision.Metadata.ClassifierPredictedLabel = "unrecognized"
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "classified as low difficulty")
	turn.Decision.Metadata.PolicyGroup = "fast"
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "Routing classified this part of the conversation as fast.")
	turn.Decision.Metadata.PolicyGroup = "unknown-group"
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "best pick for this turn")
	assert.NotContains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "unknown-group")
	turn.Decision.Metadata.ClassifierPredictedLabel = "high"
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, siblingRoutingMarkerFor(turn, "rescue-model"), "rescue-model", markerReasonSibling), "classified as high difficulty")
}

func TestModelSelectionReasoningOnlyForNewlyServedModel(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationShowModelSelectionReasoningContextKey{}, true)
	turn := turnLoopResult{Decision: router.Decision{Model: "new-model"}, PriorServedModel: "old-model"}
	marker := routingMarkerFor(turn)
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "REASONING:")
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, siblingRoutingMarkerFor(turn, "rescue-model"), "rescue-model", markerReasonSibling), "REASONING: "+markerReasonSibling)
	assert.Contains(t, modelSelectionMarkerForRequest(ctx, turn, baselineRoutingMarkerFor(turn, "baseline-model"), "baseline-model", markerReasonBaseline), "REASONING: "+markerReasonBaseline)

	turn.PriorServedModel = "new-model"
	assert.NotContains(t, modelSelectionMarkerForRequest(ctx, turn, "✦ **Weave Router** → new-model\n\n", turn.Decision.Model, ""), "REASONING:")
	turn.PriorServedModel = "old-model"
	turn.SuggestionMode = true
	assert.NotContains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "REASONING:")
	turn.SuggestionMode = false
	turn.HardPinned = true
	assert.NotContains(t, modelSelectionMarkerForRequest(ctx, turn, marker, turn.Decision.Model, ""), "REASONING:")
	assert.Empty(t, modelSelectionMarkerForRequest(ctx, turn, "", turn.Decision.Model, ""))
}

func TestRoutingMarkerFor_EmptyDecisionEmitsNothing(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{Decision: router.Decision{}})
	assert.Empty(t, got)
}

func TestRoutingMarkerFor_SuggestionModeSuppressed(t *testing.T) {
	res := turnLoopResult{
		Decision:       router.Decision{Model: "gpt-5.5", Provider: "openai"},
		SuggestionMode: true,
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonSameModel,
		},
	}
	got := routingMarkerFor(res)
	assert.Empty(t, got, "suggestion-mode responses must not emit the routing badge")
}

func TestRoutingMarkerFor_CallerModelPassthroughSuppressed(t *testing.T) {
	res := turnLoopResult{
		Decision:               router.Decision{Model: "deepseek/deepseek-v4.1-flash", Provider: "anthropic"},
		CallerModelPassthrough: true,
	}
	assert.Empty(t, routingMarkerFor(res), "caller-model passthrough must preserve the stock client experience")
}

func TestRoutingMarkerFor_RecapSuppressed(t *testing.T) {
	res := turnLoopResult{
		Decision: router.Decision{Model: "gpt-5.6-luna", Provider: "openai"},
		TurnType: turntype.Recap,
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	}
	got := routingMarkerFor(res)
	assert.Empty(t, got, "a recap renders beneath the reply the user just read and must not repeat the routing badge")
}

func TestRoutingMarkerFor_UsesSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model:    "moonshotai/kimi-k2.7-code",
			Provider: "openrouter",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Equal(t, "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work\n\n", got)
}

func TestRoutingMarkerFor_SidecarDisplayMarkerStillRespectsSuggestionMode(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		SuggestionMode: true,
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → Delegating work with moonshotai/kimi-k2.7-code\n↳ label: delegated_work",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Empty(t, got)
}

func TestRoutingMarkerFor_RejectsMalformedSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "not a router marker\narbitrary sidecar text",
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.Contains(t, got, "✦ **Weave Router** → moonshotai/kimi-k2.7-code")
	assert.Contains(t, got, markerReasonBestPick)
	assert.NotContains(t, got, "arbitrary sidecar text")
}

func TestRoutingMarkerFor_ClampsSidecarDisplayMarker(t *testing.T) {
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "moonshotai/kimi-k2.7-code",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → " + strings.Repeat("x", maxSidecarDisplayMarkerRunes+100),
			},
		},
		PlannerDecision: planner.Decision{
			Reason: planner.ReasonNoPin,
		},
	})

	assert.LessOrEqual(t, len([]rune(strings.TrimSpace(got))), maxSidecarDisplayMarkerRunes)
	assert.Contains(t, got, "✦ **Weave Router** → ")
}

func TestRoutingMarkerFor_SidecarDisplayMarkerSuppressedWhenServedModelUnchanged(t *testing.T) {
	// The HMM sidecar renders `<model> · <cluster reason>` (e.g. "fast route
	// for this turn") on every /route call regardless of whether the served
	// model actually changed from the prior turn. Without this gate the same
	// sub-agent would see the badge repeated turn after turn.
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "deepseek/deepseek-v4-flash",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn",
			},
		},
		PriorServedModel: "deepseek/deepseek-v4-flash",
	})
	assert.Empty(t, got, "sidecar marker must be suppressed when the served model did not change")
}

func TestRoutingMarkerFor_SidecarDisplayMarkerShownOnGenuineSwitch(t *testing.T) {
	// Sanity check the other side: when the served model genuinely changes
	// the sidecar marker still renders.
	got := routingMarkerFor(turnLoopResult{
		Decision: router.Decision{
			Model: "deepseek/deepseek-v4-flash",
			Metadata: &router.RoutingMetadata{
				DisplayMarker: "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn",
			},
		},
		PriorServedModel: "deepseek/deepseek-v4-pro",
	})
	assert.Equal(t, "✦ **Weave Router** → deepseek/deepseek-v4-flash · fast route for this turn\n\n", got)
}

func TestBaselineRoutingMarkerFor_SuppressedWhenBaselineAlreadyServing(t *testing.T) {
	got := baselineRoutingMarkerFor(turnLoopResult{
		PriorServedModel: "z-ai/glm-5.2",
	}, "z-ai/glm-5.2")
	assert.Empty(t, got, "a failover that lands back on the model already serving must stay quiet")
}

func TestHumanReasonFromPlanner_UnknownCodeIsSilenced(t *testing.T) {
	// Unknown codes return empty so a new planner reason can't leak its
	// snake_case label into the user-facing marker.
	got := humanReasonFromPlanner("brand_new_reason_v9")
	assert.Empty(t, got)
}

func TestRoutingMarkerFor_SuppressesStickyTurnForAllClients(t *testing.T) {
	res := turnLoopResult{
		Decision:         router.Decision{Model: "gpt-5.6-terra", Provider: "openai"},
		StickyHit:        true,
		PriorServedModel: "gpt-5.6-terra",
	}
	assert.Empty(t, routingMarkerFor(res), "same-model suppression applies to Codex as well as Claude Code")
}

func TestRoutingMarkerFor_SuppressesEffortOnlyChange(t *testing.T) {
	res := turnLoopResult{
		Decision:         router.Decision{Model: "gpt-5.6-luna", Provider: "openai", Effort: "xhigh"},
		PriorServedModel: "gpt-5.6-luna:xhigh",
	}
	assert.Empty(t, routingMarkerFor(res), "changing effort must not repeat the model-choice marker")
}

func TestRoutingMarkerFor_ShadowEscalationShowsWithoutModelSwitchAndStripsOnEcho(t *testing.T) {
	decision := router.Decision{Model: "z-ai/glm-5.3-flash"}
	turn := turnLoopResult{Decision: decision, PriorServedModel: decision.Model, EscalationShadowMarked: true}
	marker := routingMarkerFor(turn)
	require.Equal(t, routingMarkerPrefix+decision.Model+" · "+markerReasonShadowEscalation+"\n\n", marker)

	for _, content := range []any{
		marker + "The answer.",
		[]map[string]string{{"type": "text", "text": marker + "The answer."}},
	} {
		body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": content}}})
		require.NoError(t, err)
		cleaned, err := translate.StripRoutingMarkerFromMessages(body)
		require.NoError(t, err)
		require.NotContains(t, string(cleaned), markerReasonShadowEscalation)
		require.Contains(t, string(cleaned), "The answer.")
	}

	turn.EscalationShadowMarked = false
	require.Empty(t, routingMarkerFor(turn))
	turn.EscalationShadowMarked = true
	turn.SuggestionMode = true
	require.Empty(t, routingMarkerFor(turn))
}

func TestSubscriptionTargetMarker_UnchangedModelKeepsRoutedMarker(t *testing.T) {
	ctx := context.Background()
	picked := router.Decision{Model: "gpt-6.1-sol", Provider: "openai"}

	passthrough := turnLoopResult{Decision: picked, CallerModelPassthrough: true}
	assert.Empty(t, subscriptionTargetMarker(ctx, http.Header{}, passthrough, picked, picked, routingMarkerFor(passthrough)),
		"a subscription target equal to the caller's model must not invent a badge in passthrough")

	routed := turnLoopResult{Decision: picked}
	routedMarker := routingMarkerFor(routed)
	require.NotEmpty(t, routedMarker)
	got := subscriptionTargetMarker(ctx, http.Header{}, routed, picked, picked, routedMarker)
	assert.Equal(t, routedMarker, got)
	assert.NotContains(t, got, markerReasonSibling, "an unchanged model was not switched after an overload")
}

func TestSubscriptionTargetMarker_SwitchedModelIsWordedAsRescue(t *testing.T) {
	ctx := context.Background()
	picked := router.Decision{Model: "gpt-6-luna", Provider: "openai"}
	target := router.Decision{Model: "gpt-6.1-sol", Provider: "openai"}
	res := turnLoopResult{Decision: picked}

	assert.Equal(t, "✦ **Weave Router** → gpt-6.1-sol · "+markerReasonSibling+"\n\n",
		subscriptionTargetMarker(ctx, http.Header{}, res, picked, target, routingMarkerFor(res)))

	optOut := http.Header{}
	optOut.Set(routingMarkerHeader, "off")
	assert.Empty(t, subscriptionTargetMarker(ctx, optOut, res, picked, target, "kept"),
		"the per-request opt-out still hides a genuine switch badge")
}

func TestSubscriptionTargetMarker_RotationBackToPickRestoresRoutedMarker(t *testing.T) {
	ctx := context.Background()
	picked := router.Decision{Model: "gpt-6-luna", Provider: "openai"}
	sibling := router.Decision{Model: "gpt-6.1-sol", Provider: "openai"}
	res := turnLoopResult{Decision: picked, CallerModelPassthrough: true}
	pickedMarker := routingMarkerFor(res)

	// Always compared against the original pick: after the sibling failed,
	// rotation returning to the pick is not a rescue and must not be badged.
	assert.Contains(t, subscriptionTargetMarker(ctx, http.Header{}, res, picked, sibling, pickedMarker), markerReasonSibling)
	assert.Empty(t, subscriptionTargetMarker(ctx, http.Header{}, res, picked, picked, pickedMarker))
}
