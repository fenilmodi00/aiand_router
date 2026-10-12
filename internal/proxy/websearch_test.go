package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/providers"
)

const searchTurnBody = `{
	"model":"deepseek-ai/deepseek-v4.1-flash",
	"stream":false,
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"messages":[{"role":"user","content":"Perform a web search for the query: cortex agents"}]
}`

// noNativeServerToolProviders is an enabled-provider set with no provider that
// runs Anthropic server tools natively, so the native web-search detector
// cannot defer to routing on that ground.
func noNativeServerToolProviders() map[string]struct{} {
	return map[string]struct{}{providers.ProviderAIAND: {}}
}

// The web-search executor was cut with the AIand-only split: no turn is served
// by the router, and every native web-search turn stays on normal routing.
func TestServeNativeWebSearchAlwaysRoutes(t *testing.T) {
	s := &Service{}
	ctx := context.Background()

	if s.serveNativeWebSearch(ctx, []byte(searchTurnBody), false, noNativeServerToolProviders()) {
		t.Fatal("no executor exists; the turn must stay on normal routing")
	}

	enabled := map[string]struct{}{providers.ProviderAnthropic: {}}
	if s.serveNativeWebSearch(ctx, []byte(searchTurnBody), false, enabled) {
		t.Fatal("a natively capable provider also stays on normal routing")
	}

	if s.serveNativeWebSearch(ctx, []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash"}`), false, noNativeServerToolProviders()) {
		t.Fatal("no native tool declared; nothing to serve")
	}
}
