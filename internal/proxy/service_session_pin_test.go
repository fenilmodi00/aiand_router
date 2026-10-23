package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/cluster"
	"aiand-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePinStore struct {
	mu                     sync.Mutex
	pin                    sessionpin.Pin
	hasPin                 bool
	hmmHistory             sessionpin.Pin
	hasHMMHistory          bool
	getErr                 error
	getCalls               int
	upserts                []sessionpin.Pin
	upsertCh               chan struct{}
	usages                 []sessionpin.Usage
	usageCh                chan struct{}
	incrementCalls         int
	incrementReturns       int // value returned by IncrementUpstreamErrors when hasPin is true
	resetCalls             int
	overloadIncrementCalls int
	overloadResetCalls     int
	disabledProviders      []string
	commandContinuations   map[string]sessionpin.Pin
	persistUpserts         bool
	demotions              []fakeDemotion
}

// fakeDemotion is one recorded ExpireAndDemoteModel call.
type fakeDemotion struct {
	Role   string
	Model  string
	Reason sessionpin.DemotionReason
}

func newFakePinStore() *fakePinStore {
	return &fakePinStore{
		upsertCh:             make(chan struct{}, 16),
		usageCh:              make(chan struct{}, 16),
		commandContinuations: make(map[string]sessionpin.Pin),
	}
}

func (f *fakePinStore) Get(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return sessionpin.Pin{}, false, f.getErr
	}
	if strings.HasSuffix(role, "_hmm_history") {
		if !f.hasHMMHistory {
			return sessionpin.Pin{}, false, nil
		}
		pin := f.hmmHistory
		pin.SessionKey = key
		pin.Role = role
		return pin, true, nil
	}
	if strings.HasSuffix(role, "_cmd_next") {
		pin, found := f.commandContinuations[role]
		if !found {
			return sessionpin.Pin{}, false, nil
		}
		pin.SessionKey = key
		pin.Role = role
		return pin, true, nil
	}
	if !f.hasPin {
		return sessionpin.Pin{}, false, nil
	}
	pin := f.pin
	pin.SessionKey = key
	pin.Role = role
	return pin, true, nil
}

func (f *fakePinStore) Upsert(ctx context.Context, p sessionpin.Pin) error {
	f.mu.Lock()
	f.upserts = append(f.upserts, p)
	if strings.HasSuffix(p.Role, "_cmd_next") {
		f.commandContinuations[p.Role] = p
	} else if f.persistUpserts {
		f.pin = p
		f.hasPin = p.PinnedUntil.After(time.Now()) && p.Model != "" && p.Provider != ""
	}
	f.mu.Unlock()
	select {
	case f.upsertCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakePinStore) Consume(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) (sessionpin.Pin, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pin, found := f.commandContinuations[role]
	if !found || !pin.PinnedUntil.After(time.Now()) {
		return sessionpin.Pin{}, false, nil
	}
	delete(f.commandContinuations, role)
	pin.SessionKey = key
	pin.Role = role
	return pin, true, nil
}

func (f *fakePinStore) UpdateUsage(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	f.mu.Lock()
	f.usages = append(f.usages, usage)
	f.mu.Unlock()
	select {
	case f.usageCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakePinStore) IncrementUpstreamErrors(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incrementCalls++
	if !f.hasPin {
		return 0, nil
	}
	return f.incrementReturns, nil
}

func (f *fakePinStore) ResetUpstreamErrors(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCalls++
	return nil
}

func (f *fakePinStore) IncrementOverloadErrors(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overloadIncrementCalls++
	if !f.hasPin {
		return 0, nil
	}
	// Real (accumulating) counter so multi-turn integration tests observe
	// consecutive strikes, unlike IncrementUpstreamErrors' canned return.
	f.pin.ConsecutiveOverloadErrors++
	return f.pin.ConsecutiveOverloadErrors, nil
}

func (f *fakePinStore) ResetOverloadErrors(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, expected router.Strategy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overloadResetCalls++
	if f.hasPin {
		f.pin.ConsecutiveOverloadErrors = 0
	}
	return nil
}

func (f *fakePinStore) ExpireAndDemoteModel(_ context.Context, expired sessionpin.Pin, model string, reason sessionpin.DemotionReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.demotions = append(f.demotions, fakeDemotion{Role: expired.Role, Model: model, Reason: reason})
	return nil
}

func (f *fakePinStore) DisableProvider(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role, provider string, expected router.Strategy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabledProviders = append(f.disabledProviders, provider)
	if f.hasPin {
		for _, p := range f.pin.DisabledProviders {
			if p == provider {
				return nil
			}
		}
		f.pin.DisabledProviders = append(f.pin.DisabledProviders, provider)
		f.pin.ConsecutiveOverloadErrors = 0
	}
	return nil
}

func (f *fakePinStore) SweepExpired(ctx context.Context) error { return nil }

func waitForUpsert(t *testing.T, store *fakePinStore) {
	t.Helper()
	select {
	case <-store.upsertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected an async pin upsert within 2s; none observed")
	}
}

func assertOnlyHMMHistoryUpserts(t *testing.T, store *fakePinStore) {
	t.Helper()
	require.NotEmpty(t, store.upserts, "HMM turns should persist switch history")
	for _, p := range store.upserts {
		assert.True(t, strings.HasSuffix(p.Role, "_hmm_history"), "HMM must not write the active routing pin role")
		assert.True(t, p.Reason == "hmm_history" || strings.HasPrefix(strings.TrimSpace(p.Reason), "hmm_policy"), "HMM history must retain an HMM marker")
		assert.NotEmpty(t, p.Provider, "HMM history rows retain provider for cache TTL math")
		assert.Empty(t, p.Model, "HMM history rows must not be routable")
	}
}

func newPinSvc(fr *fakeRouter, store *fakePinStore) *proxy.Service {
	return proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAIAND: &fakeProvider{}},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	)
}

