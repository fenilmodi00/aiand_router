package proxy_test

import (
	"io"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// responsesStreamUpstream writes a minimal Responses SSE stream: one text
// delta, one function call, then response.completed with usage.
func responsesStreamUpstream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"patching"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"main.go\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
}

func openAIChatService(provider providers.Client, model string) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: model, Reason: "test"}},
		map[string]providers.Client{providers.ProviderAIAND: provider},
		nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil,
	)
}

const chatToolTurnBody = `{"model":"auto","stream":true,"max_tokens":2048,
  "messages":[{"role":"user","content":"read main.go"}],
  "tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],
  "reasoning_effort":"medium"}`
