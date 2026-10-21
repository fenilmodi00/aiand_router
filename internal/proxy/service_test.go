package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fakeRouter struct {
	decision    router.Decision
	err         error
	capturedReq *router.Request
	routeCalls  int
}

type fakePreviewRouter struct {
	previewResult policy.PreviewResult
	previewReq    *router.Request
	previewCalls  int
	routeCalls    int
}

func (f *fakePreviewRouter) Route(context.Context, router.Request) (router.Decision, error) {
	f.routeCalls++
	return router.Decision{}, errors.New("serving route must not run during preview")
}

func (f *fakePreviewRouter) PreviewRoute(_ context.Context, req router.Request) (policy.PreviewResult, error) {
	f.previewCalls++
	f.previewReq = &req
	return f.previewResult, nil
}

func (f *fakeRouter) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	f.capturedReq = &req
	f.routeCalls++
	return f.decision, f.err
}

type fakeProvider struct {
	proxyBodies    [][]byte
	proxyEndpoints []providers.Endpoint
	proxyHeaders   []http.Header
	proxyResponse  func(w http.ResponseWriter)
	proxyErr       error
	// proxyCreds records the resolved credential per dispatch; nil means
	// deployment-key fallback (no credential set).
	proxyCreds []*proxy.Credentials
	// passthroughCreds records the resolved credential per Passthrough call.
	passthroughCreds []*proxy.Credentials
	// passthroughResponse, when set, drives Passthrough's response and error.
	passthroughResponse func(ctx context.Context, w http.ResponseWriter) error
	// proxyErrByEndpoint overrides proxyErr for one upstream endpoint, modelling
	// an endpoint that serves chat/completions but no Responses API.
	proxyErrByEndpoint map[providers.Endpoint]error
}

func (*fakeProvider) SupportsSubscriptions() bool { return true }

func (f *fakeProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	saved := make([]byte, len(prep.Body))
	copy(saved, prep.Body)
	f.proxyBodies = append(f.proxyBodies, saved)
	f.proxyEndpoints = append(f.proxyEndpoints, prep.Endpoint)
	f.proxyHeaders = append(f.proxyHeaders, prep.Headers.Clone())
	f.proxyCreds = append(f.proxyCreds, proxy.CredentialsFromContext(ctx))
	if err, ok := f.proxyErrByEndpoint[prep.Endpoint]; ok {
		return err
	}
	if f.proxyResponse != nil {
		f.proxyResponse(w)
	}
	return f.proxyErr
}

func TestService_AgentShadowEvaluationNeverRetriesSubscriptionOnDeploymentKey(t *testing.T) {
	provider := &fakeProvider{proxyErr: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.2", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithAvailableModels(map[string]struct{}{"z-ai/glm-5.2": {}})

	ctx := context.WithValue(context.Background(), proxy.AgentShadowEvalContextKey{}, proxy.AgentShadowEvaluation{
		Model: "z-ai/glm-5.2", RolloutID: "pilot-1", StateID: "state-1",
	})
	ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, "1791da5d-d0db-494c-8574-859a4cb20d97")
	body := []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"inspect this repository"}],"max_tokens":512}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_ = svc.ProxyMessages(ctx, body, rec, req)

	require.Len(t, provider.proxyBodies, 1, "eval traffic must not retry on the paid deployment key")
	require.NotNil(t, provider.proxyCreds[0])
	assert.True(t, provider.proxyCreds[0].OAuth, "the single attempt must use the supplied subscription")
}

