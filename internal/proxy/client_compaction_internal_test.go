package proxy

import (
	"context"
	"fmt"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientRecoveryLeavesUnknownBudgetsAndUtilityTurnsUntouched(t *testing.T) {
	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`, budgetTestFable, clientCompactContinuationPrefix))
	for _, tt := range []struct {
		name   string
		budget router.ClientBudget
		turn   turntype.TurnType
	}{
		{"unknown budget", router.ClientBudget{}, turntype.MainLoop},
		{"probe", smallClientBudget(), turntype.Probe},
		{"subagent", smallClientBudget(), turntype.SubAgentDispatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			before, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
			require.NoError(t, err)
			applied, err := applyClientCompactionRecovery(context.Background(), env, tt.budget, tt.turn, conversationMessagesForRouting(env))
			require.NoError(t, err)
			assert.False(t, applied)
			after, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
			require.NoError(t, err)
			assert.Equal(t, string(before.Body), string(after.Body))
		})
	}
}
