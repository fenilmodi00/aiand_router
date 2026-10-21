package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/translate"
)

func set(ids ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func TestRouterModelsMessage_UsesTheClientsOwnSigilInTheFooter(t *testing.T) {
	codex := routerModelsMessage(set("z-ai/glm-5.2"), nil, nil, "$", translate.FormatOpenAI)
	assert.Contains(t, codex, "$router-models enable")
	assert.NotContains(t, codex, "/router-models enable")

	claude := routerModelsMessage(set("z-ai/glm-5.2"), nil, nil, "/", translate.FormatAnthropic)
	assert.Contains(t, claude, "/router-models enable")
	assert.Contains(t, claude, routingMarkerPrefix)
}

func TestRouterModelsMessage_EmptyUniverseSaysSo(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatOpenAI, translate.FormatAnthropic} {
		msg := routerModelsMessage(nil, nil, nil, "$", format)
		assert.Contains(t, msg, "no routable models")
		assert.NotContains(t, msg, "[x]")
	}
}

// Grouping is by the model's primary catalog binding, and both the group
// order and the ids inside a group are sorted so the listing is stable
// between turns rather than reordering with Go's map iteration.
func TestRouterModelsMessage_IsDeterministicallyOrdered(t *testing.T) {
	models := set("z-ai/glm-5.2", "z-ai/glm-5.3-flash", "gpt-5.5", "gemini-3-pro-preview")
	first := routerModelsMessage(models, nil, nil, "$", translate.FormatOpenAI)
	for range 8 {
		assert.Equal(t, first, routerModelsMessage(models, nil, nil, "$", translate.FormatOpenAI))
	}
}

// The legend is noise on the overwhelmingly common listing, so it only
// appears when something is actually in that state.
func TestRouterModelsMessage_NoAutomaticExclusionsMeansNoLegend(t *testing.T) {
	msg := routerModelsMessage(set("z-ai/glm-5.2"), nil, nil, "$", translate.FormatOpenAI)
	assert.NotContains(t, msg, "[-]")
	assert.NotContains(t, msg, "still serves it")
}