func TestService_ProxyOpenAIResponses_CodexFeedbackSkillIsSynthetic(t *testing.T) {
	provider := &fakeProvider{}
	fr := &fakeRouter{}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-terra", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":"$rf +"},{"type":"message","role":"user","content":"<skill>\n<name>rf</name>\nrun the feedback skill\n</skill>"},{"type":"custom_tool_call","call_id":"call_skill","name":"exec","input":"..."},{"type":"custom_tool_call_output","call_id":"call_skill","output":[{"type":"input_text","text":"Script completed\nOutput:\n /router-feedback +\n"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	assert.Empty(t, provider.proxyBodies, "a Codex feedback skill must not dispatch a model turn")
	assert.Zero(t, fr.routeCalls, "a Codex feedback skill must be answered before routing")
	assert.Contains(t, rec.Body.String(), "Feedback recorded 👍")
}

// A direct-OpenAI Responses caller dispatches on its original bytes rather
// than the chat projection, whatever the model or tool shape.
func TestService_ProxyOpenAIResponses_StaysNativeForDirectOpenAI(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		tools        string
		wantEndpoint providers.Endpoint
	}{
		{
			name:         "reasoning tool turn",
			model:        "gpt-5.6-luna",
			tools:        `,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]`,
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "toolless turn stays native too",
			model:        "gpt-5.6-luna",
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "non-reasoning tool turn stays native too",
			model:        "gpt-4.1",
			tools:        `,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]`,
			wantEndpoint: providers.EndpointResponses,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}}
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: tc.model, Reason: "test"}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
			body := []byte(`{"model":"auto","input":"remove the router","reasoning":{"effort":"medium"}` + tc.tools + `}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			assertContextHeaders(t, rec.Header())
			require.Len(t, provider.proxyBodies, 1)
			assert.Equal(t, tc.wantEndpoint, provider.proxyEndpoints[0])
			assert.Equal(t, tc.model, gjson.GetBytes(provider.proxyBodies[0], "model").Str)
			assert.Equal(t, "medium", gjson.GetBytes(provider.proxyBodies[0], "reasoning.effort").Str,
				"native dispatch keeps the caller's reasoning")
			assert.Equal(t, "remove the router", gjson.GetBytes(provider.proxyBodies[0], "input").Str)
		})
	}
}

// markerReasonBestPickForTest mirrors proxy's unexported markerReasonBestPick.
const markerReasonBestPickForTest = "best pick for this turn"

func TestService_ProxyOpenAIResponses_NativeBadgeForTerminalClientsHonorsSuppression(t *testing.T) {
	const native = "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.6-terra\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	const priorBadge = "\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-sol\n\nold answer\n\n_Weave Router feedback:_ /rf + good experience"

	for _, tc := range []struct {
		name       string
		clientApp  string
		marker     string
		suggestion bool
		wantBadge  bool
		wantStrip  bool
	}{
		{name: "Codex", clientApp: proxy.ClientAppCodex, wantBadge: true, wantStrip: true},
		{name: "Codex marker opt-out", clientApp: proxy.ClientAppCodex, marker: "off", wantStrip: true},
		{name: "Codex suggestion mode", clientApp: proxy.ClientAppCodex, suggestion: true, wantStrip: true},
		{name: "OpenCode", clientApp: proxy.ClientAppOpencode, wantBadge: true, wantStrip: true},
		{name: "OpenCode marker opt-out", clientApp: proxy.ClientAppOpencode, marker: "off", wantStrip: true},
		{name: "non-terminal Responses client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, native)
			}}
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAIAND,
				Model:    "gpt-5.6-terra",
				Reason:   "test",
			}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: tc.clientApp})
			body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"\u2063\u2060\u2063\u2060**Weave Router** — gpt-5.6-sol\n\nold answer\n\n_Weave Router feedback:_ /rf + good experience"}]},{"type":"message","role":"user","content":"continue"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]},{"type":"function_call","call_id":"call_shell","name":"shell","arguments":"{}"}]}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
			if tc.marker != "" {
				req.Header.Set("X-Weave-Routing-Marker", tc.marker)
			}
			if tc.suggestion {
				req.Header.Set("x-weave-suggestion-mode", "true")
			}

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			require.Len(t, provider.proxyBodies, 1)
			assert.Equal(t, "gpt-5.6-terra", gjson.GetBytes(provider.proxyBodies[0], "model").Str)
			upstreamHistory := gjson.GetBytes(provider.proxyBodies[0], "input.0.content.0.text").Str
			if tc.wantStrip {
				assert.Equal(t, "old answer", upstreamHistory)
				nativeInput := gjson.GetBytes(provider.proxyBodies[0], "input").Array()
				require.Len(t, nativeInput, 3)
				assert.Equal(t, "function_call", nativeInput[2].Get("type").Str)
				assert.Equal(t, "call_shell", nativeInput[2].Get("call_id").Str)
			} else {
				assert.Equal(t, priorBadge, upstreamHistory)
				nativeInput := gjson.GetBytes(provider.proxyBodies[0], "input").Array()
				require.Len(t, nativeInput, 4)
				assert.Equal(t, "message", nativeInput[2].Get("type").Str)
			}
			if tc.wantBadge {
				assert.Contains(t, rec.Body.String(), "✦ **Weave Router** → gpt-5.6-terra · "+markerReasonBestPickForTest)
				assert.NotEqual(t, native, rec.Body.String())
			} else {
				assert.Equal(t, native, rec.Body.String(), "suppressed and non-terminal clients retain byte identity")
			}
		})
	}
}

func TestService_ProxyOpenAIResponses_NativeMarkerOptOutPreservesContentType(t *testing.T) {
	terminal := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}` + "\n\n"
	openAI := &fakeProvider{proxyResponse: nativeResponsesStream(terminal)}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "gpt-5.6-sol",
		Reason:   "test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: openAI,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"reasoning","id":"rs_0","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	req.Header.Set("X-Weave-Routing-Marker", "off")

	require.NoError(t, svc.ProxyOpenAIResponses(codexNativeResponsesCtx(), body, rec, req))
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	assert.NotContains(t, rec.Body.String(), "✦ **Weave Router**")
}

// codexBadgeSentinelForTest mirrors translate's invisible provenance prefix.
const codexBadgeSentinelForTest = "\u2063\u2060\u2063\u2060"

// responsesTextDeltas collects response.output_text.delta payloads in order.
func responsesTextDeltas(t *testing.T, raw []byte) []string {
	t.Helper()
	var deltas []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if gjson.Get(payload, "type").Str != "response.output_text.delta" {
			continue
		}
		deltas = append(deltas, gjson.Get(payload, "delta").Str)
	}
	return deltas
}

