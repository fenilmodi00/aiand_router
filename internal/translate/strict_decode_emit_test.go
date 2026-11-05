package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

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
  "model":"z-ai/glm-5.3","max_tokens":4096,
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
		TargetModel:  "z-ai/glm-5.3",
		Capabilities: router.Lookup("z-ai/glm-5.3"),
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
	// oneOf is outside the strict subset: fallback preserves that construct
	// while closing object schemas that can be closed without changing meaning.
	body := `{
	  "model":"z-ai/glm-5.3","max_tokens":4096,
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
		TargetModel:  "z-ai/glm-5.3",
		Capabilities: router.Lookup("z-ai/glm-5.3"),
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
	assert.Equal(t, false, params["additionalProperties"],
		"the root object must be explicitly closed even when oneOf prevents strict mode")
	choice := params["properties"].(map[string]any)["choice"].(map[string]any)
	assert.Contains(t, choice, "oneOf")
}

func TestPrepareOpenAIResponses_FallbackClosesNestedObjectsSafely(t *testing.T) {
	body := `{
	  "model":"z-ai/glm-5.3","max_tokens":4096,
	  "tools":[{"name":"Execute","input_schema":{
	    "type":"object",
	    "properties":{
	      "steps":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}},
	      "empty":{"type":"object","properties":{}},
	      "freeform":{"type":"object","additionalProperties":{"type":"string"}},
	      "dynamic":{"type":"object","additionalProperties":{"type":"object","properties":{"value":{"type":"string"}}}},
	      "choice":{"oneOf":[{"type":"object","properties":{"mode":{"type":"string"}}}]}
	    },
	    "required":["steps"],
	    "allOf":[{"type":"object","properties":{"extra":{"type":"string"}},"required":["extra"]}]
	  }}],
	  "messages":[{"role":"user","content":"run"}]
	}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	prep, err := env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{
		TargetModel: "z-ai/glm-5.3", Capabilities: router.Lookup("z-ai/glm-5.3"),
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	tools, _ := out["tools"].([]any)
	require.Len(t, tools, 1)
	tool, _ := tools[0].(map[string]any)
	assert.Equal(t, false, tool["strict"])
	params, _ := tool["parameters"].(map[string]any)
	require.NotNil(t, params)
	assert.NotContains(t, params, "additionalProperties", "closing the root would forbid the allOf branch's required extra key")

	properties := params["properties"].(map[string]any)
	steps := properties["steps"].(map[string]any)
	items := steps["items"].(map[string]any)
	assert.Equal(t, false, items["additionalProperties"], "object array items are closed")
	empty := properties["empty"].(map[string]any)
	assert.Equal(t, false, empty["additionalProperties"], "explicit empty property objects are closed")
	freeform := properties["freeform"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "string"}, freeform["additionalProperties"], "freeform dictionaries keep their value schema")
	dynamic := properties["dynamic"].(map[string]any)
	dynamicValues := dynamic["additionalProperties"].(map[string]any)
	assert.Equal(t, false, dynamicValues["additionalProperties"], "object schemas under additionalProperties are closed")
	choice := properties["choice"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)
	assert.Equal(t, false, choice["additionalProperties"], "object union branches are closed")
	allOf := params["allOf"].([]any)[0].(map[string]any)
	assert.NotContains(t, allOf, "additionalProperties", "allOf branches stay open to sibling branch properties")
}
