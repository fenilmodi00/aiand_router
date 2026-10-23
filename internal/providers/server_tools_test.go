package providers_test

import (
	"testing"

	"aiand-os/router/internal/providers"
)

// TestEveryProviderIsOpenAICompat asserts every wired provider speaks the
// OpenAI Chat Completions wire format — the only family the router dispatches
// to. A provider in another family would have no emitter and 502 at request
// time.
func TestEveryProviderIsOpenAICompat(t *testing.T) {
	for _, provider := range providers.AllProviders() {
		if !providers.IsOpenAICompat(provider) {
			t.Errorf("provider %q is not OpenAI-compatible; no emitter serves it", provider)
		}
	}
}
