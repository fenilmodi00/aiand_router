package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

// An effort change on the same model invalidates thinking-block signatures
// and the prompt-cache prefix exactly like a model change, so it must count as a switch.
func TestModelSwitched_EffortChangeOnSameModelCountsAsSwitch(t *testing.T) {
	res := turnLoopResult{
		PriorServedModel: "claude-opus-5:low",
		Decision:         router.Decision{Model: "z-ai/glm-5.2", Effort: "xhigh"},
	}

	assert.True(t, res.modelSwitched(),
		"low -> xhigh on the same model must count as a switch")
}

// A bare legacy pin compared against a "model:effort" identity reports a switch — the
// conservative direction — rather than the unsafe one.
func TestModelSwitched_LegacyBarePinReportsSwitchAgainstEffortIdentity(t *testing.T) {
	res := turnLoopResult{
		PriorServedModel: "z-ai/glm-5.2",
		Decision:         router.Decision{Model: "z-ai/glm-5.2", Effort: "high"},
	}

	assert.True(t, res.modelSwitched(),
		"a legacy bare pin must fail safe toward stripping")
}

func TestModelSwitched_NoEffortEitherSideBehavesAsBefore(t *testing.T) {
	same := turnLoopResult{
		PriorServedModel: "z-ai/glm-5.2",
		Decision:         router.Decision{Model: "z-ai/glm-5.2"},
	}
	changed := turnLoopResult{
		PriorServedModel: "z-ai/glm-5.2",
		Decision:         router.Decision{Model: "gpt-5.6-sol"},
	}

	assert.False(t, same.modelSwitched(), "effort-free no-op must not switch")
	assert.True(t, changed.modelSwitched(), "effort-free model change must switch")
}

func TestMaxedOutServedModel_BareIdentityUnchanged(t *testing.T) {
	pin := sessionpin.Pin{
		LastServedModel:   "z-ai/glm-5.2",
		LastOutputTokens:  8192,
		LastTurnEndedAt:   time.Unix(100, 0),
		LastOutputLimitAt: time.Unix(100, 0),
	}

	assert.Equal(t, "z-ai/glm-5.2", maxedOutServedModel(pin))
}