func newPinSvcWithTelemetry(fr *fakeRouter, store *fakePinStore, telemetry proxy.TelemetryRepository) *proxy.Service {
	return proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAIAND: &fakeProvider{}},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		telemetry,
	)
}

func authedCtx(installationID string) context.Context {
	ctx := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "key-1")
	return context.WithValue(ctx, proxy.InstallationIDContextKey{}, installationID)
}

// authedCtxWithExternalKey also stashes a BYOK ExternalAPIKey, as the auth
// middleware does at runtime.
func authedCtxWithExternalKey(installationID, provider string, plaintext []byte) context.Context {
	ctx := authedCtx(installationID)
	keys := []*auth.ExternalAPIKey{{
		InstallationID: installationID,
		Provider:       provider,
		Plaintext:      plaintext,
	}}
	return context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, keys)
}

const pinTestBody = `{
	"model":"z-ai/glm-5.3",
	"system":"sys",
	"messages":[{"role":"user","content":"original prompt"}]
}`

const nativeWebSearchSubTurnBody = `{
	"model":"z-ai/glm-5.3",
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"tool_choice":{"type":"tool","name":"web_search"},
	"messages":[{"role":"user","content":"Perform a web search for the query: git-ai latest release"}]
}`

// compactionBody triggers the compaction detector (§3.4).
const compactionBody = `{
	"model":"z-ai/glm-5.3",
	"system":"Your task is to create a detailed summary of the conversation so far.",
	"messages":[{"role":"user","content":"go"}]
}`

// exploreBody marks an Explore sub-agent dispatch (§3.4).
const exploreBody = `{
	"model":"z-ai/glm-5.3",
	"metadata":{"user_id":"subagent:Explore"},
	"messages":[{"role":"user","content":"list go files"}]
}`

// With no BYOK and resolver ok=false, hard-pin must surface
// ErrClusterUnavailable rather than dispatch to an unauthenticatable default.
func TestService_HardPin_Compaction_ByokOnly_NoEligibleProviderErrors(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "z-ai/glm-5.3", Reason: "cluster"}}

	resolver := func(req proxy.HardPinRequest) (string, string, bool) {
		if _, ok := req.EnabledProviders[providers.ProviderAIAND]; ok {
			return providers.ProviderAIAND, "claude-haiku", true
		}
		return "", "", false
	}

	providerMap := map[string]providers.Client{providers.ProviderAIAND: &fakeProvider{}}
	svc := proxy.NewService(
		fr, providerMap, nil, false, nil, store, false,
		providers.ProviderAIAND, "deepseek/cheap",
		nil,
	).WithByokOnly(true).WithHardPinResolver(resolver)

	ctx := authedCtx(uuid.New().String()) // no external keys
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err := svc.ProxyMessages(ctx, []byte(compactionBody), rec, httpReq)
	require.Error(t, err, "hard-pin with no eligible provider must surface an error, not silently dispatch")
	assert.ErrorIs(t, err, cluster.ErrClusterUnavailable,
		"error must be ErrClusterUnavailable so handlers map it to HTTP 503")
}

