package proxy

import (
	"context"

	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/sessionpin"
)

// reasonCyberRefusalRepin is the pin reason recorded when a safety refusal
// moves the session off the refusing model.
const reasonCyberRefusalRepin = "cyber-refusal-repin"

// refusalFallback resolves the model/provider a safety refusal moves off to:
// the scorer's runner-up carried on the session pin, else the configured
// fallback model. context.Background() reads the pin because the request ctx
// may already be canceled once the response has been written.
func (s *Service) refusalFallback(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	served router.Decision,
) (model, provider string, ok bool) {
	model = s.ResolveCyberRefusalFallbackModel(ctx)
	if s.pinStore != nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		if existing, found, err := s.pinStore.Get(context.Background(), sessionKey, role); err == nil && found &&
			pinMatchesEffectiveStrategy(ctx, existing) && existing.PairedModel != "" {
			model, provider = existing.PairedModel, existing.PairedProvider
		}
	}
	provider = providerForModel(provider, model)
	if model == "" || provider == "" || model == served.Model {
		return "", "", false
	}
	return model, provider, true
}

// providerForModel keeps a pin's own provider and otherwise reads the model's
// first catalog binding.
func providerForModel(provider, model string) string {
	if provider != "" {
		return provider
	}
	if m, known := catalog.ByID(model); known && len(m.Providers) > 0 {
		return m.Providers[0].Provider
	}
	return ""
}
