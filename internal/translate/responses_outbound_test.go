package translate_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func itoa(n int) string { return strconv.Itoa(n) }

func gjsonStopReason(b []byte) string {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	s, _ := m["stop_reason"].(string)
	return s
}

func openAIReasoningTestSignature(t *testing.T, id, enc, scope string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"v": 1, "provider": "openai", "id": id, "enc": enc, "scope": scope})
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(b)
}

func decodeOpenAIReasoningTestSignature(t *testing.T, sig string) map[string]any {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// Unknown tool calls stay in Anthropic history for the client's feedback loop,
// but OpenAI Responses only accepts the restricted function-name alphabet.
func TestPrepareOpenAIResponses_SanitizesHistoricalToolName(t *testing.T) {
	body := []byte(`{
      "model":"z-ai/glm-5.2",
      "messages":[
        {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"dots schema search --scope all","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}
      ]
    }`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel:  "moonshotai/kimi-k2.7",
		Capabilities: router.Lookup("moonshotai/kimi-k2.7"),
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	input, _ := out["input"].([]any)
	require.Len(t, input, 2)
	call, _ := input[0].(map[string]any)
	assert.Regexp(t, `^dots_schema_search_--scope_all_[0-9a-f]{10}$`, call["name"])
	assert.Equal(t, "dots schema search --scope all", prep.ResponseToolNames[call["name"].(string)])
}

func topLevelResponsesFields(t *testing.T, body []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), token)

	fields := make([]string, 0)
	for decoder.More() {
		token, err = decoder.Token()
		require.NoError(t, err)
		field, ok := token.(string)
		require.True(t, ok, "top-level JSON key must be a string")
		var value json.RawMessage
		require.NoError(t, decoder.Decode(&value))
		fields = append(fields, field)
	}

	token, err = decoder.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('}'), token)
	return fields
}

func responseFieldIndex(fields []string, want string) int {
	for index, field := range fields {
		if field == want {
			return index
		}
	}
	return -1
}

// The Responses body keeps static controls ahead of append-only conversation
// input so those controls remain in the cacheable prefix across turns.
func TestPrepareOpenAIResponses_StaticFieldsPrecedeInput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parse      func([]byte) (*translate.RequestEnvelope, error)
		body       string
		staticKeys []string
	}{
		{
			name:  "Anthropic source",
			parse: translate.ParseAnthropic,
			body: `{"model":"z-ai/glm-5.2","max_tokens":1024,
				"system":"You are helpful.",
				"tools":[{"name":"bash","description":"run","input_schema":{"type":"object"}}],
				"tool_choice":{"type":"auto"},
				"temperature":0.7,"top_p":0.9,
				"messages":[{"role":"user","content":"hi"}]}`,
			staticKeys: []string{"tools", "tool_choice", "max_output_tokens", "temperature", "top_p"},
		},
		{
			name:  "OpenAI chat source",
			parse: translate.ParseOpenAI,
			body: `{"model":"deepseek/deepseek-v4-flash","max_tokens":1024,
				"tools":[{"type":"function","function":{"name":"bash","description":"run","parameters":{"type":"object"}}}],
				"tool_choice":"auto","parallel_tool_calls":false,
				"temperature":0.7,"top_p":0.9,
				"messages":[{"role":"user","content":"hi"}]}`,
			staticKeys: []string{"tools", "tool_choice", "parallel_tool_calls", "max_output_tokens", "temperature", "top_p"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			prepared, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
				TargetModel:  "deepseek/deepseek-v4-flash",
				Capabilities: router.Lookup("deepseek/deepseek-v4-flash"),
			})
			require.NoError(t, err)

			fields := topLevelResponsesFields(t, prepared.Body)
			inputIndex := responseFieldIndex(fields, "input")
			require.NotEqual(t, -1, inputIndex, "Responses body must include input")
			for _, key := range tc.staticKeys {
				fieldIndex := responseFieldIndex(fields, key)
				require.NotEqual(t, -1, fieldIndex, "Responses body must include %q", key)
				assert.Less(t, fieldIndex, inputIndex, "%q must precede input", key)
			}
		})
	}
}

