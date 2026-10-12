package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

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

func TestService_PreviewAnthropicRouteBuildsServingCandidateContextWithoutDispatch(t *testing.T) {
	anthropicProvider := &fakeProvider{}
	openAIProvider := &fakeProvider{}
	previewer := &fakePreviewRouter{previewResult: policy.PreviewResult{
		SchemaVersion:     policy.SchemaVersionV1,
		EligibleRosterIDs: []string{"anthropic/claude-opus-4-8", "openai/gpt-5.5"},
	}}
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropicProvider,
		providers.ProviderAIAND:     openAIProvider,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: previewer}).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAnthropic: {},
			providers.ProviderAIAND:     {},
		})

	ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
	ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "org-1")
	ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, "1791da5d-d0db-494c-8574-859a4cb20d97")
	ctx = context.WithValue(ctx, proxy.InstallationExcludedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-flash"})
	ctx = context.WithValue(ctx, proxy.InstallationPreferredModelsContextKey{}, []string{"zai-org/glm-5.3"})
	body := []byte(`{"model":"zai-org/glm-5.3","messages":[{"role":"user","content":"inspect this repository"}],"max_tokens":4096,"tools":[{"name":"Read","description":"read a file","input_schema":{"type":"object"}}]}`)

	result, err := svc.PreviewAnthropicRoute(ctx, body, http.Header{})

	require.NoError(t, err)
	assert.Equal(t, previewer.previewResult, result)
	assert.Equal(t, 1, previewer.previewCalls)
	assert.Zero(t, previewer.routeCalls)
	require.NotNil(t, previewer.previewReq)
	assert.Equal(t, "zai-org/glm-5.3", previewer.previewReq.RequestedModel)
	assert.Equal(t, "org-1", previewer.previewReq.OrganizationID)
	assert.Equal(t, "1791da5d-d0db-494c-8574-859a4cb20d97", previewer.previewReq.InstallationID)
	assert.True(t, previewer.previewReq.HasTools)
	assert.Equal(t, []string{"Read"}, previewer.previewReq.AvailableTools)
	assert.Contains(t, previewer.previewReq.EnabledProviders, providers.ProviderAnthropic)
	assert.Contains(t, previewer.previewReq.EnabledProviders, providers.ProviderAIAND)
	assert.Contains(t, previewer.previewReq.ExcludedModels, "deepseek-ai/deepseek-v4-flash")
	assert.NotContains(t, previewer.previewReq.ExcludedModels, "motif-technologies/motif-3",
		"a normal non-Codex preview must retain infrastructure OpenAI candidates")
	assert.Equal(t, []string{"zai-org/glm-5.3"}, previewer.previewReq.PreferredModels)
	assert.False(t, previewer.previewReq.TrainingAllowed)
	assert.Empty(t, anthropicProvider.proxyBodies)
	assert.Empty(t, openAIProvider.proxyBodies)
}

func TestService_AgentShadowEvaluationForcesEphemerallyWithoutServingRouter(t *testing.T) {
	telemetry := newCaptureTelemetry()
	pins := newFakePinStore()
	pins.hasPin = true
	pins.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
	}}
	servingRouter := &fakeRouter{err: errors.New("serving router must not run")}
	svc := proxy.NewService(servingRouter, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, pins, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", telemetry).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).
		WithAvailableModels(map[string]struct{}{"zai-org/glm-5.3": {}})

	ctx := context.WithValue(context.Background(), proxy.AgentShadowEvalContextKey{}, proxy.AgentShadowEvaluation{
		Model: "zai-org/glm-5.3", RolloutID: "pilot-1", StateID: "state-1",
	})
	ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, "1791da5d-d0db-494c-8574-859a4cb20d97")
	messages := make([]map[string]any, 0, 17)
	for i := range 8 {
		toolID := fmt.Sprintf("tool_%d", i)
		assistantContent := []map[string]any{
			{"type": "tool_use", "id": toolID, "name": "Read", "input": map[string]any{"file_path": "README.md"}},
		}
		if i == 0 {
			assistantContent = append([]map[string]any{
				{"type": "thinking", "thinking": "historical thought", "signature": "stale-signature"},
			}, assistantContent...)
		}
		messages = append(messages,
			map[string]any{"role": "assistant", "content": assistantContent},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": toolID, "content": fmt.Sprintf("old-result-%d", i)}}},
		)
	}
	messages = append(messages, map[string]any{"role": "user", "content": "make the next edit"})
	body, err := json.Marshal(map[string]any{
		"model": "zai-org/glm-5.3-flash", "messages": messages, "max_tokens": 512,
		"thinking": map[string]any{"type": "adaptive"},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))
	assert.Zero(t, servingRouter.routeCalls)
	require.Len(t, provider.proxyBodies, 1)
	assert.Contains(t, string(provider.proxyBodies[0]), `"model":"zai-org/glm-5.3"`)
	assert.Contains(t, string(provider.proxyBodies[0]), "old-result-0")
	assert.NotContains(t, string(provider.proxyBodies[0]), "stale-signature")
	assert.Equal(t, "zai-org/glm-5.3", rec.Header().Get(proxy.HeaderRouterModel))
	// zai-org/glm-5.3 is a 1M-window roster row, so the served context window
	// header reports the effective catalog window clients should budget against.
	assert.Equal(t, "1048576", rec.Header().Get(proxy.HeaderRouterContextWindow))
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.Equal(t, proxy.ReasonAgentShadowEval, rec.Header().Get(proxy.HeaderRouterDecision))
	assert.Never(t, func() bool {
		telemetry.mu.Lock()
		defer telemetry.mu.Unlock()
		return len(telemetry.rows) != 0 || len(telemetry.shadowRows) != 0
	}, 100*time.Millisecond, 5*time.Millisecond, "eval traffic must not create serving or policy telemetry")
	pins.mu.Lock()
	defer pins.mu.Unlock()
	assert.Zero(t, pins.getCalls, "eval traffic must not read production session pins")
	assert.Empty(t, pins.upserts, "eval traffic must not write production session pins")
	assert.Empty(t, pins.usages, "eval traffic must not update production session usage")
	assert.Zero(t, pins.incrementCalls)
	assert.Zero(t, pins.resetCalls)
}

