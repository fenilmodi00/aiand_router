package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
)

// codexNotifyTurnBody is a Codex code-mode history where an exec script
// called notify() three times, so its call_id carries four outputs.
const codexNotifyTurnBody = `{"model":"gpt-5.6-sol","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"poll the build"}]},` +
	`{"type":"custom_tool_call","call_id":"toolu_dup_1","name":"exec","input":"notify(status)"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":[{"type":"input_text","text":"Script completed\nOutput:\n"},{"type":"input_text","text":"build passed"}]},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: queued"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: running"},` +
	`{"type":"custom_tool_call_output","call_id":"toolu_dup_1","output":"status: passed"},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"what next?"}]}` +
	`],"tools":[{"type":"custom","name":"exec"}]}`

func codexClientCtx() context.Context {
	return context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
}

func TestProxyOpenAIResponses_CodexDuplicateToolOutputsStayVerbatimOnNativeOpenAI(t *testing.T) {
	openai := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-5.6-sol", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: openai,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(codexClientCtx(), []byte(codexNotifyTurnBody), rec, req))

	require.Len(t, openai.proxyBodies, 1)
	assert.Equal(t, providers.EndpointResponses, openai.proxyEndpoints[0])
	outputs := 0
	gjson.GetBytes(openai.proxyBodies[0], "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "custom_tool_call_output" && item.Get("call_id").String() == "toolu_dup_1" {
			outputs++
		}
		return true
	})
	assert.Equal(t, 4, outputs, "native Responses dispatch forwards the caller's own history")
}
