package proxy

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/websearch"
)

// serveNativeWebSearch reports that a native web-search turn stays on normal
// routing: the executor backend was cut with the AIand-only split, so nothing
// executes Anthropic's server tool in the router. The detectors stay for when
// an executor returns.
func (s *Service) serveNativeWebSearch(
	ctx context.Context,
	body []byte,
	stream bool,
	enabled map[string]struct{},
) bool {
	tool, hasTool := websearch.FindServerTool(body)
	if !hasTool || anyNativeServerToolProvider(enabled) {
		return false
	}
	log := observability.FromContext(ctx)
	if _, isSearchTurn := websearch.DetectSearchTurn(body); !isSearchTurn {
		log.Info("Native web-search tool on a provider that cannot serve it; turn left on normal routing",
			"tool_type", tool.Type)
		return false
	}
	log.Info("Native web-search turn left on normal routing", "tool_type", tool.Type)
	return false
}

// anyNativeServerToolProvider reports whether any enabled provider runs
// Anthropic server tools natively, in which case the turn stays on routing.
func anyNativeServerToolProvider(enabled map[string]struct{}) bool {
	for provider := range enabled {
		if providers.SupportsAnthropicServerTools(provider) {
			return true
		}
	}
	return false
}
