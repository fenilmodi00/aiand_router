package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatewayAllowlistCtx is a gateway-exclusive installation whose allowlist
// narrows automatic routing to one of the models its gateway key aliases.
func gatewayAllowlistCtx(allowed []string, aliased ...string) context.Context {
	aliases := make(map[string]string, len(aliased))
	for _, m := range aliased {
		aliases[m] = m
	}
	id := uuid.New().String()
	key := &auth.ExternalAPIKey{
		InstallationID: id,
		Provider:       providers.ProviderAIAND,
		Plaintext:      []byte("gw-token"),
		BaseURL:        "https://gateway.example.com",
		ModelAliases:   aliases,
	}
	ctx := context.WithValue(authedCtx(id), proxy.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{key})
	return context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, allowed)
}

func anthropicOKProvider() *fakeProvider {
	return &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}}
}

// The tenant's gateway is the access boundary, so its allowlist shapes only
// what the router picks; an explicit force reaches any model the gateway aliases.
func TestService_GatewayForcedPinOutsideAllowlistServes(t *testing.T) {
	const body = `{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"analyze usage"}]}`
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:    providers.ProviderAIAND,
		Model:       "z-ai/glm-5.2",
		Reason:      translate.ReasonUserForceModel,
		PinnedUntil: time.Now().Add(time.Hour),
	}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.2", Reason: "cluster:v0.2",
	}}
	svc := proxy.NewService(fr,
		map[string]providers.Client{providers.ProviderAIAND: anthropicOKProvider()},
		nil, false, nil, store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil)

	ctx := gatewayAllowlistCtx([]string{"z-ai/glm-5.2"}, "z-ai/glm-5.2", "z-ai/glm-5.2")
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))

	assert.Equal(t, 0, fr.routeCalls, "the force must not fall through to automatic routing")
	assert.Equal(t, "z-ai/glm-5.2", rec.Header().Get(proxy.HeaderRouterModel))
	assert.NotContains(t, rec.Body.String(), "could not be served")
}
