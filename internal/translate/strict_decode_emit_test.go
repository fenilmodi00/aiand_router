package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prevention layer for strongly-typed tool calls: upstreams that expose a
// decode-time constraint knob get it turned on, so out-of-schema tool calls
// stop being generated at the source instead of being repaired after the
// fact.
//   - OpenAI Responses (gpt-5.x): tools[].strict=true + strictified schema.
//   - Gemini 3.x: toolConfig.functionCallingConfig.mode=VALIDATED.

const anthropicToolsRequest = `{
  "model":"claude-opus-4-8","max_tokens":4096,
  "tools":[{"name":"Read","description":"read a file","input_schema":{
	"type":"object",
	"properties":{"file_path":{"type":"string"},"pages":{"type":"string"}},
	"required":["file_path"]
  }}],
  "messages":[{"role":"user","content":"read main.go"}]
}`

func TestPrepareOpenAIResponses_StrictTools(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(anthropicToolsRequest))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-5.5",
		Capabilities: router.Lookup("gpt-5.5"),
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	tools, _ := out["tools"].([]any)
	require.Len(t, tools, 1)
	tool0, _ := tools[0].(map[string]any)

	assert.Equal(t, true, tool0["strict"],
		"a strictifiable schema must opt into grammar-constrained decoding")
	params, _ := tool0["parameters"].(map[string]any)
	require.NotNil(t, params)
	assert.Equal(t, false, params["additionalProperties"])
	assert.ElementsMatch(t, []any{"file_path", "pages"}, params["required"].([]any),
		"strict mode requires every property in required")
	pages := params["properties"].(map[string]any)["pages"].(map[string]any)
	assert.Equal(t, []any{"string", "null"}, pages["type"],
		"the optional param is expressed as a null union, not omission")
}

func TestPrepareOpenAIResponses_NonStrictifiableFallsBack(t *testing.T) {
	// oneOf is outside the strict subset: the tool must emit its ORIGINAL
	// schema without strict rather than fail or mangle it.
	body := `{
	  "model":"claude-opus-4-8","max_tokens":4096,
	  "tools":[{"name":"Pick","input_schema":{
	    "type":"object",
	    "properties":{"choice":{"oneOf":[{"type":"string"},{"type":"integer"}]}},
	    "required":["choice"]
	  }}],
	  "messages":[{"role":"user","content":"pick"}]
	}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel:  "gpt-5.5",
		Capabilities: router.Lookup("gpt-5.5"),
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	tools, _ := out["tools"].([]any)
	require.Len(t, tools, 1)
	tool0, _ := tools[0].(map[string]any)

	assert.Equal(t, false, tool0["strict"])
	params, _ := tool0["parameters"].(map[string]any)
	require.NotNil(t, params)
	assert.NotContains(t, params, "additionalProperties",
		"the original schema is emitted untouched on fallback")
	choice := params["properties"].(map[string]any)["choice"].(map[string]any)
	assert.Contains(t, choice, "oneOf")
}
