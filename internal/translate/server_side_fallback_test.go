package translate_test

import (
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const serverSideFallbackBody = `{"model":"z-ai/glm-5.2","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`

func prepareWithFallback(t *testing.T, in http.Header, body string, opts translate.EmitOptions) providers.PreparedRequest {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	opts.Capabilities = router.Lookup(opts.TargetModel)
	prep, err := env.PrepareAnthropic(in, opts)
	require.NoError(t, err)
	return prep
}

// No wired target accepts Anthropic's top-level "fallbacks" field, so it is
// never forwarded and its beta token never rides along.
func TestServerSideFallback_FieldAndBetaNeverForwarded(t *testing.T) {
	in := http.Header{}
	in.Set("anthropic-beta", "server-side-fallback-2026-07-01")
	body := `{"model":"z-ai/glm-5.3-flash","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"fallbacks":"default"}`
	prep := prepareWithFallback(t, in, body, translate.EmitOptions{
		TargetModel:    "z-ai/glm-5.3-flash",
		TargetProvider: providers.ProviderAIAND,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "fallbacks").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback")
}
