package decisionmodel_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/decisionmodel"

	"github.com/stretchr/testify/require"
)

type scriptedClassifier struct {
	answers map[decisionmodel.QuestionID]decisionmodel.Choice
	asked   [][]decisionmodel.QuestionID
	err     error
}

func (c *scriptedClassifier) Classify(_ context.Context, _ decisionmodel.State, ids []decisionmodel.QuestionID) (map[decisionmodel.QuestionID]decisionmodel.Answer, error) {
	c.asked = append(c.asked, ids)
	if c.err != nil {
		return nil, c.err
	}
	answers := map[decisionmodel.QuestionID]decisionmodel.Answer{}
	for _, id := range ids {
		answers[id] = decisionmodel.Answer{Choice: c.answers[id]}
	}
	return answers, nil
}

func conversation(calls int) []router.ConversationMessage {
	messages := []router.ConversationMessage{{Role: "user", Text: "fix the flaky cache test"}}
	for range calls - 1 {
		messages = append(messages,
			router.ConversationMessage{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "bash"}}},
			router.ConversationMessage{Role: "user", ToolResults: []router.ConversationToolResult{{IsError: true, Text: "FAIL"}}})
	}
	return messages
}

func request(clientApp string, calls int) router.Request {
	return router.Request{ClientApp: clientApp, FeedbackKey: "session", ConversationMessages: conversation(calls)}
}

func TestRoutePicksLineupModel(t *testing.T) {
	for _, tc := range []struct {
		name, clientApp string
		task, call      decisionmodel.Choice
		want            string
	}{
		{name: "claude high task judgment", task: decisionmodel.ChoiceHigh, call: decisionmodel.ChoiceHigh, want: "moonshotai/kimi-k3"},
		{name: "claude high task grunt work", task: decisionmodel.ChoiceHigh, call: decisionmodel.ChoiceLow, want: "z-ai/glm-5.3-flash"},
		{name: "claude low task judgment", task: decisionmodel.ChoiceLow, call: decisionmodel.ChoiceHigh, want: "z-ai/glm-5.3"},
		{name: "claude low task grunt work", task: decisionmodel.ChoiceLow, call: decisionmodel.ChoiceLow, want: "z-ai/glm-5.3-flash"},
		{name: "codex high task judgment", clientApp: "codex", task: decisionmodel.ChoiceHigh, call: decisionmodel.ChoiceHigh, want: "moonshotai/kimi-k3"},
		{name: "codex high task grunt work", clientApp: "codex", task: decisionmodel.ChoiceHigh, call: decisionmodel.ChoiceLow, want: "z-ai/glm-5.3-flash"},
		{name: "codex low task judgment", clientApp: "codex", task: decisionmodel.ChoiceLow, call: decisionmodel.ChoiceHigh, want: "z-ai/glm-5.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: tc.task, decisionmodel.QuestionCallComplexity: tc.call}}
			decision, err := decisionmodel.New(classifier).Route(context.Background(), request(tc.clientApp, 1))
			require.NoError(t, err)
			require.Equal(t, tc.want, decision.Model)
			require.Equal(t, [][]decisionmodel.QuestionID{{decisionmodel.QuestionCallComplexity, decisionmodel.QuestionTaskComplexity}}, classifier.asked)
		})
	}
}

func TestTaskComplexityIsAskedOncePerSession(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceLow}}
	routing := decisionmodel.New(classifier)
	for calls := 1; calls <= 2; calls++ {
		_, err := routing.Route(context.Background(), request("", calls))
		require.NoError(t, err)
	}
	require.Equal(t, [][]decisionmodel.QuestionID{{decisionmodel.QuestionCallComplexity, decisionmodel.QuestionTaskComplexity}, {decisionmodel.QuestionCallComplexity}}, classifier.asked)
}

func TestEscalationIsCheckedEveryThirdCallAndSticks(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionEscalate: decisionmodel.ChoiceYes}}
	routing := decisionmodel.New(classifier)
	var served []string
	for calls := 1; calls <= 5; calls++ {
		decision, err := routing.Route(context.Background(), request("", calls))
		require.NoError(t, err)
		served = append(served, decision.Model)
	}
	require.Equal(t, []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.3-flash", "z-ai/glm-5.3-flash", "moonshotai/kimi-k3", "moonshotai/kimi-k3"}, served)
	require.Contains(t, classifier.asked[3], decisionmodel.QuestionEscalate)
	require.Len(t, classifier.asked, 4, "escalated sessions need no further judging")
}