// classifierBody: small max_tokens, no tools — DetectFromEnvelope tags this
// Classifier. It is scored like a main-loop turn but never reads or writes a
// session pin.
const classifierBody = `{"model":"z-ai/glm-5.3-flash","max_tokens":5,"messages":[{"role":"user","content":"hello"}]}`

// Regression guard: excluded_models must be honored on the hard-pin tier too.
// Prod symptom: an excluded gemini model still got all utility traffic
// because the hard-pin path never consulted req.ExcludedModels.
func TestService_HardPin_TitleGen_AppliesExcludedModels(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "z-ai/glm-5.3", Reason: "cluster"}}

	const excludedModel = "gemini-3.1-flash-lite-preview"
	const allowedFallback = "z-ai/glm-5.3-flash"

	// Mimics cluster.FastestModelInSet: fastest candidate is excluded, so
	// resolver must fall through to the next allowed candidate.
	resolver := func(req proxy.HardPinRequest) (string, string, bool) {
		if _, denied := req.ExcludedModels[excludedModel]; !denied {
			return providers.ProviderAIAND, excludedModel, true
		}
		return providers.ProviderAIAND, allowedFallback, true
	}

	// Both upstreams answer 200 so the served model reflects the hard-pin, not a failover.
	okResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}
	providerMap := map[string]providers.Client{
		providers.ProviderAIAND: &fakeProvider{proxyResponse: okResp},
	}
	svc := proxy.NewService(
		fr, providerMap, nil, false, nil, store, false,
		providers.ProviderAIAND, excludedModel, // boot-time pin is the excluded model
		nil,
	).WithExplicitUtilityHardPin(true).WithHardPinResolver(resolver)

	ctx := context.WithValue(authedCtx(uuid.New().String()),
		proxy.InstallationExcludedModelsContextKey{}, []string{excludedModel})
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(titleGenBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "title generation must bypass the cluster scorer")
	assert.Equal(t, allowedFallback, rec.Header().Get(proxy.HeaderRouterModel),
		"hard-pin must skip the excluded model and serve an allowed candidate")
	assert.NotEqual(t, excludedModel, rec.Header().Get(proxy.HeaderRouterModel),
		"excluded model must never be served on the hard-pin tier")
}

// WithSubAgentOverride must route SubAgentDispatch turns to the configured
// pair instead of the shared hardPinProvider/hardPinModel — even with
// hardPinExplore off, since an explicit override implies the operator wants
// sub-agents pinned regardless of that legacy flag.
func TestService_HardPin_SubAgentOverrideRoutesIndependentlyOfHardPin(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: "anthropic", Model: "z-ai/glm-5.3", Reason: "cluster"}}
	// OpenRouter speaks the OpenAI-compat wire format, so the fake response
	// must be a valid chat-completion body for the cross-format translation
	// back to the Anthropic-shaped client response to succeed.
	openRouterResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAIAND: &fakeProvider{proxyResponse: openRouterResp},
		},
		nil,
		false,
		nil,
		store,
		false, // hardPinExplore=false: override alone must still hard-pin
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	).WithSubAgentOverride(providers.ProviderAIAND, "local/qwen3-coder")

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(exploreBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "sub-agent override must bypass the cluster scorer")
	assert.Equal(t, "local/qwen3-coder", rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.NotEqual(t, "z-ai/glm-5.3-flash", rec.Header().Get(proxy.HeaderRouterModel),
		"must not fall back to the shared hardPinProvider/hardPinModel pair")
}

