package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"
)

func TestClassifierPerCallHistoryWithinSingleHumanTurn(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	svc, principal, store := classifierSessionFixture(t, func(_ context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		inputs = append(inputs, input)
		complexity := router.ClassifierComplexity(input.User.Features.ToolCallCount % 4)
		probabilities := make([]float64, 4)
		probabilities[complexity] = 1
		return router.ClassifierPrediction{Complexity: complexity, Probabilities: probabilities}, nil
	})
	ctx := classifierAdmit(t, svc, principal)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic task"))
	failed := true
	for call := range 14 {
		input, err := classifierContextForCall(observation)
		require.NoError(t, err)
		prediction, err := svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, router.ClassifierComplexity(call%4), prediction.Complexity)
		require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: call, ToolErrorCount: call}, inputs[call].User.Features)
		require.Equal(t, call*2, inputs[call].CompletedResponseCount)
		require.Len(t, inputs[call].User.PrecedingResponses, min(10, call*2))
		for _, response := range inputs[call].User.PrecedingResponses {
			require.Equal(t, router.ClassifierComplexity((response.ResponseIndex/2)%4), response.Complexity)
		}
		// A replacement replica must deduplicate this call, not the human turn.
		replica := &Service{now: svc.now}
		require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
		retried, err := replica.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, prediction, retried)
		require.Len(t, inputs, call+1)
		svc = replica
		callID := fmt.Sprintf("call-%d", call)
		// Multiple output messages and an empty block from ONE API invocation
		// must all retain that invocation's prediction, not invent new calls.
		observation.Messages = append(observation.Messages,
			classifierTestText(translate.EscalationRoleAssistant, fmt.Sprintf("response-%d", call)),
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockText, Text: ""},
				{Type: translate.EscalationBlockToolCall, ID: callID, Name: "test"},
			}},
			translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockToolResult, CallID: callID, IsError: &failed},
			}},
		)
	}
	require.Equal(t, "response-8", inputs[13].User.PrecedingResponses[0].Content)
	require.Empty(t, inputs[13].User.PrecedingResponses[9].Content)
}

func TestClassifierCodexExitErrorsAdvanceWithoutChangingReplay(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	svc, principal, _ := classifierSessionFixture(t, func(_ context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		inputs = append(inputs, input)
		return classifierMedium(context.Background(), input)
	})
	ctx := classifierAdmit(t, svc, principal)
	items := []json.RawMessage{json.RawMessage(`{"role":"user","content":"Synthetic task"}`)}
	var previousPrefixes []string
	for completed := 0; completed <= 12; completed++ {
		body, err := json.Marshal(map[string]any{"input": items})
		require.NoError(t, err)
		observation, err := translate.ParseResponsesEscalationObservation(body)
		require.NoError(t, err)
		original, err := json.Marshal(observation)
		require.NoError(t, err)
		observation.CodexToolResults = true
		input, err := classifierContextForCall(observation)
		require.NoError(t, err)
		replayed, err := json.Marshal(observation)
		require.NoError(t, err)
		require.Equal(t, original, replayed)
		if completed > 0 {
			require.Equal(t, previousPrefixes, input.PrefixDigests[:len(previousPrefixes)])
			require.Nil(t, observation.Messages[len(observation.Messages)-1].Blocks[0].IsError)
		}
		previousPrefixes = input.PrefixDigests
		_, err = svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: completed, ToolErrorCount: completed}, inputs[completed].User.Features)
		require.Equal(t, completed, inputs[completed].CompletedResponseCount)
		require.Len(t, inputs[completed].User.PrecedingResponses, min(10, completed))
		_, err = svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Len(t, inputs, completed+1, "exact-prefix retries reuse their prediction")
		items = append(items,
			json.RawMessage(`{"role":"assistant","content":[{"type":"output_text","text":""}]}`),
			json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":"exec-%d","name":"exec_command","arguments":"{\"cmd\":\"exit 7\"}"}`, completed)),
			json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"exec-%d","output":"Chunk ID: abc123\nWall time: 0.0001 seconds\nProcess exited with code 7\nOriginal token count: 1\nOutput:\nSynthetic output\n"}`, completed)),
		)
	}
}

func TestClassifierLegacyPredictionsCannotBecomeCallHistory(t *testing.T) {
	svc, principal, store := classifierSessionFixture(t, classifierMedium)
	ctx := classifierAdmit(t, svc, principal)
	input, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic task")))
	require.NoError(t, err)
	prediction, err := svc.classifyThread(ctx, input)
	require.NoError(t, err)
	thread := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	prediction.InputMessageCount = 0
	store.turns[thread.ThreadID][prediction.TurnDigest] = prediction
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}

func TestClassifierMissingUserPredictionCannotInheritOlderCall(t *testing.T) {
	svc, principal, _ := classifierSessionFixture(t, classifierMedium)
	ctx := classifierAdmit(t, svc, principal)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic first task"))
	input, err := classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.NoError(t, err)
	observation.Messages = append(observation.Messages,
		classifierTestText(translate.EscalationRoleAssistant, "First answer"),
		classifierTestText(translate.EscalationRoleUser, "Unrouted second task"),
		classifierTestText(translate.EscalationRoleAssistant, "Unrouted second answer"),
		classifierTestText(translate.EscalationRoleUser, "Third task"),
	)
	input, err = classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}