func TestHighTaskEscalatesOnlyAfterFastModel(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceHigh, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceHigh, decisionmodel.QuestionEscalate: decisionmodel.ChoiceYes}}
	routing := decisionmodel.New(classifier)
	for calls := 1; calls <= 4; calls++ {
		decision, err := routing.Route(context.Background(), request("", calls))
		require.NoError(t, err)
		require.Equal(t, "moonshotai/kimi-k3", decision.Model)
	}
	classifier.answers[decisionmodel.QuestionCallComplexity] = decisionmodel.ChoiceLow
	decision, err := routing.Route(context.Background(), request("", 5))
	require.NoError(t, err)
	require.Equal(t, "z-ai/glm-5.3-flash", decision.Model, "a yes vote before any fast-model call must not escalate")
}

func TestUnavailableModelFallsBackThroughLineup(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceHigh}}
	req := request("", 1)
	req.ExcludedModels = map[string]struct{}{"z-ai/glm-5.3": {}}
	decision, err := decisionmodel.New(classifier).Route(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "moonshotai/kimi-k3", decision.Model, "the excluded judgment model must fall back through the lineup")
}

func TestJudgmentFallbackToTopDoesNotEscalate(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceHigh}}
	decisionRouter := decisionmodel.New(classifier)
	first := request("", 1)
	first.ExcludedModels = map[string]struct{}{"z-ai/glm-5.3": {}}
	decision, err := decisionRouter.Route(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, "moonshotai/kimi-k3", decision.Model)

	classifier.answers[decisionmodel.QuestionCallComplexity] = decisionmodel.ChoiceLow
	second := request("", 2)
	second.ExcludedModels = first.ExcludedModels
	second.PolicyTurnContext = &router.PolicyTurnContext{PreviousServedModel: decision.Model}
	decision, err = decisionRouter.Route(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, "z-ai/glm-5.3-flash", decision.Model)
}

func TestClassifierFailureIsUnavailable(t *testing.T) {
	_, err := decisionmodel.New(&scriptedClassifier{err: errors.New("timeout")}).Route(context.Background(), request("", 1))
	require.ErrorIs(t, err, decisionmodel.ErrUnavailable)
}

func TestBuildStateKeepsObjectiveAndToolStatusOnly(t *testing.T) {
	state := decisionmodel.BuildState(conversation(9))
	require.Equal(t, "fix the flaky cache test", state.Objective)
	require.Empty(t, state.RecentUserMessages)
	require.Len(t, state.RecentAgentTurns, 6)
	require.Equal(t, []decisionmodel.ToolCall{{Name: "bash", Status: decisionmodel.ToolCallError}}, state.RecentAgentTurns[0].ToolCalls)
}

func TestImageRequestSkipsTextOnlyJudgmentModel(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceLow, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceHigh}}
	req := request("", 1)
	req.HasImages = true
	decision, err := decisionmodel.New(classifier).Route(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "moonshotai/kimi-k3", decision.Model, "the text-only judgment model must be skipped on an image turn")
}

func TestAutomaticExclusionFallsBackWithinLineup(t *testing.T) {
	classifier := &scriptedClassifier{answers: map[decisionmodel.QuestionID]decisionmodel.Choice{decisionmodel.QuestionTaskComplexity: decisionmodel.ChoiceHigh, decisionmodel.QuestionCallComplexity: decisionmodel.ChoiceLow}}
	req := request("", 1)
	req.AutomaticExcludedModels = map[string]struct{}{"z-ai/glm-5.3-flash": {}}
	decision, err := decisionmodel.New(classifier).Route(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "moonshotai/kimi-k3", decision.Model, "the excluded fast model must fall back within the lineup")
}

func TestSplitAssistantMessagesCountAsOneCall(t *testing.T) {
	messages := []router.ConversationMessage{
		{Role: "user", Text: "fix the failing test"},
		{Role: "assistant", Text: "running both checks"},
		{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "bash"}}},
		{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "read"}}},
		{Role: "tool", ToolResults: []router.ConversationToolResult{{ToolUseID: "1", IsError: true}}},
		{Role: "tool", ToolResults: []router.ConversationToolResult{{ToolUseID: "2"}}},
	}
	require.Equal(t, 2, decisionmodel.AgentCallIndex(messages))
	state := decisionmodel.BuildState(messages)
	require.Len(t, state.RecentAgentTurns, 1)
	require.Equal(t, []decisionmodel.ToolCall{{Name: "bash", Status: decisionmodel.ToolCallError}, {Name: "read", Status: decisionmodel.ToolCallOK}}, state.RecentAgentTurns[0].ToolCalls)
}