// An explicit sub-agent override is Go-owned dispatch policy and applies under
// the HMM strategy too; the sidecar no longer has a sub-agent path of its own.
func TestService_HardPin_SubAgentOverrideAppliesUnderHMMStrategy(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.3",
		Reason:   "hmm_policy:classifier_select(label=medium)",
		Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM)},
	}}
	openRouterResp := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAIAND: &fakeProvider{proxyResponse: openRouterResp},
		},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	).WithSubAgentOverride(providers.ProviderAIAND, "local/qwen3-coder").WithHMMRouter(fr)

	ctx := router.WithStrategy(authedCtx(uuid.New().String()), router.StrategyHMM)
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(exploreBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "sub-agent override must bypass the HMM classifier")
	assert.Equal(t, "local/qwen3-coder", rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, providers.ProviderAIAND, rec.Header().Get(proxy.HeaderRouterProvider))
}

// OpenAI ingress: same Stage 1 path via ProxyOpenAIChatCompletion.

const openAIPinTestBody = `{
	"model":"gpt-4o",
	"messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"original prompt"}
	]
}`

func newOpenAIPinSvc(fr *fakeRouter, store *fakePinStore) *proxy.Service {
	return proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAIAND: &fakeProvider{},
		},
		nil, false, nil,
		store,
		false, providers.ProviderAIAND, "z-ai/glm-5.3-flash",
		nil,
	)
}

// OpenAI ingress: Tier-2 pin hit keeps the pinned model under the
// planner (no prior-turn usage → stay).
func TestService_SessionPin_OpenAI_PostgresHitKeepsPinnedModel(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderAIAND,
		Model:         "gpt-5",
		Reason:        "cluster:v0.2",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "cluster:v0.2"}}
	svc := newOpenAIPinSvc(fr, store)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(openAIPinTestBody), rec, httpReq))

	assert.Equal(t, 1, fr.routeCalls, "scorer runs every MainLoop turn under the planner")
	assert.Equal(t, "gpt-5", rec.Header().Get(proxy.HeaderRouterModel))
	waitForUpsert(t, store)
}

func TestService_SessionPin_OpenAI_FreshRouteCreatesPin(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "fresh"}}
	svc := newOpenAIPinSvc(fr, store)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(openAIPinTestBody), rec, httpReq))

	assert.Equal(t, 1, fr.routeCalls, "first turn must route fresh")
	waitForUpsert(t, store)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, providers.ProviderAIAND, store.upserts[0].Provider)
	assert.Equal(t, "gpt-4o", store.upserts[0].Model)
}

func TestService_SessionPin_OpenAI_UnforceModelCommandClearsPin(t *testing.T) {
	const unforceBody = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"/unforce-model"}
		]
	}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIPinSvc(fr, store)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(unforceBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "unforce-model command must short-circuit routing")
	require.Len(t, store.upserts, 1)
	assert.Equal(t, "user_unforced", store.upserts[0].Reason)
	assert.Empty(t, store.upserts[0].Provider)
	assert.Empty(t, store.upserts[0].Model)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chat.completion", resp["object"])
	choices, ok := resp["choices"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, choices)
	first, ok := choices[0].(map[string]any)
	require.True(t, ok)
	msg, ok := first["message"].(map[string]any)
	require.True(t, ok)
	content, _ := msg["content"].(string)
	assert.Contains(t, content, "force-model cleared")
}

func TestService_SessionPin_OpenAI_ForceModelCommandStreamShape(t *testing.T) {
	const forceStreamBody = `{
		"model":"gpt-4o",
		"stream":true,
		"messages":[
			{"role":"user","content":"/force-model gpt-5"}
		]
	}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIPinSvc(fr, store)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(forceStreamBody), rec, httpReq))

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	assert.Contains(t, body, `"object":"chat.completion.chunk"`)
	assert.Contains(t, body, "data: [DONE]")
	assert.NotContains(t, body, `"type":"message"`, "must not emit Anthropic wire format on OpenAI ingress")
}

func TestService_SessionPin_OpenAI_ToolResultShortCircuit(t *testing.T) {
	// Kill switch OFF: pinned tool_result reuses the pin verbatim (legacy #82 path).
	// Default (scorer runs) is covered by turnloop_test.go.
	const toolResultBody = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"original prompt"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"t1","content":"ls output"}
		]
	}`
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "gpt-5",
		Reason:      "cluster:v0.2",
		PinnedUntil: time.Now().Add(30 * time.Minute),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "fresh"}}
	svc := newOpenAIPinSvc(fr, store).WithScoreToolResultTurns(false)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(toolResultBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "disabled tool-result scoring must not re-run the scorer")
	assert.Equal(t, "gpt-5", rec.Header().Get(proxy.HeaderRouterModel))
}

