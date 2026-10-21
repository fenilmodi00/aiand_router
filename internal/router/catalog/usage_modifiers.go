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
// every cache write at the 5-minute rate with global inference.
type UsageModifiers struct {
	// CacheCreation1h is the share of the call's cache-creation tokens
	// written with the 1-hour TTL.
	CacheCreation1h int
	InferenceGeo    InferenceGeo
}

// EffectiveCacheWrite1hMultiplier returns the 1-hour cache-write multiplier.
// The only upstream family is OpenAI-compatible, which does not distinguish
// write TTLs, so the 5-minute rate applies.
func (p Pricing) EffectiveCacheWrite1hMultiplier(upstreamProvider string) float64 {
	return p.EffectiveCacheWriteMultiplier()
}

// EffectiveUSInferenceGeoMultiplier returns the binding's US-inference
// multiplier, or 1 when it charges no geography premium.
func (p Pricing) EffectiveUSInferenceGeoMultiplier() float64 {
	if p.USInferenceGeoMultiplier > 0 {
		return p.USInferenceGeoMultiplier
	}
	return 1
}

// WithoutInferenceGeoPremium returns p for a binding that borrows a
// first-party list price (a custom endpoint, or an unbound provider): only
// first-party Anthropic charges the US-inference premium.
func (p Pricing) WithoutInferenceGeoPremium() Pricing {
	p.USInferenceGeoMultiplier = 0
	return p
}

func (p Pricing) inferenceGeoMultiplier(geo InferenceGeo) float64 {
	if geo == InferenceGeoUS {
		return p.EffectiveUSInferenceGeoMultiplier()
	}
	return 1
}
