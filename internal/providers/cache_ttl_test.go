package providers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/providers"
)

func TestCacheTTLFor(t *testing.T) {
	t.Parallel()

	// The OpenAI-compatible upstream caches best-effort on a minutes-scale
	// window; the pin TTL is sized to that.
	assert.Equal(t, 5*time.Minute, providers.CacheTTLFor(providers.ProviderAIAND),
		"ai& should report the short best-effort window")
}