// newOpenAIHardPinSvc keeps the hard-pin target on OpenAI so the path stays
// same-format (cross-format translation needs a real response body).
func newOpenAIHardPinSvc(fr *fakeRouter, store *fakePinStore, hardPinExplore bool) *proxy.Service {
	return proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAIAND: &fakeProvider{},
		},
		nil, false, nil,
		store,
		hardPinExplore,
		providers.ProviderAIAND,
		"gpt-4o-mini",
		nil,
	)
}

// Regression guard: Claude Code's compaction phrase is Claude-Code-only, but
// pre-fix the detector matched it on any wire format, so Codex's flattened
// `instructions` field could hard-pin every turn to the cheap model.
// Compaction detection is now gated on Anthropic format only.
func TestService_OpenAI_CompactionPhraseDoesNotHardPin(t *testing.T) {
	const compactionOpenAIBody = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"system","content":"Your task is to create a detailed summary of the conversation so far."},
			{"role":"user","content":"go"}
		]
	}`
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIHardPinSvc(fr, store, false)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(compactionOpenAIBody), rec, httpReq))

	assert.Equal(t, 1, fr.routeCalls, "OpenAI body must run the scorer, not hard-pin")
	assert.Equal(t, "gpt-4o", rec.Header().Get(proxy.HeaderRouterModel))
}

func TestService_HardPin_OpenAI_SubAgentHeaderHintRoutesToHardPin(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIHardPinSvc(fr, store, true)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	httpReq.Header.Set("x-aiand-subagent-type", "Explore")
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(openAIPinTestBody), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "x-aiand-subagent-type must trigger Explore hard-pin")
	assert.Equal(t, "gpt-4o-mini", rec.Header().Get(proxy.HeaderRouterModel))
}

// haikuClampBody requests haiku (Low); scorer returns opus (High) — router
// honors the upgrade instead of downgrading to a cheap in-tier model.
const haikuClampBody = `{
	"model":"z-ai/glm-5.3-flash",
	"system":"sys",
	"messages":[{"role":"user","content":"summarize this"}]
}`

// buildCyclicLoopBody builds a wide re-read cycle (same files re-Read, no
// edits) — enough to trip detectCyclicToolCallLoop.
func buildCyclicLoopBody(t *testing.T, nFiles, total int) []byte {
	t.Helper()
	msgs := []any{map[string]any{"role": "user", "content": "do the task"}}
	for i := 0; i < total; i++ {
		id := "toolu_" + strconv.Itoa(i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": id, "name": "Read",
					"input": map[string]any{"file_path": "/app/f" + strconv.Itoa(i%nFiles) + ".go"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": "x"},
			}},
		)
	}
	b, err := json.Marshal(map[string]any{"model": "z-ai/glm-5.3", "max_tokens": 256, "messages": msgs})
	require.NoError(t, err)
	return b
}

// An unrecognized x-aiand-force-model value fails the request: routing on
// regardless would serve a model the caller never asked for while looking to
// them like the force took.
func TestService_ForceModelHeader_UnknownModelRejected(t *testing.T) {
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash", Reason: "fresh"}}
	svc := newPinSvc(fr, store)

	ctx := authedCtx(uuid.New().String())
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	httpReq.Header.Set(proxy.ForceModelHeader, "totally-not-a-model")
	err := svc.ProxyMessages(ctx, []byte(pinTestBody), rec, httpReq)

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.Contains(t, cls.Message, "totally-not-a-model")
	assert.Equal(t, 0, fr.routeCalls, "an unservable force must not fall through to the scorer")
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.upserts, "a refused force must not write any pin")
}

// authedCtxWithGatewayKey attaches an openai_gateway BYOK key, optionally
// aliasing one catalog model onto it.
func authedCtxWithGatewayKey(installationID, aliasedModel string) context.Context {
	key := &auth.ExternalAPIKey{
		InstallationID: installationID,
		Provider:       providers.ProviderAIAND,
		Plaintext:      []byte("gw-token"),
		BaseURL:        "https://gateway.example.com/v1",
	}
	if aliasedModel != "" {
		key.ModelAliases = map[string]string{aliasedModel: aliasedModel}
	}
	return context.WithValue(authedCtx(installationID), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{key})
}