func TestService_AgentShadowEvaluationNeverSubstitutesRequestedBaseline(t *testing.T) {
	provider := &fakeProvider{proxyErr: &providers.UpstreamStatusError{Status: http.StatusServiceUnavailable}}
	svc := proxy.NewService(&fakeRouter{}, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "zai-org/glm-5.3-flash", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{
			providers.ProviderAIAND: {},
		}).
		WithAvailableModels(map[string]struct{}{
			"zai-org/glm-5.3":               {},
			"deepseek-ai/deepseek-v4-flash": {},
		})

	ctx := context.WithValue(context.Background(), proxy.AgentShadowEvalContextKey{}, proxy.AgentShadowEvaluation{
		Model: "deepseek-ai/deepseek-v4-flash", RolloutID: "pilot-1", StateID: "state-1",
	})
	body := []byte(`{"model":"zai-org/glm-5.3","messages":[{"role":"user","content":"inspect this repository"}],"max_tokens":512}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_ = svc.ProxyMessages(ctx, body, rec, req)

	require.Len(t, provider.proxyBodies, 1, "eval forcing must attempt the planned candidate exactly once")
	assert.Contains(t, string(provider.proxyBodies[0]), `"model":"deepseek-ai/deepseek-v4-flash"`,
		"eval forcing must never dispatch the request baseline")
	assert.Equal(t, "deepseek-ai/deepseek-v4-flash", rec.Header().Get(proxy.HeaderRouterModel))
	// deepseek-v4-flash is a 1M-window roster row, so the header carries its catalog window.
	assert.Equal(t, "1048576", rec.Header().Get(proxy.HeaderRouterContextWindow))
}

func TestService_ProxyOpenAIResponses_CustomToolUsesNativeOpenAIFamily(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek-ai/deepseek-v4-flash", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderAIAND:     provider,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil)

	body := []byte(`{"model":"deepseek-ai/deepseek-v4-flash","input":"apply a patch","reasoning":{"effort":"high"},"tools":[{"type":"custom","name":"apply_patch"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIResponses(context.Background(), body, rec, req))

	require.NotNil(t, fr.capturedReq)
	originalEnvelope, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	assert.Equal(t, originalEnvelope.ReasoningConfigurationSHA256(), fr.capturedReq.ReasoningConfigurationSHA256)
	assert.Equal(t, originalEnvelope.ToolConfigurationSHA256(), fr.capturedReq.ToolConfigurationSHA256)
	assert.Equal(t, map[string]struct{}{providers.ProviderAIAND: {}}, fr.capturedReq.EnabledProviders)
	require.Len(t, provider.proxyBodies, 1)
	assert.JSONEq(t, `{"model":"deepseek-ai/deepseek-v4-flash","input":"apply a patch","reasoning":{"effort":"high"},"tools":[{"type":"custom","name":"apply_patch"}]}`, string(provider.proxyBodies[0]))
	assert.Equal(t, providers.EndpointResponses, provider.proxyEndpoints[0])
	assert.JSONEq(t, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`, rec.Body.String())
}

func TestService_ProxyOpenAIResponses_CodexFeedbackSkillIsSynthetic(t *testing.T) {
	provider := &fakeProvider{}
	fr := &fakeRouter{}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "qwen/qwen3.8-27b", nil)
	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"qwen/qwen3.8-27b","input":[{"type":"message","role":"user","content":"$rf +"},{"type":"message","role":"user","content":"<skill>\n<name>rf</name>\nrun the feedback skill\n</skill>"},{"type":"custom_tool_call","call_id":"call_skill","name":"exec","input":"..."},{"type":"custom_tool_call_output","call_id":"call_skill","output":[{"type":"input_text","text":"Script completed\nOutput:\n /router-feedback +\n"}]}]}`)
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
			model:        "zai-org/glm-5.3-flash",
			tools:        `,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]`,
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "toolless turn stays native too",
			model:        "zai-org/glm-5.3-flash",
			wantEndpoint: providers.EndpointResponses,
		},
		{
			name:         "non-reasoning tool turn stays native too",
			model:        "qwen/qwen3.8-27b",
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
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
			body := []byte(`{"model":"auto","input":"remove the router","reasoning":{"effort":"medium"}` + tc.tools + `}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			require.Len(t, provider.proxyBodies, 1)
			assert.Equal(t, tc.wantEndpoint, provider.proxyEndpoints[0])
			assert.Equal(t, tc.model, gjson.GetBytes(provider.proxyBodies[0], "model").Str)
			assert.Equal(t, "medium", gjson.GetBytes(provider.proxyBodies[0], "reasoning.effort").Str,
				"native dispatch keeps the caller's reasoning")
			assert.Equal(t, "remove the router", gjson.GetBytes(provider.proxyBodies[0], "input").Str)
		})
	}
}

// Killing the broad rollout per org must not take the incident fix: the
// reasoning+tools turn stays promoted either way.
func TestService_ProxyOpenAIResponses_BroadRolloutOffKeepsNarrowPromotion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tools        string
		wantEndpoint providers.Endpoint
	}{
		{
			name:         "reasoning tool turn is still promoted",
			tools:        `,"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]`,
			wantEndpoint: providers.EndpointResponses,
		},
		{name: "toolless turn keeps the chat projection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}}
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "zai-org/glm-5.3-flash", Reason: "test"}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
			ctx = flags.WithOverrides(ctx, flags.Overrides{
				Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
			})
			body := []byte(`{"model":"auto","input":"remove the router","reasoning":{"effort":"medium"}` + tc.tools + `}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			require.Len(t, provider.proxyBodies, 1)
			assert.Equal(t, tc.wantEndpoint, provider.proxyEndpoints[0])
		})
	}
}

// An OpenAI-compatible endpoint can serve chat/completions but no Responses
// API; the promoted turn falls back there, and the result is memoized.
func TestService_ProxyOpenAIResponses_ToolTurnFallsBackWhenEndpointLacksResponses(t *testing.T) {
	provider := &fakeProvider{
		proxyErrByEndpoint: map[providers.Endpoint]error{
			providers.EndpointResponses: &providers.UpstreamErrorResponse{
				Status: http.StatusNotFound,
				Body:   []byte(`{"error":{"message":"Unknown path /v1/responses"}}`),
			},
		},
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		},
	}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "zai-org/glm-5.3-flash",
		Reason:   "test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

	ctx := context.WithValue(
		context.Background(),
		proxy.ClientIdentityContextKey{},
		proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode},
	)
	body := []byte(`{"model":"auto","input":"remove the router","reasoning":{"effort":"medium"},"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`)

	for _, want := range [][]providers.Endpoint{
		{providers.EndpointResponses, providers.EndpointChatCompletions},
		{providers.EndpointChatCompletions},
	} {
		provider.proxyEndpoints = nil
		provider.proxyBodies = nil
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

		require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
		assert.Equal(t, want, provider.proxyEndpoints)
		last := provider.proxyBodies[len(provider.proxyBodies)-1]
		assert.False(t, gjson.GetBytes(last, "input").Exists())
		// The gpt-5.6 effort-none override went with its catalog rows; a roster
		// model's chat fallback carries no reasoning_effort at all.
		assert.False(t, gjson.GetBytes(last, "reasoning_effort").Exists())
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

// markerReasonBestPickForTest mirrors proxy's unexported markerReasonBestPick.
const markerReasonBestPickForTest = "best pick for this turn"

func TestService_ProxyOpenAIResponses_OpenCodeStripsTerminalArtifactsBeforeTranslation(t *testing.T) {
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.1", Reason: "test"}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: provider,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode})
	// Rollout off: this test pins the chat/completions body the translation
	// emits, not the promoted native-Responses dispatch.
	ctx = flags.WithOverrides(ctx, flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
	})
	body := []byte(`{"model":"auto","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"⁣⁠⁣⁠**Weave Router** — gpt-5.6-sol\n\nold answer\n\n_Weave Router feedback:_ /rf + good experience"}]},{"type":"message","role":"user","content":"continue"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	require.Len(t, provider.proxyBodies, 1)
	assert.Equal(t, "old answer", gjson.GetBytes(provider.proxyBodies[0], "messages.0.content").Str)
}

func TestService_ProxyOpenAIResponses_TranslatedMarkerOptOutPreservesStream(t *testing.T) {
	const upstream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	together := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstream)
	}}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.1",
		Reason:   "test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: together,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

	ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	// Rollout off keeps this turn on the chat/completions surface it speaks; the
	// broad promotion would stream it as native Responses and the prelude
	// lifecycle is decided by the marker, which this test suppresses.
	ctx = flags.WithOverrides(ctx, flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
	})
	body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"say hello"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	req.Header.Set("X-Weave-Routing-Marker", "off")

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, []string{"hello"}, responsesTextDeltas(t, rec.Body.Bytes()))
	assert.NotContains(t, rec.Body.String(), "Weave Router")
}

func TestService_ProxyOpenAIResponses_NativeMarkerOptOutPreservesContentType(t *testing.T) {
	terminal := "event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","model":"moonshotai/kimi-k3","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":120,"output_tokens":8}}}` + "\n\n"
	openAI := &fakeProvider{proxyResponse: nativeResponsesStream(terminal)}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "moonshotai/kimi-k3",
		Reason:   "test",
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderAIAND: openAI,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

	body := []byte(`{"model":"moonshotai/kimi-k3","stream":true,"input":[{"type":"reasoning","id":"rs_0","encrypted_content":"opaque"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	req.Header.Set("X-Weave-Routing-Marker", "off")

	require.NoError(t, svc.ProxyOpenAIResponses(codexNativeResponsesCtx(), body, rec, req))
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	assert.NotContains(t, rec.Body.String(), "✦ **Weave Router**")
}

// A first Codex turn must show the marker even when the action is tool-call-only;
// both cases were previously invisible (debug-gated marker; badge could only
// ride text deltas).
func TestService_ProxyOpenAIResponses_EmitsRoutingMarkerForCodex(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		wantText string
	}{
		{
			name: "text turn",
			upstream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
			wantText: "hello",
		},
		{
			name: "tool-call-only turn",
			upstream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{}
			provider.proxyResponse = func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				// AIand promotes the turn to POST /v1/responses, so the fixture
				// must answer the promoted endpoint in Responses frames.
				if n := len(provider.proxyEndpoints); n > 0 && provider.proxyEndpoints[n-1] == providers.EndpointResponses {
					// The proxy emits the badge itself (passthrough badge); the
					// fixture contributes only the model's own output.
					if tc.wantText == "" {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"\"}}\n\n"+
							"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"fc_1\",\"delta\":\"{}\"}\n\n"+
							"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"fc_1\",\"arguments\":\"{}\"}\n\n"+
							"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"{}\"}}\n\n"+
							"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"shell\",\"arguments\":\"{}\"}]}}\n\n")
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n"+
						"data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_1\",\"part\":{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}}\n\n"+
						"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_1\",\"delta\":\""+tc.wantText+"\"}\n\n"+
						"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\""+tc.wantText+"\"}]}}\n\n"+
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\""+tc.wantText+"\"}]}]}}\n\n")
					return
				}
				_, _ = io.WriteString(w, tc.upstream)
			}
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAIAND,
				Model:    "deepseek-ai/deepseek-v4-pro",
				Reason:   "test",
			}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
			body := []byte(`{"model":"zai-org/glm-5.3-flash","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"do the thing"}]}]}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))

			marker := "✦ **Weave Router** → deepseek-ai/deepseek-v4-pro · " + markerReasonBestPickForTest
			deltas := responsesTextDeltas(t, rec.Body.Bytes())
			require.NotEmpty(t, deltas)
			assert.Equal(t, codexBadgeSentinelForTest+marker+"\n\n", deltas[0])
			if tc.wantText != "" {
				assert.Equal(t, tc.wantText, strings.Join(deltas[1:], ""))
			} else {
				assert.Empty(t, deltas[1:], "a tool-call-only turn carries no model text")
				assert.Contains(t, rec.Body.String(), "response.function_call_arguments.done")
			}
		})
	}
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

func (f *fakeProvider) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	f.passthroughCreds = append(f.passthroughCreds, proxy.CredentialsFromContext(ctx))
	if f.passthroughResponse != nil {
		return f.passthroughResponse(ctx, w)
	}
	return nil
}

func makeProxyService(decision router.Decision, p map[string]providers.Client) *proxy.Service {
	return proxy.NewService(&fakeRouter{decision: decision}, p, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
		WithRetrySleep(noRetrySleep)
}

// TestService_PassthroughToNamedProvider_ResolvesBYOKCredential: passthrough
// must resolve credential precedence so a BYOK key wins over the deployment key.
func TestService_PassthroughToNamedProvider_ResolvesBYOKCredential(t *testing.T) {
	provider := &fakeProvider{}
	svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAnthropic: provider})

	ctx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-byok"), BaseURL: "https://byok.example.com"},
	})
	httpReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	err := svc.PassthroughToNamedProvider(ctx, providers.ProviderAnthropic, nil, rec, httpReq)

	require.NoError(t, err)
	require.Len(t, provider.passthroughCreds, 1)
	require.NotNil(t, provider.passthroughCreds[0], "BYOK credential must be resolved onto ctx before Passthrough dispatches")
	assert.Equal(t, []byte("sk-ant-byok"), provider.passthroughCreds[0].APIKey)
	assert.Equal(t, "https://byok.example.com", provider.passthroughCreds[0].BaseURL)
}

// TestService_PassthroughToProvider_CountTokensLocalFallback verifies that a
// deployment with no reachable Anthropic credential answers count_tokens locally.
func TestService_PassthroughToProvider_CountTokensLocalFallback(t *testing.T) {
	anthropicProvider := &fakeProvider{}
	openAIProvider := &fakeProvider{}
	svc := makeProxyService(router.Decision{}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropicProvider,
		providers.ProviderAIAND:     openAIProvider,
	}).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})

	body := []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash","messages":[{"role":"user","content":"hello world"}]}`)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
	rec := httptest.NewRecorder()

	require.NoError(t, svc.PassthroughToProvider(context.Background(), body, rec, httpReq))
	assert.Empty(t, anthropicProvider.passthroughCreds, "no upstream dispatch when no Anthropic credential is reachable")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("content-type"))
	tokens := gjson.GetBytes(rec.Body.Bytes(), "input_tokens")
	require.True(t, tokens.Exists())
	assert.Positive(t, tokens.Int())
}

