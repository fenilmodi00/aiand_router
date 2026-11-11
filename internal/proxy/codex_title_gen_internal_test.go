package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexTitleRouter records the policy turn context each routed title request
// carried, so title turns can assert they scored as title_gen.
type codexTitleRouter struct {
	routeCalls int
	turnTypes  []string
}

func (r *codexTitleRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.routeCalls++
	if req.PolicyTurnContext != nil {
		r.turnTypes = append(r.turnTypes, req.PolicyTurnContext.TurnType)
	}
	return router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String(), Reason: "scored"}, nil
}

func TestCodexDesktopThreadTitleScoresWithoutMarker(t *testing.T) {
	routerSpy := &codexTitleRouter{}
	provider := &codexTitleProvider{}
	svc := NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderAIAND: provider},
		nil, false, nil, nil, false,
		providers.ProviderAIAND, catalog.ModelIDGLM53.String(), nil,
	)
	body := []byte(`{
		"model":"z-ai/glm-5.3",
		"stream":true,
		"tool_choice":"auto",
		"text":{"verbosity":"low","format":{"type":"json_schema","strict":true,"name":"codex_output_schema","schema":{
			"type":"object",
			"properties":{"title":{"type":"string","minLength":1,"maxLength":60},"description":{"type":"string","minLength":1}},
			"required":["title","description"],
			"additionalProperties":false
		}}},
		"client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"thread_title\",\"turn_trigger\":\"thread_title\",\"request_kind\":\"turn\"}"},
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","description":"","tools":[
				{"type":"function","name":"exec_command","parameters":{"type":"object"}}
			]}]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Codex."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"You are a helpful assistant. You will be presented with a user prompt, and your job is to provide a short title for a task that will be created from that prompt.\n\nUser prompt:\nfix the flaky login test"}]}
		]
	}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.endpoints, 1)
	require.Equal(t, 1, routerSpy.routeCalls, "Codex title generation must be scored independently")
	assert.Equal(t, []string{"title_gen"}, routerSpy.turnTypes)
	assert.NotContains(t, rec.Body.String(), "Aiand Router", "a routing marker corrupts the structured title JSON Codex Desktop parses")
}