func TestService_CodexRequestRoutesInfrastructureOpenAIModelWithoutOAuth(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "gpt-5.4-nano",
		Reason:   "hmm:test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	body := []byte(`{"model":"gpt-5.6-sol","input":"route this turn"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	require.NoError(t, svc.ProxyOpenAIResponses(context.Background(), body, rec, req))
	require.Len(t, provider.proxyBodies, 1)
	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])
	assert.Contains(t, string(provider.proxyBodies[0]), `"model":"gpt-5.4-nano"`)
	assert.Nil(t, provider.proxyCreds[0],
		"an HMM-selected infrastructure model must use the OpenAI deployment key, never caller OAuth")
}

func (f *fakeProvider) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	f.passthroughCreds = append(f.passthroughCreds, proxy.CredentialsFromContext(ctx))
	if f.passthroughResponse != nil {
		return f.passthroughResponse(ctx, w)
	}
	return nil
}

func makeProxyService(decision router.Decision, p map[string]providers.Client) *proxy.Service {
	return proxy.NewService(&fakeRouter{decision: decision}, p, nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithRetrySleep(noRetrySleep)
}

// TestService_PassthroughToNamedProvider_ResolvesBYOKCredential: passthrough
// must resolve credential precedence so a BYOK key wins over the deployment key.
func TestService_PassthroughToNamedProvider_ResolvesBYOKCredential(t *testing.T) {
	provider := &fakeProvider{}
	svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAIAND: provider})

	ctx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-byok"), BaseURL: "https://byok.example.com"},
	})
	httpReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	err := svc.PassthroughToNamedProvider(ctx, providers.ProviderAIAND, nil, rec, httpReq)

	require.NoError(t, err)
	require.Len(t, provider.passthroughCreds, 1)
	require.NotNil(t, provider.passthroughCreds[0], "BYOK credential must be resolved onto ctx before Passthrough dispatches")
	assert.Equal(t, []byte("sk-ant-byok"), provider.passthroughCreds[0].APIKey)
	assert.Equal(t, "https://byok.example.com", provider.passthroughCreds[0].BaseURL)
}

// TestService_PassthroughToProvider_CountTokensForwardsWithCredential verifies
// that a reachable Anthropic credential keeps count_tokens on the real upstream.
func TestService_PassthroughToProvider_CountTokensForwardsWithCredential(t *testing.T) {
	body := []byte(`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hello"}]}`)

	t.Run("deployment key", func(t *testing.T) {
		provider := &fakeProvider{}
		svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAIAND: provider}).
			WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
		rec := httptest.NewRecorder()

		require.NoError(t, svc.PassthroughToProvider(context.Background(), body, rec, httpReq))
		assert.Len(t, provider.passthroughCreds, 1, "deployment-keyed Anthropic must forward upstream")
	})

	t.Run("BYOK key", func(t *testing.T) {
		provider := &fakeProvider{}
		svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAIAND: provider}).
			WithDeploymentKeyedProviders(map[string]struct{}{})
		ctx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{Provider: providers.ProviderAIAND, Plaintext: []byte("sk-ant-byok")},
		})
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
		rec := httptest.NewRecorder()

		require.NoError(t, svc.PassthroughToProvider(ctx, body, rec, httpReq))
		assert.Len(t, provider.passthroughCreds, 1, "BYOK Anthropic must forward upstream")
	})
}

func boolPtr(b bool) *bool { return &b }

