package turntype_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

// A repair loop in progress — deep conversation plus substantial tool output —
// decodes to the lanes that measurably lead that class. Each row pins one
// boundary of that decode, so a deleted or loosened signal fails the table.
func TestAgenticRepairPreference_TableDriven(t *testing.T) {
	tests := []struct {
		name  string
		turn  turntype.TurnType
		body  []byte
		feats translate.RoutingFeatures
		want  bool
	}{
		{
			name: "deep history plus test-output-sized tool results is a repair loop",
			turn: turntype.ToolResult,
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", 25_000)+`]}`,
				`{"name":"Bash"}`),
			feats: translate.RoutingFeatures{MessageCount: 8},
			want:  true,
		},
		{
			name: "shallow history is a fresh question, not a repair",
			turn: turntype.MainLoop,
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", 25_000)+`]}`,
				`{"name":"Bash"}`),
			feats: translate.RoutingFeatures{MessageCount: 2},
			want:  false,
		},
		{
			name: "deep history with only a small tool result is a quick check",
			turn: turntype.ToolResult,
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", 19_999)+`]}`,
				`{"name":"Bash"}`),
			feats: translate.RoutingFeatures{MessageCount: 40},
			want:  false,
		},
		{
			name: "the payload threshold boundary admits at exactly 20000",
			turn: turntype.ToolResult,
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", 20_000)+`]}`,
				`{"name":"Bash"}`),
			feats: translate.RoutingFeatures{MessageCount: 6},
			want:  true,
		},
		{
			name:  "no tools at all is a plain conversation",
			turn:  turntype.MainLoop,
			body:  anthropicBody(`{"role":"user","content":[{"type":"text","text":"hi"}]}`, ""),
			feats: translate.RoutingFeatures{MessageCount: 50},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(tt.body)
			require.NoError(t, err)
			got := turntype.AgenticRepairPreference(tt.turn, tt.feats, env)
			if !tt.want {
				assert.Nil(t, got, "a non-repair turn must not prefer the repair lanes")
				return
			}
			assert.Equal(t, turntype.AgenticRepairModels, got)
		})
	}
}

// Every non-scored turn type keeps the deployment default: those turns have
// their own hard pins or their own purpose.
func TestAgenticRepairPreference_NonScoredTurnsNeverPrefer(t *testing.T) {
	unscored := []turntype.TurnType{
		turntype.Compaction,
		turntype.Probe,
		turntype.TitleGen,
		turntype.Recap,
		turntype.Classifier,
		turntype.SubAgentDispatch,
	}
	env, err := translate.ParseAnthropic(anthropicBody(
		`{"role":"user","content":[`+toolResultBlock("toolu_1", 500_000)+`]}`,
		`{"name":"Bash"},{"name":"Edit"},{"name":"Grep"}`))
	require.NoError(t, err)
	feats := translate.RoutingFeatures{MessageCount: 200, Tokens: 100_000, HasTools: true}
	for _, turnType := range unscored {
		t.Run(string(turnType), func(t *testing.T) {
			assert.Nil(t, turntype.AgenticRepairPreference(turnType, feats, env),
				"turn type %s must never prefer the repair lanes", turnType)
		})
	}
}

// A nil envelope must not panic and must not prefer anything: the decode runs on
// converted request shapes that can legitimately be absent.
func TestAgenticRepairPreference_NilEnvelope(t *testing.T) {
	feats := translate.RoutingFeatures{MessageCount: 50}
	assert.NotPanics(t, func() {
		_ = turntype.AgenticRepairPreference(turntype.ToolResult, feats, nil)
	})
	assert.Nil(t, turntype.AgenticRepairPreference(turntype.ToolResult, feats, nil))
}
