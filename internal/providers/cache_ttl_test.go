package providers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestCacheTTLFor(t *testing.T) {
	t.Parallel()

	// The OpenAI-compatible upstream caches best-effort on a minutes-scale
	// window; the pin TTL is sized to that.
	assert.Equal(t, 5*time.Minute, providers.CacheTTLFor(providers.ProviderAIAND),
		"ai& should report the short best-effort window")

	// Unknown providers fall back to the conservative default.
	assert.Equal(t, providers.DefaultCacheTTL, providers.CacheTTLFor("nonexistent-provider"),
		"unknown provider should fall back to the default TTL")
}
