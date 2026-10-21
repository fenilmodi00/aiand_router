package anthropic_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

// slowSubscriptionStream is the upstream's OpenAI-compatible chat-completions
// SSE; the router translates it into the Anthropic-shaped client response.
const slowSubscriptionStream = "data: {\"id\":\"chatcmpl_synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"slow subscription answer\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl_synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4}}\n\n" +
	"data: [DONE]\n\n"

// slowSubscriptionProvider answers after a delay unless its request context
// ends first, recording which credential source each call used.
type slowSubscriptionProvider struct {
	delay   time.Duration
	sources []string
}

func (p *slowSubscriptionProvider) SupportsSubscriptions() bool { return true }

func (p *slowSubscriptionProvider) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	source := ""
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		source = creds.Source
	}
	p.sources = append(p.sources, source)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.delay):
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(slowSubscriptionStream))
	return err
}

func (p *slowSubscriptionProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestMessagesHandler_LinkedSubscriptionSlowFirstResponseSucceeds(t *testing.T) {
	provider := &slowSubscriptionProvider{delay: 12 * time.Second}
	// The service owns background cache janitors that never exit, so it is built
	// outside the bubble; every request-path timer is still created inside it.
	svc := newTestService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-v4.1-flash", Reason: "test"}}, providers.ProviderAIAND, provider).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}})
	engine := messagesEngine(svc)
	synctest.Test(t, func(t *testing.T) {
		body := `{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"synthetic slow turn"}],"max_tokens":4096}`
		ctx := context.WithValue(proxy.WithManagedSubscriptionUsage(context.Background()), proxy.AnthropicSubscriptionContextKey{}, "sk-ant-oat01-synthetic-linked-token")
		rec := httptest.NewRecorder()

		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body))).WithContext(ctx))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "slow subscription answer")
		assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
		assert.NotContains(t, rec.Body.String(), "Upstream call failed")
		assert.Equal(t, []string{requestcontext.SourceSubscription}, provider.sources, "served once on the linked subscription, with no paid rescue")
	})
}