// TestPrepareOpenAIResponses_ToolChoiceVariants covers the Anthropic ->
// Responses tool_choice mapping for "any", "none", and named-tool; "auto" is
// covered by TestPrepareOpenAIResponses_RequestShape.
func TestPrepareOpenAIResponses_ToolChoiceVariants(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice string
		want       any
	}{
		{"any", `{"type":"any"}`, "required"},
		{"none", `{"type":"none"}`, "none"},
		{"tool", `{"type":"tool","name":"bash"}`, map[string]any{"type": "function", "name": "bash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{
				"model":"z-ai/glm-5.2","max_tokens":4096,
				"tools":[{"name":"bash","input_schema":{"type":"object"}}],
				"tool_choice":` + tc.toolChoice + `,
				"messages":[{"role":"user","content":"fix the bug"}]
			}`)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
				TargetModel:  "z-ai/glm-5.3",
				Capabilities: router.Lookup("z-ai/glm-5.3"),
			})
			require.NoError(t, err)

			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			assert.Equal(t, tc.want, out["tool_choice"])
		})
	}
}

// Gemini sessions smuggle a thoughtSignature into tool_use ids
// (call_xxx__thought__<sig>, often >1KB). On re-route to a gpt-5.x Responses
// model, call_id must be stripped and clamped to 64 chars or the upstream
// 400s; tool_use and tool_result must still share the clamped call_id.
func TestPrepareOpenAIResponses_ClampsGeminiThoughtSignatureCallID(t *testing.T) {
	longSig := strings.Repeat("A", 1300) // valid base64url, > 64 chars
	id := "call_abc123__thought__" + longSig
	require.Greater(t, len(id), 1300)
	body := []byte(`{
		"model":"z-ai/glm-5.2","max_tokens":1024,
		"messages":[
			{"role":"user","content":"continue"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":` + strconv.Quote(id) + `,"name":"Read","input":{"file_path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":` + strconv.Quote(id) + `,"content":"ok"}
			]}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3"), ForceReasoningEffort: "high"})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	input, _ := out["input"].([]any)
	var fnCallID, fnOutCallID string
	for _, item := range input {
		m, _ := item.(map[string]any)
		switch m["type"] {
		case "function_call":
			fnCallID, _ = m["call_id"].(string)
		case "function_call_output":
			fnOutCallID, _ = m["call_id"].(string)
		}
	}
	require.NotEmpty(t, fnCallID)
	require.NotEmpty(t, fnOutCallID)
	assert.LessOrEqual(t, len(fnCallID), 64, "call_id must fit OpenAI's 64-char limit")
	assert.Equal(t, "call_abc123", fnCallID, "the bare id (sans __thought__) is within the limit")
	assert.Equal(t, fnCallID, fnOutCallID, "tool_use and tool_result must share the clamped call_id")
}

// Encrypted reasoning is bound to the account+model that produced it: Cortex
// answers "encrypted reasoning was created for a different account or model"
// and the rejected turn ends the client session, so a turn dispatched
// anywhere else drops the item instead of replaying it.
func TestPrepareOpenAIResponses_DropsReasoningMintedOnAnotherAccount(t *testing.T) {
	sig := openAIReasoningTestSignature(t, "rs_prev", "enc_prev", "scope_a")
	// Claude Code drops the thinking block but echoes the tool_use id, so the
	// envelope rides on both carriers and both must honor the scope.
	carrierID := "toolu_1__openai_reasoning__" + base64.RawURLEncoding.EncodeToString([]byte(sig))
	body := []byte(`{
		"model":"z-ai/glm-5.2","max_tokens":1024,
		"thinking":{"type":"enabled","budget_tokens":8192},
		"messages":[
			{"role":"user","content":"continue"},
			{"role":"assistant","content":[
				{"type":"text","text":"I'll inspect it."},
				{"type":"thinking","thinking":"summary","signature":` + strconv.Quote(sig) + `},
				{"type":"tool_use","id":` + strconv.Quote(carrierID) + `,"name":"Read","input":{"file_path":"main.go"}}
			]}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	for name, scope := range map[string]string{
		"another account or model": "scope_b",
		"unknown target":           "",
	} {
		t.Run(name, func(t *testing.T) {
			prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
				TargetModel:          "z-ai/glm-5.3",
				Capabilities:         router.Lookup("z-ai/glm-5.3"),
				ForceReasoningEffort: "high",
				ReasoningReplayScope: scope,
			})
			require.NoError(t, err)

			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			input, _ := out["input"].([]any)
			var toolCall map[string]any
			for _, item := range input {
				m, _ := item.(map[string]any)
				require.NotEqual(t, "reasoning", m["type"], "foreign encrypted reasoning must not be replayed")
				if m["type"] == "function_call" {
					toolCall = m
				}
			}
			// The turn still goes out; only the undecryptable item is dropped.
			require.NotNil(t, toolCall)
			assert.Equal(t, "toolu_1", toolCall["call_id"])
		})
	}
}

func TestResponsesToAnthropicResponse_StopReasons(t *testing.T) {
	// max tokens
	mx := []byte(`{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	out, err := translate.ResponsesToAnthropicResponse(mx, "z-ai/glm-5.3", "scope_a")
	require.NoError(t, err)
	assert.Equal(t, "max_tokens", gjsonStopReason(out))
	// plain end_turn
	et := []byte(`{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	out, err = translate.ResponsesToAnthropicResponse(et, "z-ai/glm-5.3", "scope_a")
	require.NoError(t, err)
	assert.Equal(t, "end_turn", gjsonStopReason(out))
	filtered := []byte(`{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`)
	_, err = translate.ResponsesToAnthropicResponse(filtered, "z-ai/glm-5.3", "scope_a")
	require.Error(t, err)
}

// Anthropic's numeric thinking budget must be normalized to Gemini 3.x's named
// thinkingLevel before generic capability validation, otherwise a valid HMM
// route to Gemini fails locally before any upstream call.
func TestPrepareGemini_ThinkingBudgetToThinkingConfig(t *testing.T) {
	for _, tc := range []struct {
		budget int
		level  string
	}{
		{budget: 2048, level: "low"},
		{budget: 8192, level: "medium"},
		{budget: 31999, level: "high"},
	} {
		t.Run(tc.level, func(t *testing.T) {
			body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":` + itoa(tc.budget) + `}}`)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := env.PrepareGemini(nil, translate.EmitOptions{
				TargetModel:  "gemini-3.7-flash",
				Capabilities: alwaysOnSpec,
			})
			require.NoError(t, err)

			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			gen, ok := out["generationConfig"].(map[string]any)
			require.True(t, ok, "generationConfig present")
			thinking, ok := gen["thinkingConfig"].(map[string]any)
			require.True(t, ok, "thinkingConfig set from thinking budget")
			assert.Equal(t, tc.level, thinking["thinkingLevel"])
			assert.NotContains(t, thinking, "thinkingBudget")
		})
	}
}

// gemini-2.5 (legacy) keeps the numeric thinkingBudget — thinkingLevel is 3.x only.
func TestPrepareGemini_ThinkingBudget_Legacy25(t *testing.T) {
	body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":31999}}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareGemini(nil, translate.EmitOptions{
		TargetModel:  "gemini-2.5-pro",
		Capabilities: reasoningSpec,
	})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	gen, _ := out["generationConfig"].(map[string]any)
	tc, ok := gen["thinkingConfig"].(map[string]any)
	require.True(t, ok, "thinkingConfig set from thinking budget")
	assert.EqualValues(t, 31999, tc["thinkingBudget"], "budget must be preserved exactly")
	_, hasLevel := tc["thinkingLevel"]
	assert.False(t, hasLevel, "gemini-2.5 must NOT send thinkingLevel")
}

