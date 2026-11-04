package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/escalation"
	"aiand-os/router/internal/router/hmm"
	"aiand-os/router/internal/router/hmm/rosterdata"
	"aiand-os/router/internal/router/hmm/selection"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Ported from upstream de58308f (#1939), with v0's aiand catalog models and
// sidecar-router fixture. The upstream full sliding-ladder e2e is adapted to
// v0's SidecarRouter + stub Decider (upstream drove the retired in-process HMM).

const (
	codexRoutingLow  = "deepseek/deepseek-v4.1-flash"
	codexRoutingHigh = "z-ai/glm-5.3"
)

// Full-history forks inherit both the parent's task and its role instruction.
// Only the final agent-role block identifies the thread issuing the request.
func codexRoutingEnvelope(t *testing.T, agentPath string, toolResult bool) (context.Context, *translate.RequestEnvelope) {
	t.Helper()
	input := []map[string]any{
		{"type": "message", "role": "developer", "content": "<multi_agent_role>You are `/root`, the primary agent in a team of agents.</multi_agent_role>"},
		{"type": "message", "role": "user", "content": "Synthetic repository instructions"},
		{"type": "message", "role": "user", "content": "Repair a synthetic parser and validate its behavior"},
	}
	if agentPath != "/root" {
		input = append(input,
			map[string]any{"type": "message", "role": "developer", "content": "<multi_agent_role>You are an agent in a team of agents.</multi_agent_role>"},
			map[string]any{"type": "agent_message", "author": "/root", "recipient": agentPath, "content": []map[string]string{{"type": "input_text", "text": fmt.Sprintf("Message Type: NEW_TASK\nTask name: %s\nSender: /root\nPayload:\nReview the synthetic parser", agentPath)}}},
		)
	}
	if toolResult {
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "synthetic-read", "name": "read_file", "arguments": `{"path":"parser.go"}`},
			map[string]any{"type": "function_call_output", "call_id": "synthetic-read", "output": "Synthetic parser source"},
		)
	}
	body, err := json.Marshal(map[string]any{
		"model": "auto", "instructions": "Synthetic coding assistant instructions", "input": input,
		"max_output_tokens": 4096,
		"tools":             []map[string]any{{"type": "function", "name": "read_file", "parameters": map[string]any{"type": "object"}}},
	})
	require.NoError(t, err)
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(converted.Body)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, SessionID: "synthetic-codex-conversation"})
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, body)
	return router.WithStrategy(ctx, router.StrategyHMM), env
}

// codexRoutingPrediction returns the result the stub sidecar reports for one
// group at the given confidence.
func codexRoutingPrediction(t *testing.T, group escalation.Group, confidence float64) policy.Result {
	t.Helper()
	classOrder := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	probabilities := map[string]float64{}
	for _, label := range classOrder {
		probabilities[label] = (1 - confidence) / 3
	}
	probabilities[string(group)] = confidence
	return policy.Result{
		SchemaVersion: policy.SchemaVersionV4, RouteID: "synthetic-route",
		ClassOrder: classOrder, PredictedLabel: string(group), ClassProbabilities: probabilities,
	}
}

// codexRoutingDecider reports the configured prediction, then the per-turn
// candidate the arm selector offers.
type codexRoutingDecider struct {
	prediction policy.Result
}

func (c *codexRoutingDecider) Decide(_ context.Context, query policy.Query) (policy.Result, error) {
	if len(query.Candidates) == 0 {
		return policy.Result{}, errors.New("no candidates offered")
	}
	result := c.prediction
	for _, candidate := range query.Candidates {
		if candidate.CatalogID == codexRoutingPredictionModel(c.prediction.PredictedLabel) {
			result.Model = candidate.CatalogID
			result.Provider = candidate.Provider
			result.ArmID = candidate.ArmID
			return result, nil
		}
	}
	result.Model = query.Candidates[0].CatalogID
	result.Provider = query.Candidates[0].Provider
	result.ArmID = query.Candidates[0].ArmID
	return result, nil
}

func codexRoutingPredictionModel(label string) string {
	if label == string(escalation.Low) {
		return codexRoutingLow
	}
	return codexRoutingHigh
}

