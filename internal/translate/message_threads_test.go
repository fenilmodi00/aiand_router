package translate_test

import (
	"net/http"
	"testing"

	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

func TestStripMessageThreadsBeta(t *testing.T) {
	h := http.Header{}
	h.Add("anthropic-beta", "claude-code-20250219, message-threads-2026-08-12")
	h.Add("anthropic-beta", "context-1m-2025-08-07")
	translate.StripMessageThreadsBeta(h)
	assert.Equal(t, []string{"claude-code-20250219,context-1m-2025-08-07"}, h.Values("anthropic-beta"))

	only := http.Header{"Anthropic-Beta": {"message-threads-2026-08-12"}}
	translate.StripMessageThreadsBeta(only)
	assert.Empty(t, only.Values("anthropic-beta"))
}
