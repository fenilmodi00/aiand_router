package otel_test

import (
	"testing"

	"aiand-os/router/internal/observability/otel"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
)

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