// TestService_PassthroughToProvider_CountTokensForwardsWithCredential verifies
// that a reachable Anthropic credential keeps count_tokens on the real upstream.
func TestService_PassthroughToProvider_CountTokensForwardsWithCredential(t *testing.T) {
	body := []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash","messages":[{"role":"user","content":"hello"}]}`)

	t.Run("deployment key", func(t *testing.T) {
		provider := &fakeProvider{}
		svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAnthropic: provider}).
			WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
		rec := httptest.NewRecorder()

		require.NoError(t, svc.PassthroughToProvider(context.Background(), body, rec, httpReq))
		assert.Len(t, provider.passthroughCreds, 1, "deployment-keyed Anthropic must forward upstream")
	})

	t.Run("BYOK key", func(t *testing.T) {
		provider := &fakeProvider{}
		svc := makeProxyService(router.Decision{}, map[string]providers.Client{providers.ProviderAnthropic: provider}).
			WithDeploymentKeyedProviders(map[string]struct{}{})
		ctx := context.WithValue(context.Background(), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-byok")},
		})
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(""))
		rec := httptest.NewRecorder()

		require.NoError(t, svc.PassthroughToProvider(ctx, body, rec, httpReq))
		assert.Len(t, provider.passthroughCreds, 1, "BYOK Anthropic must forward upstream")
	})
}