func newCodexRoutingService(t *testing.T, upstream *codexTitleProvider, store *codexRoutingPinStore, decider *codexRoutingDecider) *Service {
	t.Helper()
	escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	roster := &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, SHA256: strings.Repeat("b", 64), ClassOrder: escalationClasses, Clusters: map[string]rosterdata.Cluster{}}
	for _, class := range escalationClasses {
		arm := armForClass(class)
		roster.Clusters[class] = rosterdata.Cluster{Arms: []string{arm}, ArmScores: map[string]float64{arm: 1}}
	}
	resolver := policy.NewResolver(map[string]struct{}{codexRoutingLow: {}, codexRoutingHigh: {}}, map[string]struct{}{providers.ProviderAIAND: {}}, func(m catalog.Model) string { return m.ID })
	capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, AuthoritativePerTurnSelection: true}
	routing := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM, Unavailable: hmm.ErrHMMUnavailable}, decider, resolver).
		WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
	return NewService(nil, map[string]providers.Client{providers.ProviderAIAND: upstream}, nil, false, nil, store, false, providers.ProviderAIAND, codexRoutingHigh, nil).
		WithOpenAIResponsesBroad(true).
		WithAvailableModels(map[string]struct{}{codexRoutingLow: {}, codexRoutingHigh: {}}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: routing, Capabilities: capabilities, Unavailable: hmm.ErrHMMUnavailable})
}

func armForClass(class string) string {
	if class == string(escalation.Low) {
		return codexRoutingLow
	}
	return codexRoutingHigh
}

type codexRoutingPinKey struct {
	session [sessionpin.SessionKeyLen]byte
	role    string
}

type codexRoutingPinStore struct {
	*stubPinStore
	pins map[codexRoutingPinKey]sessionpin.Pin
}

func (s *codexRoutingPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.pins[codexRoutingPinKey{key, role}]
	return pin, found, nil
}

func (s *codexRoutingPinStore) Upsert(_ context.Context, pin sessionpin.Pin) error {
	s.pins[codexRoutingPinKey{pin.SessionKey, pin.Role}] = pin
	return nil
}

type codexTitleProvider struct {
	endpoints []providers.Endpoint
	models    []string
}

func (p *codexTitleProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.endpoints = append(p.endpoints, prep.Endpoint)
	p.models = append(p.models, gjson.GetBytes(prep.Body, "model").String())
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	return nil
}

func (p *codexTitleProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// Child turns must not share the parent's routing pin: a full-history fork
// inherits the parent's deliveries, and only the final agent delivery names
// the child. The parent's ladder must not restart on a child downgrade, and
// parent-only forcing must not reach a child while conversation-wide forcing
// still does.
func TestCodexAgentRoutingIsolation(t *testing.T) {
	upstream := &codexTitleProvider{}
	store := &codexRoutingPinStore{stubPinStore: newStubPinStore(), pins: make(map[codexRoutingPinKey]sessionpin.Pin)}
	decider := &codexRoutingDecider{}
	svc := newCodexRoutingService(t, upstream, store, decider)
	installationID := "11111111-1111-1111-1111-111111111111"
	rootCtx, root := codexRoutingEnvelope(t, "/root", true)
	childCtx, child := codexRoutingEnvelope(t, "/root/reviewer", true)
	rootCtx = context.WithValue(context.WithValue(rootCtx, InstallationIDContextKey{}, installationID), APIKeyIDContextKey{}, "synthetic-api-key")
	childCtx = context.WithValue(context.WithValue(childCtx, InstallationIDContextKey{}, installationID), APIKeyIDContextKey{}, "synthetic-api-key")

	turns := []struct {
		child bool
		group escalation.Group
		want  string
	}{
		{false, escalation.High, codexRoutingHigh},
		{true, escalation.Low, codexRoutingLow},
		{false, escalation.High, codexRoutingHigh},
		{true, escalation.Low, codexRoutingLow},
	}
	var parentModels, childModels []string
	for i, turn := range turns {
		decider.prediction = codexRoutingPrediction(t, turn.group, 0.9)
		ctx := rootCtx
		if turn.child {
			ctx = childCtx
		}
		body := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, recorder, req), "turn %d", i)
		require.Len(t, upstream.models, i+1)
		model := upstream.models[i]
		if turn.child {
			childModels = append(childModels, model)
		} else {
			parentModels = append(parentModels, model)
		}
	}
	// The child's separate session key keeps its Low-group pin off the
	// parent's High-group pin and vice versa: each conversation serves its own
	// group's arm instead of the first turn's model for both.
	assert.Equal(t, []string{codexRoutingHigh, codexRoutingHigh}, parentModels,
		"child downgrades must not move the parent off its own pin")
	assert.Equal(t, []string{codexRoutingLow, codexRoutingLow}, childModels,
		"the parent's pin must not force a child onto its model")
	assert.NotEqual(t, deriveSessionKeyForRequest(rootCtx, root, "synthetic-api-key"),
		deriveSessionKeyForRequest(childCtx, child, "synthetic-api-key"))
}

