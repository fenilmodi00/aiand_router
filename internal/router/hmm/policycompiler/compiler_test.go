package policycompiler_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router/hmm/policycompiler"
	"aiand-os/router/internal/router/hmm/rosterdata"
)

func TestCompileLegacyRosterPreservesWIIDescription(t *testing.T) {
	source := []byte(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "wii_v1": "six-benchmark absolute WII with frozen normalization",
    "alpha": {"low": 0.4},
    "alpha_min": {"low": 0.05},
    "alpha_max": {"low": 0.8},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["z-ai/glm-5.3"],
      "arms_by_harness": {"codex": ["z-ai/glm-5.3"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"z-ai/glm-5.3": 10},
      "arm_indices": {"z-ai/glm-5.3": {"wii_v1": 50, "wpi_v1": 10}}
    }
  },
  "manual_pins": {"pi": {"low": ["z-ai/glm-5.3"]}}
}`)

	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: []string{"low"},
	})
	require.NoError(t, err)
	assert.Equal(t, rosterdata.SchemaVersionPolicyV1, policy.SchemaVersion)
	assert.Equal(t, "six-benchmark absolute WII with frozen normalization", policy.Ranking.WIIDescription)
	assert.Equal(t, []string{"z-ai/glm-5.3"}, policy.Clusters["low"].ManualPinsByHarness[rosterdata.HarnessPI])
	assert.Contains(t, string(canonical), `"wii_v1":"six-benchmark absolute WII with frozen normalization"`)
}

func TestCompileMapsPooledPinsToAllHarness(t *testing.T) {
	source := []byte(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "alpha": {"low": 0.4},
    "alpha_min": {"low": 0.05},
    "alpha_max": {"low": 0.8},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["z-ai/glm-5.3"],
      "arms_by_harness": {"codex": ["z-ai/glm-5.3"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"z-ai/glm-5.3": 10},
      "arm_indices": {"z-ai/glm-5.3": {"wii_v1": 50, "wpi_v1": 10}}
    }
  },
  "manual_pins": {"pooled": {"low": ["moonshotai/kimi-k3"]}}
}`)

	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: []string{"low"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"moonshotai/kimi-k3"}, policy.Clusters["low"].ManualPinsByHarness[rosterdata.HarnessAll])
	assert.NotContains(t, string(canonical), "pooled")
}

func TestCompileKeepsOpenCodeHarnessPolicy(t *testing.T) {
	source := []byte(`{
  "schema_version": "hmm_router_cluster_roster_v7",
  "ranking": {
    "alpha": {"low": 0.4},
    "alpha_min": {"low": 0.05},
    "alpha_max": {"low": 0.8},
    "quality_bias_neutral": 0.7,
    "wii_score_version": "wii-v1",
    "wii_normalization_sha256": "wii-sha",
    "wpi_score_version": "wpi-v1",
    "wpi_normalization_sha256": "wpi-sha"
  },
  "harness_vendor_priority": {"opencode": {"vendors": ["openai"], "clusters": ["low"]}},
  "clusters": {
    "low": {
      "complexity_label": "low",
      "arms": ["z-ai/glm-5.3", "moonshotai/kimi-k3"],
      "arms_by_harness": {"opencode": ["moonshotai/kimi-k3", "z-ai/glm-5.3"]},
      "cost_ref_usd": 0.02,
      "latency_ref_ms": 8000,
      "arm_scores": {"z-ai/glm-5.3": 10, "moonshotai/kimi-k3": 12},
      "arm_indices": {"z-ai/glm-5.3": {"wii_v1": 50, "wpi_v1": 10}, "moonshotai/kimi-k3": {"wii_v1": 55, "wpi_v1": 15}}
    }
  },
  "manual_pins": {"opencode": {"low": ["moonshotai/kimi-k3"]}}
}`)

	_, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: []string{"low"},
	})
	require.NoError(t, err)
	low := policy.Clusters["low"]
	assert.Equal(t, []string{"moonshotai/kimi-k3", "z-ai/glm-5.3"}, low.ArmsByHarness[rosterdata.HarnessOpenCode])
	assert.Equal(t, []string{"moonshotai/kimi-k3"}, low.ManualPinsByHarness[rosterdata.HarnessOpenCode])
	assert.Equal(t, []string{"openai"}, low.PreferredVendorsByHarness[rosterdata.HarnessOpenCode])
}
