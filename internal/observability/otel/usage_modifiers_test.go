package otel_test

import (
	"testing"

	"aiand-os/router/internal/observability/otel"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes follow recorded first-party Messages responses: the per-TTL split,
// service_tier and inference_geo ride on the full usage object, while
// message_delta repeats only the token totals.
const (
	anthropicSplitBody   = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"z-ai/glm-5.2","stop_reason":"end_turn","usage":{"input_tokens":3,"cache_creation_input_tokens":322,"cache_read_input_tokens":8895,"cache_creation":{"ephemeral_5m_input_tokens":22,"ephemeral_1h_input_tokens":300},"output_tokens":4,"service_tier":"standard","speed":"fast","inference_geo":"us"}}`
	anthropicNoSplitBody = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"z-ai/glm-5.2","stop_reason":"end_turn","usage":{"input_tokens":3,"cache_creation_input_tokens":322,"cache_read_input_tokens":8895,"output_tokens":4}}`
)

func writeAll(t *testing.T, ext *otel.UsageExtractor, chunks ...string) {
	t.Helper()
	for _, chunk := range chunks {
		_, err := ext.Write([]byte(chunk))
		require.NoError(t, err)
	}
}

func TestUsageExtractor_RecordedUsageModifiersFeedTranslatedPaths(t *testing.T) {
	ext := otel.NewUsageExtractor(nil, providers.ProviderAIAND)
	ext.RecordProviderUsage(usage.Facts{Present: true, CacheWrite: usage.Reported(500), CacheRead: usage.Reported(0), CacheWrite1h: usage.Reported(200), Speed: catalog.SpeedFast, SpeedReported: true, InferenceGeo: catalog.InferenceGeoUS, InferenceGeoReported: true})
	ext.RecordProviderUsage(usage.Facts{})

	assert.Equal(t, catalog.UsageModifiers{CacheCreation1h: 200}, ext.UsageModifiers())
	assert.Equal(t, catalog.SpeedFast, ext.Speed())
}

func TestUsageExtractor_NilUsageModifiersAreZero(t *testing.T) {
	var ext *otel.UsageExtractor
	assert.Equal(t, catalog.UsageModifiers{}, ext.UsageModifiers())
	assert.Empty(t, ext.Speed())
	assert.Empty(t, ext.InferenceGeo())
}
