//go:build smoke

package smoke

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

// aiandRoster mirrors the AIand cluster registry shared by every
// bundle in internal/router/cluster/artifacts (the served version is
// artifacts/latest): the complete set of models an auto-model request
// may be routed to on an AIand-only deployment. The registry — not the
// BYOK credential — gates the candidate pool
// (docs/CONFIGURATION.md, "AIand-only deployment"): a vendor key for a
// provider outside the roster cannot widen it.
var aiandRoster = []string{
	"deepseek-ai/deepseek-v4-pro",
	"deepseek-ai/deepseek-v4-flash",
	"deepseek-ai/deepseek-v4.1-flash",
	"zai-org/glm-5.3",
	"zai-org/glm-5.3-flash",
	"qwen/qwen3.8-27b",
	"moonshotai/kimi-k3",
	"motif-technologies/motif-3",
}

// TestAIandRoster pins the AIand-only deployment invariants on the
// request path: an auto-model request is served by an AIand roster
// model through the AIand decision provider, and a vendor BYOK key
// does not widen the roster.
func TestAIandRoster(t *testing.T) {
	t.Run("auto-model request routes to an AIand roster model", func(t *testing.T) {
		body := newRequest("smoke-aiand-auto").tokens(64).
			text("Reply with exactly the word: ok").build(t)

		// Empty x-weave-force-model: the router treats it as absent
		// and the cluster scorer picks the model.
		r := callModel(t, body, "")

		requireOKMessage(t, r)
		assertServedByAIandRoster(t, r)
	})

	t.Run("vendor BYOK key does not widen the roster", func(t *testing.T) {
		body := newRequest("smoke-aiand-byok").tokens(64).
			text("Reply with exactly the word: ok").build(t)

		// A vendor key for a provider whose models are not in the
		// registry. X-Weave-Router-Key authenticates the client
		// to the router; Authorization is preserved verbatim for the
		// upstream. The decision must still come from the AIand
		// roster, not from the credential's provider.
		r := callModelWithHeaders(t, body, "", map[string]string{
			"X-Weave-Router-Key": "Bearer " + cfg.RouterKey,
			"Authorization":      "Bearer sk-ant-vendor-key",
		})

		requireOKMessage(t, r)
		assertServedByAIandRoster(t, r)
	})
}

// assertServedByAIandRoster checks the decision headers name an
// AIand roster model served by the AIand provider.
func assertServedByAIandRoster(t *testing.T, r response) {
	t.Helper()
	gotModel := r.headers.Get(headerRouterModel)
	if gotModel == "" {
		t.Errorf("missing %s header", headerRouterModel)
	} else {
		assert.Contains(t, aiandRoster, gotModel,
			"auto-model decision must be an AIand roster model")
	}
	if gotProvider := r.headers.Get(headerRouterProvider); gotProvider == "" {
		t.Errorf("missing %s header", headerRouterProvider)
	} else {
		assert.Equal(t, providers.ProviderAIAND, gotProvider,
			"decision provider must be AIand on an AIand-only deployment")
	}
}
