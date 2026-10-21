package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const extendedCtxBody = `{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`

// TestEnableExtendedContext_NoOpWithoutCapability guards that the injection is
// gated on the target model: a 200K-only model (Haiku) must never receive the
// beta, since it cannot honor it.
func TestEnableExtendedContext_NoOpWithoutCapability(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:           "z-ai/glm-5.3-flash",
		EnableExtendedContext: true,
	})
	require.NoError(t, err)
	assert.Empty(t, prep.Headers.Get("anthropic-beta"))
}

// TestEnableExtendedContext_OffLeavesHeaderUntouched confirms the flag is the
// switch: with it unset, no beta is synthesized.
func TestEnableExtendedContext_OffLeavesHeaderUntouched(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(extendedCtxBody))
	require.NoError(t, err)

	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.2"})
	require.NoError(t, err)
	assert.Empty(t, prep.Headers.Get("anthropic-beta"))
}
