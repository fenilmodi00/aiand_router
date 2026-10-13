package turntype_test

import (
	"strings"
	"testing"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolResultBlock builds one Anthropic tool_result content block carrying n
// bytes of payload.
func toolResultBlock(id string, n int) string {
	return `{"type":"tool_result","tool_use_id":"` + id + `","content":"` + strings.Repeat("x", n) + `"}`
}

func anthropicBody(messages string, tools string) []byte {
	return []byte(`{"model":"claude-opus-4-7","messages":[` + messages + `],"tools":[` + tools + `]}`)
}

func TestPremiumWorthyCap_TableDriven(t *testing.T) {
	const (
		premium = turntype.PremiumWorthyCapMultiple
		tiny    = 39_999
		huge    = 40_000 + 1
	)

	tests := []struct {
		name     string
		turnType turntype.TurnType
		feats    translate.RoutingFeatures
		body     []byte
		wantOK   bool
	}{
		{
			name:     "tool result turn with large tool results relaxes",
			turnType: turntype.ToolResult,
			feats:    translate.RoutingFeatures{MessageCount: 6, LastKind: "tool_result"},
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", huge)+`]}`,
				`{"name":"Bash"}`,
			),
			wantOK: true,
		},
		{
			name:     "tool result turn with small tool results stays at deployment cap",
			turnType: turntype.ToolResult,
			feats:    translate.RoutingFeatures{MessageCount: 6, LastKind: "tool_result"},
			body: anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", tiny)+`]}`,
				`{"name":"Bash"}`,
			),
			wantOK: false,
		},
		{
			name:     "main loop tiny single message does not relax",
			turnType: turntype.MainLoop,
			feats:    translate.RoutingFeatures{Tokens: 12, MessageCount: 1},
			body:     anthropicBody(`{"role":"user","content":[{"type":"text","text":"hi"}]}`, ""),
			wantOK:   false,
		},
		{
			name:     "agentic prompt budget relaxes",
			turnType: turntype.MainLoop,
			feats:    translate.RoutingFeatures{Tokens: 30_000, MessageCount: 3},
			body:     anthropicBody(`{"role":"user","content":[{"type":"text","text":"go"}]}`, ""),
			wantOK:   true,
		},
		{
			name:     "multi-tool declaration relaxes",
			turnType: turntype.MainLoop,
			feats:    translate.RoutingFeatures{HasTools: true, MessageCount: 3},
			body: anthropicBody(
				`{"role":"user","content":[{"type":"text","text":"go"}]}`,
				`{"name":"Bash"},{"name":"Edit"}`,
			),
			wantOK: true,
		},
		{
			name:     "single tool declaration alone does not relax",
			turnType: turntype.MainLoop,
			feats:    translate.RoutingFeatures{HasTools: true, MessageCount: 3},
			body: anthropicBody(
				`{"role":"user","content":[{"type":"text","text":"go"}]}`,
				`{"name":"Bash"}`,
			),
			wantOK: false,
		},
		{
			name:     "long conversation alone relaxes",
			turnType: turntype.MainLoop,
			feats:    translate.RoutingFeatures{MessageCount: 20},
			body:     anthropicBody(`{"role":"user","content":[{"type":"text","text":"go"}]}`, ""),
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(tt.body)
			require.NoError(t, err)
			got, ok := turntype.PremiumWorthyCap(tt.turnType, tt.feats, env)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, premium, got)
			}
		})
	}
}

// Every non-scored turn type must return the deployment cap default, no matter
// how premium-worthy its other signals look: they are hard-pinned (or
// unpinned-but-purpose-scoped) and must never widen the premium pool.
func TestPremiumWorthyCap_NonScoredTurnsNeverRelax(t *testing.T) {
	unscored := []turntype.TurnType{
		turntype.Compaction,
		turntype.Probe,
		turntype.TitleGen,
		turntype.Recap,
		turntype.Classifier,
		turntype.SubAgentDispatch,
	}

	for _, turnType := range unscored {
		t.Run(string(turnType), func(t *testing.T) {
			env, err := translate.ParseAnthropic(anthropicBody(
				`{"role":"user","content":[`+toolResultBlock("toolu_1", 400_000)+`]}`,
				`{"name":"Bash"},{"name":"Edit"},{"name":"Grep"}`,
			))
			require.NoError(t, err)
			// Signals that relax a MainLoop turn outright.
			feats := translate.RoutingFeatures{
				Tokens:       500_000,
				HasTools:     true,
				MessageCount: 500,
			}
			got, ok := turntype.PremiumWorthyCap(turnType, feats, env)
			assert.False(t, ok, "turn type %s must never relax the cost cap", turnType)
			assert.Zero(t, got)
		})
	}
}