// An Anthropic image block must survive the Responses emit as a typed
// input_image part — a non-reasoning model now routes here too, so images can
// no longer be skipped.
func TestPrepareOpenAIResponses_ImageBlocks(t *testing.T) {
	body := []byte(`{
		"model":"z-ai/glm-5.2","max_tokens":1024,
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"what is this"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAB"}},
				{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}
			]}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel:  "deepseek/deepseek-v4-flash",
		Capabilities: router.Lookup("deepseek/deepseek-v4-flash"),
	})
	require.NoError(t, err)

	var out struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	require.Len(t, out.Input, 1)
	require.Len(t, out.Input[0].Content, 3)
	assert.Equal(t, "input_text", out.Input[0].Content[0].Type)
	assert.Equal(t, "what is this", out.Input[0].Content[0].Text)
	assert.Equal(t, "input_image", out.Input[0].Content[1].Type)
	assert.Equal(t, "data:image/png;base64,AAAB", out.Input[0].Content[1].ImageURL,
		"a base64 source becomes a data URL")
	assert.Equal(t, "input_image", out.Input[0].Content[2].Type)
	assert.Equal(t, "https://example.com/a.png", out.Input[0].Content[2].ImageURL)
}

// function_call_output is text-only; a nested image must be hoisted into a
// following message instead of vanishing.
func TestPrepareOpenAIResponses_ToolResultImageHoisted(t *testing.T) {
	body := []byte(`{
		"model":"z-ai/glm-5.2","max_tokens":1024,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"shot","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[
				{"type":"text","text":"captured"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBC"}}
			]}]}
		]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel:  "deepseek/deepseek-v4-flash",
		Capabilities: router.Lookup("deepseek/deepseek-v4-flash"),
	})
	require.NoError(t, err)

	var out struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	require.Len(t, out.Input, 3)
	assert.Equal(t, "function_call", out.Input[0].Type)
	assert.Equal(t, "function_call_output", out.Input[1].Type)
	assert.Equal(t, "call_1", out.Input[1].CallID)
	assert.Contains(t, out.Input[1].Output, "captured")
	assert.Equal(t, "user", out.Input[2].Role, "the hoisted image rides a user message")
	require.Len(t, out.Input[2].Content, 1)
	assert.Equal(t, "input_image", out.Input[2].Content[0].Type)
	assert.Equal(t, "data:image/png;base64,BBBC", out.Input[2].Content[0].ImageURL)
}