func TestService_ProxyMessages_PropagatesUpstreamStatusError(t *testing.T) {
	upstreamErr := &providers.UpstreamStatusError{Status: 400}
	provider := &fakeProvider{proxyErr: upstreamErr}
	svc := makeProxyService(
		router.Decision{Provider: "anthropic", Model: "zai-org/glm-5.3-flash"},
		map[string]providers.Client{"anthropic": provider},
	)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(context.Background(), body, rec, httpReq)

	var got *providers.UpstreamStatusError
	require.ErrorAs(t, err, &got, "must surface the typed UpstreamStatusError")
	assert.Equal(t, 400, got.Status)
}

// TestService_ProxyMessages_CrossFormatUpstreamErrorBodyReachesClient guards a
// regression: a cross-format upstream non-2xx (e.g. an OpenAI-compat provider 402) buffered
// the body inside AnthropicSSETranslator but never flushed it, because
// Finalize was skipped on any non-nil proxyErr. Both the translated body and
// the typed UpstreamStatusError must reach the client/handler.
func TestService_ProxyMessages_CrossFormatUpstreamErrorBodyReachesClient(t *testing.T) {
	const upstreamBody = `{"error":{"message":"OpenRouter: insufficient credits","code":402,"type":"invalid_request_error"}}`
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, upstreamBody)
		},
		proxyErr: &providers.UpstreamStatusError{Status: http.StatusPaymentRequired},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-chat"},
		map[string]providers.Client{providers.ProviderAIAND: provider},
	)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(context.Background(), body, rec, httpReq)

	var got *providers.UpstreamStatusError
	require.ErrorAs(t, err, &got, "UpstreamStatusError must still propagate for telemetry")
	assert.Equal(t, http.StatusPaymentRequired, got.Status)

	assert.Equal(t, http.StatusPaymentRequired, rec.Code, "upstream status must reach the client")
	respBody := rec.Body.String()
	require.NotEmpty(t, respBody, "translated upstream error body must reach the client")
	assert.Contains(t, respBody, "insufficient credits", "upstream error message must survive translation")
	assert.Contains(t, respBody, `"type":"error"`, "body must be in Anthropic error envelope shape")
}

