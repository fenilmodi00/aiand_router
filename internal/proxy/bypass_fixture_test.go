package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
)

const (
	bypassRequestedMdl  = "deepseek/deepseek-v4.1-flash"
	bypassScorerPickMdl = "z-ai/glm-5.3-flash"
)

// bypassFixture builds a service whose fake scorer would route to
// bypassScorerPickMdl, with a fake provider that returns a minimal valid
// Messages response so a routed turn completes.
func bypassFixture(t *testing.T) (*proxy.Service, *fakeRouter, *fakeProvider) {
	t.Helper()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: bypassScorerPickMdl}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + bypassScorerPickMdl + `","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAIAND: p}, nil, false, nil, nil, false, providers.ProviderAIAND, bypassScorerPickMdl, nil)
	return svc, fr, p
}

// bypassCtx returns the request context the bypass-era fixtures used.
func bypassCtx() context.Context {
	return context.Background()
}

func bypassRequest(t *testing.T) (*httptest.ResponseRecorder, *http.Request, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"` + bypassRequestedMdl + `","messages":[{"role":"user","content":"hi"}]}`)
	return rec, req, body
}
