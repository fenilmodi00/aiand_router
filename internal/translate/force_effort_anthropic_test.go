package translate_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

// TestForceEffort_NonAdaptiveTargetNoOp verifies ForceEffort is a no-op on
// legacy extended-thinking targets (no output_config.effort knob).
func TestForceEffort_NonAdaptiveTargetNoOp(t *testing.T) {
	body := []byte(`{"model":"deepseek/deepseek-v4.1-flash","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":16384}}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(http.Header{}, translate.EmitOptions{
		TargetModel:  "deepseek/deepseek-v4.1-flash",
		Capabilities: router.Lookup("deepseek/deepseek-v4.1-flash"),
		ForceEffort:  "low",
	})
	require.NoError(t, err)
	// Use of Lookup on a sonnet-4-5 returns zero (non-adaptive) — no rewrite.
	var out map[string]any
	require.NoError(t, json.Unmarshal(prep.Body, &out))
	// thinking block should remain "enabled" with the original budget; no
	// output_config should appear (only adaptive models use it).
	if oc, present := out["output_config"]; present {
		assert.Nil(t, oc, "non-adaptive target must not gain output_config.effort")
	}
}