// TestService_ProxyMessages_StripsRoutingMarkerFromInboundHistory guards
// service.go's hygiene fix: the routing-marker text injected on prior
// cross-format responses (✦ **Weave Router** → ...) must not survive into the
// upstream body, or it round-trips and pollutes context on every later turn.
func TestService_ProxyMessages_StripsRoutingMarkerFromInboundHistory(t *testing.T) {
	const markerSentinel = "Weave Router"
	body := []byte(`{
		"model":"deepseek-ai/deepseek-v4-pro",
		"messages":[
			{"role":"user","content":"first prompt"},
			{"role":"assistant","content":[
				{"type":"text","text":"✦ **Weave Router** → deepseek/deepseek-v4-pro (openrouter) · reason: top scorer\n\n"},
				{"type":"text","text":"real assistant reply"}
			]},
			{"role":"user","content":[
				{"type":"text","text":"</summary>\n<result>✦ **Weave Router** → claude-haiku-4-5 (anthropic) · reason: tool-result follow-up\n\n</result>"}
			]}
		]
	}`)

	provider := &fakeProvider{}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"},
		map[string]providers.Client{providers.ProviderAnthropic: provider},
	)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

	require.Len(t, provider.proxyBodies, 1)
	upstream := string(provider.proxyBodies[0])
	assert.NotContains(t, upstream, markerSentinel, "routing marker must not reach upstream")
	assert.Contains(t, upstream, "real assistant reply", "non-marker assistant content must survive")

	// The last user message's content is promoted to an array with a cache_control
	// marker; unmarshal to verify the wrapper text survived.
	var upstreamJSON map[string]any
	require.NoError(t, json.Unmarshal(provider.proxyBodies[0], &upstreamJSON))
	msgs, _ := upstreamJSON["messages"].([]any)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := lastMsg["content"].([]any)
	lastBlock, _ := blocks[len(blocks)-1].(map[string]any)
	assert.Contains(t, lastBlock["text"], "</result>", "wrapper text around an embedded marker must survive")
}