// OpenRouter speaks OpenAI Chat Completions natively, so an OpenAI-format
// inbound landing on an OpenRouter decision must take the no-translation path.
// Regression: eval harness v0.27 hit "no translation path defined".
func TestService_ProxyOpenAIChatCompletion_NativeOpenRouter(t *testing.T) {
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion"}`)
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAIAND, Model: "qwen/qwen3-coder", Reason: "test"},
		map[string]providers.Client{providers.ProviderAIAND: provider},
	)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	err := svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, httpReq)
	require.NoError(t, err)

	require.Len(t, provider.proxyBodies, 1)
	var got map[string]any
	require.NoError(t, json.Unmarshal(provider.proxyBodies[0], &got))
	assert.Equal(t, "qwen/qwen3-coder", got["model"], "envelope rewrites model to decision.Model")
	assert.Contains(t, rec.Body.String(), `"chat.completion"`)
}

// Bedrock, Makora, and Together are direct providers served by the
// openaicompat client and must route through the OpenAI-emission case, not
// the default "no translation path" branch. Regression: Makora/Together
// (DeepSeek-V4 primaries) were missing from the old literal dispatch list and
// 502'd in prod. Keying dispatch off the translation family fixes all of
// them.
func TestService_ProxyMessages_DispatchesBedrockMakoraTogether(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"bedrock", providers.ProviderAIAND, "moonshotai/kimi-k2.5"},
		{"makora", providers.ProviderAIAND, "deepseek/deepseek-v4-flash"},
		{"together", providers.ProviderAIAND, "deepseek/deepseek-v4-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
				},
			}
			svc := makeProxyService(
				router.Decision{Provider: tc.provider, Model: tc.model, Reason: "test"},
				map[string]providers.Client{tc.provider: p},
			)
			// Tools + opus keeps this out of the classifier-hard-pin path,
			// so the test exercises the widened switch, not the fallback.
			body := []byte(`{"model":"z-ai/glm-5.2","max_tokens":16,"tools":[{"name":"calc","description":"add","input_schema":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}}],"messages":[{"role":"user","content":"What is 7+5? Use calc."}]}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
			require.Len(t, p.proxyBodies, 1, "%s must reach the upstream", tc.provider)
		})
	}
}

func TestService_ProxyOpenAIChatCompletion_DispatchesBedrockMakoraTogether(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"bedrock", providers.ProviderAIAND, "qwen/qwen3-coder-next"},
		{"makora", providers.ProviderAIAND, "deepseek/deepseek-v4-flash"},
		{"together", providers.ProviderAIAND, "deepseek/deepseek-v4-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{
				proxyResponse: func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion"}`)
				},
			}
			svc := makeProxyService(
				router.Decision{Provider: tc.provider, Model: tc.model, Reason: "test"},
				map[string]providers.Client{tc.provider: p},
			)
			body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, req))
			require.Len(t, p.proxyBodies, 1, "%s must reach the upstream", tc.provider)
		})
	}
}

// The native Responses body is dispatched verbatim, so the arm's level has to
// be written onto it — otherwise an effort-qualified arm serves at whatever
// level the caller sent.
func TestService_ProxyOpenAIResponses_NativeDispatchAppliesArmEffort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		armEffort  string
		wantEffort string
	}{
		{name: "arm level within the target's menu", armEffort: "low", wantEffort: "low"},
		{name: "the top level reaches the wire unclamped", armEffort: "xhigh", wantEffort: "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}}
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAIAND,
				Model:    "gpt-5.6-luna",
				Effort:   tc.armEffort,
				Reason:   "test",
			}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
			body := []byte(`{"model":"auto","input":"remove the router","reasoning":{"effort":"medium","summary":"auto"}}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			require.Len(t, provider.proxyBodies, 1)
			assert.Equal(t, tc.wantEffort, gjson.GetBytes(provider.proxyBodies[0], "reasoning.effort").Str)
			assert.Equal(t, "auto", gjson.GetBytes(provider.proxyBodies[0], "reasoning.summary").Str,
				"unrelated native fields survive")
			assert.Equal(t, "remove the router", gjson.GetBytes(provider.proxyBodies[0], "input").Str)
		})
	}
}

func assertContextHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	require.Equal(t, "1", headers.Get(proxy.HeaderRouterContextVersion))
	require.Equal(t, "approximate", headers.Get(proxy.HeaderRouterContextEstimateKind))
	require.Equal(t, "8000", headers.Get(proxy.HeaderRouterContextReserve))
	require.NotEmpty(t, headers.Get(proxy.HeaderRouterContextEstimate))
	require.NotEqual(t, "0", headers.Get(proxy.HeaderRouterContextEstimate))
}