// Pin identity keys off the last agent delivery's recipient: sibling children
// and nested forks each get their own session state, repeated turns stay
// stable, and non-Codex clients are untouched.
func TestCodexAgentPinIdentity(t *testing.T) {
	parentCtx, parent := codexRoutingEnvelope(t, "/root", false)
	childCtx, child := codexRoutingEnvelope(t, "/root/reviewer", false)
	siblingCtx, sibling := codexRoutingEnvelope(t, "/root/checker", false)
	nestedCtx, nested := codexRoutingEnvelope(t, "/root/reviewer/checker", false)
	parentKey := deriveSessionKeyForRequest(parentCtx, parent, "synthetic-api-key")
	childKey := deriveSessionKeyForRequest(childCtx, child, "synthetic-api-key")
	siblingKey := deriveSessionKeyForRequest(siblingCtx, sibling, "synthetic-api-key")
	assert.NotEqual(t, parentKey, childKey)
	assert.NotEqual(t, childKey, siblingKey)
	assert.NotEqual(t, childKey, deriveSessionKeyForRequest(nestedCtx, nested, "synthetic-api-key"))
	nextCtx, next := codexRoutingEnvelope(t, "/root/reviewer", true)
	assert.Equal(t, childKey, deriveSessionKeyForRequest(nextCtx, next, "synthetic-api-key"))
	assert.Equal(t, deriveForceModelSessionKeyForRequest(parentCtx, parent, "synthetic-api-key", parentKey),
		deriveForceModelSessionKeyForRequest(childCtx, child, "synthetic-api-key", childKey),
		"conversation-wide forcing must still share one key")
	assert.Equal(t, parentKey, deriveSessionKeyForRequest(
		context.WithValue(parentCtx, nativeResponsesBodyContextKey{}, []byte(`{"input":[{"type":"agent_message","recipient":"/root","content":[]}]}`)),
		parent, "synthetic-api-key"), "root deliveries preserve the existing parent key")
}

// Only an agent delivery to a non-root path in the /root tree forks the key;
// prose recipients, traversal, and other trees do not.
func TestCodexAgentMessageBoundary(t *testing.T) {
	ctx, env := codexRoutingEnvelope(t, "/root", false)
	parentKey := deriveSessionKeyForRequest(ctx, env, "synthetic-api-key")
	tests := []struct {
		name   string
		native string
		child  bool
	}{
		{"child recipient", `{"input":[{"type":"agent_message","recipient":"/root/reviewer"}]}`, true},
		{"nested recipient", `{"input":[{"type":"agent_message","recipient":"/root/reviewer/checker"}]}`, true},
		{"last delivery wins", `{"input":[{"type":"agent_message","recipient":"/root/reviewer"},{"type":"agent_message","recipient":"/root"}]}`, false},
		{"inherited parent delivery", `{"input":[{"type":"agent_message","recipient":"/root"},{"type":"agent_message","recipient":"/root/reviewer"}]}`, true},
		{"user text is not identity", `{"input":[{"type":"message","role":"user","recipient":"/root/reviewer","content":"Message Type: NEW_TASK\nTask name: /root/reviewer"}]}`, false},
		{"assistant text is not identity", `{"input":[{"type":"message","role":"assistant","recipient":"/root/reviewer"}]}`, false},
		{"missing recipient", `{"input":[{"type":"agent_message"}]}`, false},
		{"different tree", `{"input":[{"type":"agent_message","recipient":"/other/reviewer"}]}`, false},
		{"empty segment", `{"input":[{"type":"agent_message","recipient":"/root//reviewer"}]}`, false},
		{"traversal", `{"input":[{"type":"agent_message","recipient":"/root/../reviewer"}]}`, false},
		{"bounded path", `{"input":[{"type":"agent_message","recipient":"/root/` + strings.Repeat("a", 300) + `"}]}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestCtx := context.WithValue(ctx, nativeResponsesBodyContextKey{}, []byte(test.native))
			key := deriveSessionKeyForRequest(requestCtx, env, "synthetic-api-key")
			assert.Equal(t, test.child, key != parentKey)
			nonCodex := context.WithValue(requestCtx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppOpencode, SessionID: "synthetic-codex-conversation"})
			assert.Equal(t, parentKey, deriveSessionKeyForRequest(nonCodex, env, "synthetic-api-key"))
		})
	}
}