func TestService_ProxyMessages_EmbedOnlyUserMessageFlag(t *testing.T) {
	const firstUserPrompt = "Walk every Go file under router/internal/ and produce a one-paragraph summary of each."
	const secondUserPrompt = "Now narrow it to handlers under internal/api/."
	// embedOnlyUserMessage must keep both user prompts, drop system text,
	// assistant tool_use, and tool_result blocks.
	body := []byte(`{
		"model":"deepseek-ai/deepseek-v4-pro",
		"system":"You are Claude Code. CLAUDE.md says: do not use emojis...",
		"messages":[
			{"role":"user","content":"` + firstUserPrompt + `"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"go.mod"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"module weave-os/router\n\ngo 1.23\n\nrequire (\n\tgithub.com/gin-gonic/gin v1.10.0\n)"}]},
			{"role":"user","content":"` + secondUserPrompt + `"}
		]
	}`)

	t.Run("flag off uses concatenated stream", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr,
			map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}},
			nil,
			false,
			nil,
			nil,
			false,
			providers.ProviderAnthropic, "zai-org/glm-5.3-flash",
			nil,
		)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		got := fr.capturedReq.PromptText
		assert.Contains(t, got, "You are Claude Code", "flag=off keeps system prompt")
		assert.Contains(t, got, firstUserPrompt, "flag=off keeps first user message text")
	})

	t.Run("flag on concatenates user-role text and drops everything else", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr,
			map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}},
			nil,
			true,
			nil,
			nil,
			false,
			providers.ProviderAnthropic, "zai-org/glm-5.3-flash",
			nil,
		)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		got := fr.capturedReq.PromptText
		assert.Equal(t, firstUserPrompt+"\n"+secondUserPrompt, got,
			"flag=on emits user-role text only (no system, no assistant tool_use, no tool_result)")
	})
}

func TestService_ProxyMessages_EmbedOnlyUserMessageContextOverride(t *testing.T) {
	const userPrompt = "Find the race condition in main.go"
	body := []byte(`{
		"model":"deepseek-ai/deepseek-v4-pro",
		"system":"You are Claude Code preamble...",
		"messages":[{"role":"user","content":"` + userPrompt + `"}]
	}`)

	cases := []struct {
		name           string
		startupFlag    bool
		ctxOverride    *bool
		wantPromptText string
	}{
		{
			name:           "ctx=true overrides startup=false",
			startupFlag:    false,
			ctxOverride:    boolPtr(true),
			wantPromptText: userPrompt,
		},
		{
			name:           "ctx=false overrides startup=true",
			startupFlag:    true,
			ctxOverride:    boolPtr(false),
			wantPromptText: "You are Claude Code preamble...\n" + userPrompt,
		},
		{
			name:           "no ctx override falls back to startup=true",
			startupFlag:    true,
			ctxOverride:    nil,
			wantPromptText: userPrompt,
		},
		{
			name:           "no ctx override falls back to startup=false",
			startupFlag:    false,
			ctxOverride:    nil,
			wantPromptText: "You are Claude Code preamble...\n" + userPrompt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "zai-org/glm-5.3-flash"}}
			svc := proxy.NewService(fr,
				map[string]providers.Client{"anthropic": &fakeProvider{}},
				nil,
				tc.startupFlag,
				nil,
				nil,
				false,
				"anthropic", "zai-org/glm-5.3-flash",
				nil,
			)

			ctx := context.Background()
			if tc.ctxOverride != nil {
				ctx = context.WithValue(ctx, proxy.EmbedOnlyUserMessageContextKey{}, *tc.ctxOverride)
			}

			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, svc.ProxyMessages(ctx, body, rec, httpReq))

			require.NotNil(t, fr.capturedReq)
			assert.Equal(t, tc.wantPromptText, fr.capturedReq.PromptText,
				"context override (%v) must beat startup flag (%v)", tc.ctxOverride, tc.startupFlag)
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestService_ProxyMessages_NoPinStoreRunsScorerEveryTurn verifies that
// without a pin store, every turn re-runs the cluster scorer.
func TestService_ProxyMessages_NoPinStoreRunsScorerEveryTurn(t *testing.T) {
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "zai-org/glm-5.3-flash"}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}},
		nil,
		false,
		nil,
		nil, // pinStore disabled
		false,
		providers.ProviderAnthropic, "zai-org/glm-5.3-flash",
		nil,
	)

	ctx := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "key-1")
	for range 2 {
		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httpReq))
	}
	assert.Equal(t, 2, fr.routeCalls, "without a pin store, both turns must consult the scorer")
}

