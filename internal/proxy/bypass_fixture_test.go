package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

const (
	bypassSubToken      = "sk-ant-oat01-subscription-token"
	bypassRequestedMdl  = "deepseek/deepseek-v4.1-flash"
	bypassScorerPickMdl = "z-ai/glm-5.3-flash"
)

// bypassFixture builds a service whose fake scorer would route to
// bypassScorerPickMdl, with the subscription usage observer wired and a fake
// Anthropic provider that returns a minimal valid Messages response so a routed
// turn completes. seedUtil >= 0 pre-records an observation at that utilization
// under the subscription token; seedUtil < 0 leaves the observer cold.
func bypassFixture(t *testing.T, seedUtil float64) (*proxy.Service, *fakeRouter, *fakeProvider) {
	t.Helper()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: bypassScorerPickMdl}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + bypassScorerPickMdl + `","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}}
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	if seedUtil >= 0 {
		obs.Record(obs.Key([]byte(bypassSubToken)), usage.Snapshot{
			Primary: usage.Window{UsedPercent: seedUtil, WindowMinutes: 300},
		})
	}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAIAND: p}, nil, false, nil, nil, false, providers.ProviderAIAND, bypassScorerPickMdl, nil).
		WithUsageObserver(obs)
	return svc, fr, p
}

// bypassCtx returns the request context the bypass-era fixtures used.
func bypassCtx(threshold float64) context.Context {
	return context.Background()
}

func bypassRequest(t *testing.T) (*httptest.ResponseRecorder, *http.Request, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"` + bypassRequestedMdl + `","messages":[{"role":"user","content":"hi"}]}`)
	return rec, req, body
}
