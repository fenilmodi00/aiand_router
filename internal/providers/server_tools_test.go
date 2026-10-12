package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"
)

func TestSupportsAnthropicServerTools(t *testing.T) {
	cases := map[string]bool{
		providers.ProviderAnthropic: true,
		providers.ProviderAIAND:     false,
		// A provider absent from ProviderFamilies (the cut gateway) gets no family,
		// so it cannot be credited with native server-tool support.
		"anthropic_gateway": false,
	}
	for provider, want := range cases {
		if got := providers.SupportsAnthropicServerTools(provider); got != want {
			t.Errorf("SupportsAnthropicServerTools(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestIsGatewayFalseForEveryKnownProvider(t *testing.T) {
	for _, provider := range providers.AllProviders() {
		if providers.IsGateway(provider) {
			t.Errorf("IsGateway(%q) = true; the AIand-only build has no gateway surface", provider)
		}
	}
}