func TestService_ProxyOpenAIChatCompletion_AnthropicCrossFormat(t *testing.T) {
	anthropicResp := `{"id":"msg_abc","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"deepseek-ai/deepseek-v4-pro","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`

	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(anthropicResp))
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: "anthropic", Model: "deepseek-ai/deepseek-v4-pro", Reason: "test"},
		map[string]providers.Client{"anthropic": provider},
	)

	openAIReq := `{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(openAIReq))

	err := svc.ProxyOpenAIChatCompletion(context.Background(), []byte(openAIReq), rec, httpReq)
	require.NoError(t, err)

	require.Len(t, provider.proxyBodies, 1)
	var translated map[string]any
	require.NoError(t, json.Unmarshal(provider.proxyBodies[0], &translated))
	assert.Equal(t, float64(100), translated["max_tokens"], "max_tokens preserved on translated body")
	msgs, _ := translated["messages"].([]any)
	require.Len(t, msgs, 1)

	var openAIOut map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &openAIOut))
	assert.Equal(t, "chat.completion", openAIOut["object"])
	choices, _ := openAIOut["choices"].([]any)
	require.Len(t, choices, 1)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	assert.Equal(t, "Hello!", message["content"])
	assert.Equal(t, "stop", choice["finish_reason"])
}

func TestService_ProxyOpenAIChatCompletion_AnthropicProxyError_PropagatesError(t *testing.T) {
	upstreamErr := errors.New("dial tcp: connection refused")
	provider := &fakeProvider{
		proxyErr: upstreamErr,
	}
	svc := makeProxyService(
		router.Decision{Provider: "anthropic", Model: "deepseek-ai/deepseek-v4-pro", Reason: "test"},
		map[string]providers.Client{"anthropic": provider},
	)

	body := `{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	err := svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, httpReq)

	require.ErrorIs(t, err, upstreamErr, "upstream Proxy error must propagate")
	assert.NotContains(t, rec.Body.String(), "translation failed",
		"Proxy error must not be masked by Finalize's translation failure body")
}

func TestService_ProxyOpenAIChatCompletion_NativeOpenAI(t *testing.T) {
	provider := &fakeProvider{
		proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion"}`)
		},
	}
	svc := makeProxyService(
		router.Decision{Provider: providers.ProviderAIAND, Model: "qwen/qwen3.8-27b", Reason: "test"},
		map[string]providers.Client{providers.ProviderAIAND: provider},
	)

	body := `{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	// Broad rollout off: this asserts the untranslated same-format path, which
	// stays reachable as the kill switch's target.
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
	})
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httpReq)
	require.NoError(t, err)

	require.Len(t, provider.proxyBodies, 1)
	assert.Equal(t, providers.EndpointChatCompletions, provider.proxyEndpoints[0])
	var got map[string]any
	require.NoError(t, json.Unmarshal(provider.proxyBodies[0], &got))
	assert.Equal(t, "qwen/qwen3.8-27b", got["model"], "envelope rewrites model to decision.Model")
	msgs, _ := got["messages"].([]any)
	require.Len(t, msgs, 1)
	assert.Contains(t, rec.Body.String(), `"chat.completion"`)
}

// The AIand OpenAI-compatible surface speaks OpenAI Chat Completions natively,
// so an OpenAI-format inbound landing on an OpenAI-compat decision must take
// the no-translation path.
// Regression: eval harness v0.27 hit "no translation path defined".
func TestService_ProxyOpenAIChatCompletion_NativeOpenAICompatProvider(t *testing.T) {
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

	body := `{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))

	// Rollout off: this test pins the no-translation chat/completions path, not
	// the Responses promotion an AIand turn takes by default.
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyOpenAIResponsesBroad: false},
	})
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, httpReq)
	require.NoError(t, err)

	require.Len(t, provider.proxyBodies, 1)
	var got map[string]any
	require.NoError(t, json.Unmarshal(provider.proxyBodies[0], &got))
	assert.Equal(t, "qwen/qwen3-coder", got["model"], "envelope rewrites model to decision.Model")
	assert.Contains(t, rec.Body.String(), `"chat.completion"`)
}

// OpenAI and AIand are direct providers served by the openaicompat client and
// must route through the OpenAI-emission case, not the default "no translation
// path" branch. Regression: the vendor DeepSeek-V4 primaries were missing from
// the old literal dispatch list and 502'd in prod. Keying dispatch off the
// translation family fixes all of them.
func TestService_ProxyMessages_DispatchesOpenAICompatProviders(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"aiand", providers.ProviderAIAND, "deepseek/deepseek-v4-flash"},
		{"openai", providers.ProviderAIAND, "moonshotai/kimi-k3"},
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
			body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","max_tokens":16,"tools":[{"name":"calc","description":"add","input_schema":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}}],"messages":[{"role":"user","content":"What is 7+5? Use calc."}]}`)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))
			require.Len(t, p.proxyBodies, 1, "%s must reach the upstream", tc.provider)
		})
	}
}

