package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"aiand-os/router/internal/router/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/cache"
	"aiand-os/router/internal/router/policy"
)

func escalationCompletionContext() context.Context {
	ctx := escalationTestContext(true, false)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	return context.WithValue(ctx, APIKeyIDContextKey{}, "completion-test-key")
}

func newEscalationCompletionService(store *escalationTestStore, observer *escalationTestObserver, clients map[string]providers.Client) *Service {
	return NewService(nil, clients, nil, false, nil, newStubPinStore(), false, providers.ProviderAIAND, "z-ai/glm-5.3", nil).
		WithEscalation(store, observer).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
}

type escalationCompletionProvider struct {
	writeResponse func(http.ResponseWriter) error
}

func (p escalationCompletionProvider) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	return p.writeResponse(w)
}
func (p escalationCompletionProvider) Passthrough(_ context.Context, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	return p.writeResponse(w)
}

type escalationFinalizeFailureWriter struct {
	*httptest.ResponseRecorder
	writeErr    error
	beforeWrite func()
}

func (w *escalationFinalizeFailureWriter) Write(body []byte) (int, error) {
	w.beforeWrite()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(body)
}

func TestEscalationTurnsBypassPopulatedSemanticCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format cache.Format
		body   string
		invoke func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", cache.FormatAnthropic, `{"model":"z-ai/glm-5.3","max_tokens":4096,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect the repository"}]}`, (*Service).ProxyMessages},
		{"chat", cache.FormatOpenAI, `{"model":"gpt-5","messages":[{"role":"user","content":"inspect the repository"}],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`, (*Service).ProxyOpenAIChatCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newEscalationTestStore()
			semanticCache := cache.New(cache.DefaultConfig())
			embedding := []float32{1, 0}
			const externalID = "escalation-cache-test"
			provenance := cache.NewProvenance(cache.ProvenanceScope{CredentialSubject: "completion-test-key", Product: cache.ProductLegacy, Model: catalog.ModelIDGLM53Flash, Provider: providers.ProviderAIAND, UpstreamScope: "deployment"})
			semanticCache.Store(externalID, tc.format, embedding, 1, cache.CachedResponse{StatusCode: http.StatusOK, Body: []byte(`{"cached":true}`)}, "", 0, provenance)
			_, hit := semanticCache.Lookup(externalID, tc.format, embedding, []int{1}, "", 0, provenance)
			require.True(t, hit)
			classifier := &authoritativeTestRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53Flash.String(), Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding), Embedding: embedding, ClusterIDs: []int{1}}}}
			svc := NewService(nil, nil, nil, false, semanticCache, newStubPinStore(), false, providers.ProviderAIAND, "z-ai/glm-5.3", nil).WithEscalation(store, &escalationTestObserver{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: classifier, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}})
			ctx := context.WithValue(escalationCompletionContext(), ExternalIDContextKey{}, externalID)
			recorder := httptest.NewRecorder()
			err := tc.invoke(svc, ctx, []byte(tc.body), recorder, httptest.NewRequest(http.MethodPost, "/test", nil))
			require.ErrorIs(t, err, ErrProviderNotConfigured, "an opted-in observation must attempt dispatch instead of returning cached content")
			require.NotContains(t, recorder.Body.String(), "cached")
			require.Len(t, store.sessions, 1)
			for _, session := range store.sessions {
				require.NotNil(t, session.PreviousOutcome)
				require.True(t, session.PreviousOutcome.IsError)
			}
		})
	}
}
