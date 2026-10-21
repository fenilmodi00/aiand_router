package proxy_test

// OpenAI /v1/chat/completions conformance cases, shared by every OpenAI-compat
// upstream. Each pins a known translation behavior or past regression.

import (
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
)

func openRouterClient(baseURL string) providers.Client {
	return openaicompat.NewClient("test-key", baseURL)
}

const weatherTool = `[{"name":"get_weather","description":"Get the weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`

// readTool mirrors the Claude Code Read tool shape: required param, optional
// string, integer, closed schema — for toolcheck validate/repair cases.
const readTool = `[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"pages":{"type":"string"},"limit":{"type":"integer"}},"required":["file_path"],"additionalProperties":false}}]`
