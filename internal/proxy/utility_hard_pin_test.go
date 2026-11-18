package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/policy"
)

type utilityHardPinSetup struct {
	name      string
	clients   []string
	byokOnly  bool
	ctx       func(installationID string) context.Context
	wantModel string
	wantVia   string
}

func newUtilityHardPinSvc(setup utilityHardPinSetup, fr *fakeRouter) *proxy.Service {
	clients := make(map[string]providers.Client, len(setup.clients))
	for _, name := range setup.clients {
		clients[name] = &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}}
	}
	return proxy.NewService(
		fr, clients, nil, false, nil, newFakePinStore(), true,
		policy.UtilityHardPinDefaultProvider, policy.UtilityHardPinDefaultModel,
		nil,
	).WithByokOnly(setup.byokOnly).WithHardPinResolver(proxy.CatalogUtilityHardPin)
}

// Explore sub-agent hard pins resolve from the catalog against each
// installation's reachable providers, exclusions and gateway aliases.
func TestService_UtilityHardPin_CatalogTargetPerInstallation(t *testing.T) {
	setups := []utilityHardPinSetup{
		{
			name:     "byok_only",
			clients:  []string{providers.ProviderAIAND},
			byokOnly: true,
			ctx: func(id string) context.Context {
				return authedCtxWithExternalKey(id, providers.ProviderAIAND, []byte("sk-byok"))
			},
			wantModel: policy.UtilityHardPinDefaultModel,
			wantVia:   providers.ProviderAIAND,
		},
		{
			name:    "excluded_default_model",
			clients: []string{providers.ProviderAIAND},
			ctx: func(id string) context.Context {
				return context.WithValue(authedCtx(id), proxy.InstallationExcludedModelsContextKey{}, []string{policy.UtilityHardPinDefaultModel})
			},
			wantModel: catalog.ModelIDDeepSeekV41Flash1017.String(),
			wantVia:   providers.ProviderAIAND,
		},
	}
	for _, setup := range setups {
		t.Run(setup.name, func(t *testing.T) {
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: catalog.ModelIDGLM53.String()}}
			svc := newUtilityHardPinSvc(setup, fr)
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

			require.NoError(t, svc.ProxyMessages(setup.ctx(uuid.New().String()), []byte(exploreBody), rec, httpReq))

			assert.Equal(t, 0, fr.routeCalls, "utility hard pins bypass the scorer")
			assert.Equal(t, setup.wantModel, rec.Header().Get(proxy.HeaderRouterModel))
			assert.Equal(t, setup.wantVia, rec.Header().Get(proxy.HeaderRouterProvider))
		})
	}
}

// No servable utility model is a fixable configuration problem, not a
// retryable router outage.
func TestService_UtilityHardPin_NoEligibleProviderIs400(t *testing.T) {
	setup := utilityHardPinSetup{
		clients:  []string{providers.ProviderAIAND},
		byokOnly: true,
		ctx:      authedCtx,
	}
	svc := newUtilityHardPinSvc(setup, &fakeRouter{})
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	err := svc.ProxyMessages(setup.ctx(uuid.New().String()), []byte(exploreBody), rec, httpReq)

	require.ErrorIs(t, err, router.ErrNoEligibleProvider)
	require.NotErrorIs(t, err, router.ErrRoutingUnavailable)
	class, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorNoEligibleProvider, class.Kind)
	assert.Equal(t, http.StatusBadRequest, class.Status)
}
