package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Codex (ChatGPT) subscription bearer is a JWT-shaped token (not sk-/rk_)
// paired with a ChatGPT-Account-ID header; the pair resolves to an OAuth
// subscription credential the Codex backend serves for free.
const (
	codexSubToken     = "eyJhbGciOi.codex.jwt"
	codexSubAccountID = "acct-codex-123"
)

// codexSubRequest builds an OpenAI chat-completions request carrying a Codex
// subscription in the inbound Authorization header.
func codexSubRequest(t *testing.T, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+codexSubToken)
	req.Header.Set("ChatGPT-Account-ID", codexSubAccountID)
	return httptest.NewRecorder(), req
}

// TestSubscriptionOnly_OpenAI_PaidRoute_Refuses402: a Codex-covered request
// that routing resolves to a paid provider (not served on the subscription)
// must be refused with the credits-exhausted sentinel and never dispatched —
// the bug the Codex path previously had, where such a turn debited past the
// floor with no bound.
func TestSubscriptionOnly_OpenAI_PaidRoute_Refuses402(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-chat", Reason: "test"}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion"}`)
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAIAND: p}, nil, false, nil, nil, false, providers.ProviderAIAND, "gpt-5.6-sol", nil)

	// MainLoop-shaped (tools + large max_tokens) so the turn isn't classified as
	// a hard-pinned classifier turn; that would bypass the scorer and defeat the
	// paid-route scenario under test.
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Refactor the auth middleware and add tests."}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"edit_file","parameters":{"type":"object"}}}]}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
	require.Error(t, err)
	require.Positive(t, fr.routeCalls, "the scorer must be consulted so the decision is the paid route under test")
	assert.True(t, errors.Is(err, proxy.ErrCreditsExhaustedSubscriptionUnavailable),
		"a Codex-covered turn that routes to a paid model must be refused, not dispatched")
	assert.Empty(t, p.proxyBodies, "no paid dispatch may occur below the floor in subscription-only mode")
}
