package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"aiand-os/router/internal/providers"
)

func TestUpstreamIDsForProvider_MiniMax(t *testing.T) {
	ids := upstreamIDsForProvider(providers.ProviderAIAND)

	assert.Equal(t, "zai-org/glm-5.3", ids["z-ai/glm-5.3"])
	assert.Equal(t, "moonshotai/kimi-k3", ids["moonshotai/kimi-k3"])
}