func TestService_ProxyOpenAIChatCompletion_DispatchesOpenAICompatProviders(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"aiand", providers.ProviderAIAND, "deepseek/deepseek-v4-flash"},
		{"openai", providers.ProviderAIAND, "moonshotai/kimi-k3"},
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
			body := `{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"hi"}]}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			require.NoError(t, svc.ProxyOpenAIChatCompletion(context.Background(), []byte(body), rec, req))
			require.Len(t, p.proxyBodies, 1, "%s must reach the upstream", tc.provider)
		})
	}
}

// TestService_CodexPassthrough_RoutesFreelyWithBothSubs guards against a
// Codex (ChatGPT) subscription forcing OpenAI-only routing when a Claude
// subscription is also present. Both should stay eligible so the scorer can
// route freely and the sub matching the chosen model pays. (Single-sub
// callers are unaffected: a lone Codex sub still yields {OpenAI}.)

// TestService_WithByokOnly_FiltersUnauthedProvidersFromScorer: with BYOK-only,
// providers without per-request creds must be excluded, or argmax 402s.
func TestService_WithByokOnly_FiltersUnauthedProvidersFromScorer(t *testing.T) {
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)
	providerMap := map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderAIAND:     &fakeProvider{},
	}

	t.Run("byok-off keeps every registered provider eligible (selfhost baseline)", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		assert.Contains(t, fr.capturedReq.EnabledProviders, providers.ProviderAnthropic)
		assert.Contains(t, fr.capturedReq.EnabledProviders, providers.ProviderAIAND)
	})

	t.Run("byok-on with no creds yields empty eligible set", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
			WithByokOnly(true)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		assert.Empty(t, fr.capturedReq.EnabledProviders, "BYOK-only: registered providers ineligible without creds")
	})

	t.Run("byok-on Anthropic surface with x-api-key enables Anthropic only", func(t *testing.T) {
		// A client x-api-key on the Anthropic surface is a legitimate passthrough
		// credential and enables Anthropic, but must not leak into AIand or
		// other OpenAI-compat upstreams on a different inbound surface.
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
			WithByokOnly(true)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		httpReq.Header.Set("x-api-key", "sk-ant-customer-key")
		_ = svc.ProxyMessages(context.Background(), body, rec, httpReq)

		require.NotNil(t, fr.capturedReq)
		assert.Contains(t, fr.capturedReq.EnabledProviders, providers.ProviderAnthropic,
			"client-supplied x-api-key on the Anthropic surface enables Anthropic")
		assert.NotContains(t, fr.capturedReq.EnabledProviders, providers.ProviderAIAND,
			"client header on the Anthropic surface must not leak credentials into OpenAI-compat upstreams")
	})

	t.Run("byok-on Anthropic surface with inbound subscription Bearer enables Anthropic only", func(t *testing.T) {
		// A Claude subscription OAuth bearer is legitimate Anthropic auth and
		// enables Anthropic, but must never enable AIand or other
		// OpenAI-compat upstreams — that cross-provider leak was the 2026-05-13
		// prod incident (argmax picked the compat upstream, 401'd with no key).
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
			WithByokOnly(true)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		httpReq.Header.Set("Authorization", "Bearer sk-ant-oat01-claude-code-token")
		_ = svc.ProxyMessages(context.Background(), body, rec, httpReq)

		require.NotNil(t, fr.capturedReq)
		assert.Contains(t, fr.capturedReq.EnabledProviders, providers.ProviderAnthropic,
			"a Claude subscription bearer is valid Anthropic auth and enables Anthropic")
		assert.NotContains(t, fr.capturedReq.EnabledProviders, providers.ProviderAIAND,
			"inbound Bearer on the Anthropic surface must never leak into OpenAI-compat upstreams (2026-05-13 incident)")
	})
}

// Model exclusion flows from installation context or env override into
// the router.Request that the scorer consumes. Env override wins.
func TestService_ExcludedModelsThroughRequest(t *testing.T) {
	body := []byte(`{"model":"deepseek-ai/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)
	providerMap := map[string]providers.Client{providers.ProviderAnthropic: &fakeProvider{}}

	t.Run("no override and no installation list → nil", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil)

		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		assert.Nil(t, fr.capturedReq.ExcludedModels)
	})

	t.Run("installation list populates request", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil)

		ctx := context.WithValue(context.Background(), proxy.InstallationExcludedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro", "moonshotai/kimi-k3"})
		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		assert.Contains(t, fr.capturedReq.ExcludedModels, "deepseek-ai/deepseek-v4-pro")
		assert.Contains(t, fr.capturedReq.ExcludedModels, "moonshotai/kimi-k3")
	})

	t.Run("env override replaces installation list", func(t *testing.T) {
		fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "zai-org/glm-5.3-flash"}}
		svc := proxy.NewService(fr, providerMap, nil, false, nil, nil, false, providers.ProviderAnthropic, "zai-org/glm-5.3-flash", nil).
			WithExcludedModelsOverride([]string{"qwen/qwen3.8-27b"})

		// Installation list says one thing; override says another. Override wins.
		ctx := context.WithValue(context.Background(), proxy.InstallationExcludedModelsContextKey{}, []string{"deepseek-ai/deepseek-v4-pro"})
		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
		require.NoError(t, svc.ProxyMessages(ctx, body, rec, httpReq))

		require.NotNil(t, fr.capturedReq)
		assert.Contains(t, fr.capturedReq.ExcludedModels, "qwen/qwen3.8-27b")
		assert.NotContains(t, fr.capturedReq.ExcludedModels, "deepseek-ai/deepseek-v4-pro")
		assert.True(t, svc.HasExcludedModelsOverride())
		assert.Equal(t, []string{"qwen/qwen3.8-27b"}, svc.ExcludedModelsOverride())
	})
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
		{name: "the top level reaches the wire unclamped", armEffort: "high", wantEffort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}}
			fr := &fakeRouter{decision: router.Decision{
				Provider: providers.ProviderAIAND,
				Model:    "zai-org/glm-5.3-flash",
				Effort:   tc.armEffort,
				Reason:   "test",
			}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAIAND: provider,
			}, nil, false, nil, nil, false, providers.ProviderAIAND, "moonshotai/kimi-k3", nil)

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
