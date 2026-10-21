package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"
)

func TestSupportsAnthropicServerTools(t *testing.T) {
	cases := map[string]bool{
		providers.ProviderAIAND:        true,
		providers.ProviderAIAND: false,
		providers.ProviderAIAND:           false,
		providers.ProviderAIAND:          false,
	}
	for provider, want := range cases {
		if got := providers.SupportsAnthropicServerTools(provider); got != want {
			t.Errorf("SupportsAnthropicServerTools(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestAnthropicGatewayShareFamilyButNotServerTools(t *testing.T) {
	if providers.FamilyFor(providers.ProviderAIAND) != providers.FamilyFor(providers.ProviderAIAND) {
		t.Fatal("the gateway must stay Anthropic-family: it speaks the Messages wire format")
	}
	if providers.SupportsAnthropicServerTools(providers.ProviderAIAND) {
		t.Fatal("wire-format compatibility must not imply server-tool support")
	}
}
