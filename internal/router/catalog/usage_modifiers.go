package catalog

import (
	"weave-os/router/internal/usage"
)

// InferenceGeo is the inference geography a provider reports it served a call
// from (Anthropic usage.inference_geo).
type InferenceGeo = usage.InferenceGeo

// InferenceGeoUS is Anthropic's US-only inference geography.
const InferenceGeoUS = usage.InferenceGeoUS

// Speed is the serving speed a provider reports it billed a call at
// (Anthropic usage.speed).
type Speed = usage.Speed

// SpeedFast is Anthropic's fast-mode serving speed.
const SpeedFast = usage.SpeedFast

// UsageModifiers are provider-reported attributes of a call that change its
// per-token rates without changing its token counts. The zero value prices
// every cache write at the 5-minute rate.
type UsageModifiers struct {
	// CacheCreation1h is the share of the call's cache-creation tokens
	// written with the 1-hour TTL.
	CacheCreation1h int
}

// EffectiveCacheWrite1hMultiplier returns the 1-hour cache-write multiplier.
// The only upstream family is OpenAI-compatible, which does not distinguish
// write TTLs, so the 5-minute rate applies.
func (p Pricing) EffectiveCacheWrite1hMultiplier() float64 {
	return p.EffectiveCacheWriteMultiplier()
}