// Reasoning gpt-5.x models 400 on a supplied temperature; everything else on
// Responses samples normally, so the client's knobs must reach a gpt-4.1.
func TestPrepareOpenAIResponses_Samplers(t *testing.T) {
	body := []byte(`{
		"model":"z-ai/glm-5.2","max_tokens":1024,"temperature":0.2,"top_p":0.9,
		"messages":[{"role":"user","content":"hi"}]
	}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	for _, tc := range []struct {
		model    string
		wantTemp bool
		spec     router.ModelSpec
	}{
		{model: "deepseek/deepseek-v4-flash", wantTemp: true, spec: router.Lookup("deepseek/deepseek-v4-flash")},
		{model: "gpt-5.4-mini", wantTemp: false, spec: reasoningSpec},
	} {
		t.Run(tc.model, func(t *testing.T) {
			prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
				TargetModel:  tc.model,
				Capabilities: tc.spec,
			})
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(prep.Body, &out))
			_, hasTemp := out["temperature"]
			_, hasTopP := out["top_p"]
			assert.Equal(t, tc.wantTemp, hasTemp)
			assert.Equal(t, tc.wantTemp, hasTopP)
			if !tc.wantTemp {
				return
			}
			// The reasoning-output floor is a reasoning-model allowance; a
			// non-reasoning target keeps the ceiling the client asked for.
			assert.EqualValues(t, 1024, out["max_output_tokens"])
		})
	}
}

// Stop sequences have no Responses equivalent, so a turn using them keeps the
// chat/completions projection rather than losing them.
func TestRequiresChatCompletionsParams(t *testing.T) {
	nonReasoning := router.Lookup("deepseek/deepseek-v4-flash")
	reasoning := reasoningSpec

	withStops, err := translate.ParseAnthropic([]byte(
		`{"model":"z-ai/glm-5.2","max_tokens":16,"stop_sequences":["\n\n"],"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.True(t, withStops.RequiresChatCompletionsParams(nonReasoning))
	assert.False(t, withStops.RequiresChatCompletionsParams(reasoning),
		"a reasoning target rejects stop on chat/completions too, so it is already dropped")

	empty, err := translate.ParseAnthropic([]byte(
		`{"model":"z-ai/glm-5.2","max_tokens":16,"stop_sequences":[],"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.False(t, empty.RequiresChatCompletionsParams(nonReasoning))

	chatIngress, err := translate.ParseOpenAI([]byte(
		`{"model":"deepseek/deepseek-v4-flash","stop":["END"],"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.True(t, chatIngress.RequiresChatCompletionsParams(nonReasoning),
		"chat ingress carries the same constraint under its own key")
}
