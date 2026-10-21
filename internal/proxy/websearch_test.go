package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/websearch"
)

type fakeSearch struct {
	got  websearch.Query
	resp websearch.Response
	err  error
	runs int
}

func (f *fakeSearch) Search(_ context.Context, q websearch.Query) (websearch.Response, error) {
	f.runs++
	f.got = q
	return f.resp, f.err
}

const searchTurnBody = `{
	"model":"deepseek/deepseek-v4-pro",
	"stream":false,
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"messages":[{"role":"user","content":"Perform a web search for the query: cortex agents"}]
}`

func gatewayCtx() context.Context {
	return context.WithValue(context.Background(), CredentialsContextKey{}, &Credentials{
		APIKey:   []byte("WIF.GCP.token"),
		BaseURL:  "https://acct.snowflakecomputing.com/api/v2/cortex",
		AuthType: auth.AuthTypeWIF,
	})
}

func gatewayOnly() map[string]struct{} {
	return map[string]struct{}{providers.ProviderAIAND: {}}
}

func TestServeNativeWebSearchDefersToNativelyCapableProvider(t *testing.T) {
	ex := &fakeSearch{}
	s := &Service{webSearch: ex}
	enabled := map[string]struct{}{providers.ProviderAIAND: {}}

	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "deepseek/deepseek-v4-pro", false, 0, enabled, http.Header{}, httptest.NewRecorder()) {
		t.Fatal("vendor Anthropic runs the tool itself; the turn must stay on normal routing")
	}
	if ex.runs != 0 {
		t.Fatal("executor must not run when a capable provider is available")
	}
}

func TestServeNativeWebSearchFallsThroughOnExecutorFailure(t *testing.T) {
	ex := &fakeSearch{err: context.DeadlineExceeded}
	s := &Service{webSearch: ex}
	rec := httptest.NewRecorder()

	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "deepseek/deepseek-v4-pro", false, 0, gatewayOnly(), http.Header{}, rec) {
		t.Fatal("a failed search must not claim the turn")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing may be written before falling through: %q", rec.Body.String())
	}
}

func TestServeNativeWebSearchIgnoresTurnsWithoutTheTool(t *testing.T) {
	ex := &fakeSearch{}
	s := &Service{webSearch: ex}
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"Perform a web search for the query: x"}]}`)

	if s.serveNativeWebSearch(gatewayCtx(), body, "deepseek/deepseek-v4-pro", false, 0, gatewayOnly(), http.Header{}, httptest.NewRecorder()) {
		t.Fatal("no native tool declared; nothing to serve")
	}
	if ex.runs != 0 {
		t.Fatal("executor ran without a declared server tool")
	}
}

func TestServeNativeWebSearchDisabled(t *testing.T) {
	s := &Service{}
	if s.serveNativeWebSearch(gatewayCtx(), []byte(searchTurnBody), "deepseek/deepseek-v4-pro", false, 0, gatewayOnly(), http.Header{}, httptest.NewRecorder()) {
		t.Fatal("no executor wired; the turn must stay on normal routing")
	}
}
