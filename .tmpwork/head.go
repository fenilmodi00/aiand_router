package cluster

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

// buildCentroidsBlob produces centroids.bin bytes from in-memory data,
// bypassing the committed placeholder.
func buildCentroidsBlob(t *testing.T, k, dim int, data []float32) []byte {
	t.Helper()
	require.Len(t, data, k*dim, "data must be k*dim long")
	var b bytes.Buffer
	b.WriteString(centroidsMagic)
	require.NoError(t, binary.Write(&b, binary.LittleEndian, centroidsVersion))
	require.NoError(t, binary.Write(&b, binary.LittleEndian, uint32(k)))
	require.NoError(t, binary.Write(&b, binary.LittleEndian, uint32(dim)))
	for _, v := range data {
		require.NoError(t, binary.Write(&b, binary.LittleEndian, math.Float32bits(v)))
	}
	return b.Bytes()
}

func TestLoadCentroids_Roundtrip(t *testing.T) {
	data := []float32{1, 2, 3, 4, 5, 6, 7, 8}
	dim := EmbedDim
	k := 2
	full := make([]float32, k*dim)
	copy(full, data)
	blob := buildCentroidsBlob(t, k, dim, full)

	got, err := loadCentroids(blob)
	require.NoError(t, err)
	assert.Equal(t, k, got.K)
	assert.Equal(t, dim, got.Dim)
	assert.InDeltaSlice(t, full, got.Data, 1e-6)
	assert.InDeltaSlice(t, full[:dim], got.Row(0), 1e-6)
}

func TestLoadCentroids_BadMagic(t *testing.T) {
	_, err := loadCentroids([]byte("XXXX\x01\x00\x00\x00\x01\x00\x00\x00\x00\x03\x00\x00"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad magic")
}

func TestLoadCentroids_TooShort(t *testing.T) {
	_, err := loadCentroids([]byte("CRT1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestLoadCentroids_ArbitraryDimAccepted(t *testing.T) {
	// Dim is per-bundle now; loadCentroids accepts any non-zero dim and
	// validateDeclaredDim cross-checks it against metadata.
	dim := 1024
	blob := buildCentroidsBlob(t, 1, dim, make([]float32, dim))
	got, err := loadCentroids(blob)
	require.NoError(t, err)
	assert.Equal(t, dim, got.Dim)
}

func TestValidateDeclaredDim(t *testing.T) {
	t.Run("legacy bundle without metadata must match Jina default", func(t *testing.T) {
		c := &Centroids{K: 1, Dim: EmbedDim}
		require.NoError(t, validateDeclaredDim("v-test", c, nil))

		bad := &Centroids{K: 1, Dim: 1024}
		err := validateDeclaredDim("v-test", bad, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "embedder mismatch")
	})

	t.Run("declared dim must match centroids header", func(t *testing.T) {
		meta := &ArtifactMetadata{Embedder: ArtifactEmbedder{Model: EmbedderQwen3, EmbedDim: 1024}}
		require.NoError(t, validateDeclaredDim("v-test", &Centroids{K: 1, Dim: 1024}, meta))

		err := validateDeclaredDim("v-test", &Centroids{K: 1, Dim: EmbedDim}, meta)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "embedder mismatch")
	})
}

func TestBundleEmbedderDefaults(t *testing.T) {
	t.Run("nil metadata defaults to Jina", func(t *testing.T) {
		b := &Bundle{}
		assert.Equal(t, EmbedderJinaV2, b.EmbedderID())
		assert.Equal(t, EmbedDim, b.EmbedDim())
	})

	t.Run("metadata embedder block wins", func(t *testing.T) {
		b := &Bundle{Metadata: &ArtifactMetadata{
			Embedder: ArtifactEmbedder{Model: EmbedderQwen3, EmbedDim: 1024},
		}}
		assert.Equal(t, EmbedderQwen3, b.EmbedderID())
		assert.Equal(t, 1024, b.EmbedDim())
	})
}

func TestLoadCentroids_ZeroK(t *testing.T) {
	blob := buildCentroidsBlob(t, 0, EmbedDim, []float32{})
	_, err := loadCentroids(blob)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "K=0")
}

func TestLoadCentroids_SizeMismatch(t *testing.T) {
	// Header K=2 but only 1 centroid of data follows.
	var b bytes.Buffer
	b.WriteString(centroidsMagic)
	binary.Write(&b, binary.LittleEndian, centroidsVersion)
	binary.Write(&b, binary.LittleEndian, uint32(2))
	binary.Write(&b, binary.LittleEndian, uint32(EmbedDim))
	for i := 0; i < EmbedDim; i++ {
		binary.Write(&b, binary.LittleEndian, math.Float32bits(0))
	}
	_, err := loadCentroids(b.Bytes())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size")
}

func TestLoadRankings_Roundtrip(t *testing.T) {
	raw := []byte(`{
		"aiand": {"router_version": "weave-router-v0.1-bootstrap"},
		"rankings": {
			"0": {"moonshotai/kimi-k3": 0.8, "z-ai/glm-5.2": 0.5},
			"1": {"moonshotai/kimi-k3": 0.3, "z-ai/glm-5.2": 0.6}
		}
	}`)
	got, err := loadRankings(raw)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.InDelta(t, 0.8, got[0]["moonshotai/kimi-k3"], 1e-6)
	assert.InDelta(t, 0.6, got[1]["z-ai/glm-5.2"], 1e-6)
}

func TestLoadRankings_NonIntegerKey(t *testing.T) {
	raw := []byte(`{"rankings": {"oops": {"m": 0.5}}}`)
	_, err := loadRankings(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-integer")
}

func TestLoadRankings_Empty(t *testing.T) {
	_, err := loadRankings([]byte(`{"rankings": {}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no clusters")
}

func TestLoadRankings_EmptyClusterRow(t *testing.T) {
	_, err := loadRankings([]byte(`{"rankings": {"0": {}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no models")
}

func TestLoadRegistry_Roundtrip(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"deployed_models": []any{
			map[string]any{"model": "moonshotai/kimi-k3", "provider": "aiand", "bench_column": "z-ai/glm-5.2", "proxy": true},
			map[string]any{"model": "z-ai/glm-5.3-flash", "provider": "aiand", "bench_column": "z-ai/glm-5.3-flash"},
		},
	})
	got, err := loadRegistry(raw)
	require.NoError(t, err)
	require.Len(t, got.DeployedModels, 2)
	assert.Equal(t, "aiand", got.DeployedModels[0].Provider)
	assert.Equal(t, "z-ai/glm-5.2", got.DeployedModels[0].BenchColumn)
	assert.True(t, got.DeployedModels[0].Proxy)
	assert.Equal(t, "aiand", got.DeployedModels[1].Provider)
	assert.ElementsMatch(t, []string{"moonshotai/kimi-k3", "z-ai/glm-5.3-flash"}, got.Models())
}

func TestLoadRegistry_EmptyMapping(t *testing.T) {
	_, err := loadRegistry([]byte(`{"deployed_models": []}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deployed_models is empty")
}

func TestLoadRegistry_MissingProvider(t *testing.T) {
	raw := []byte(`{"deployed_models": [{"model": "x", "bench_column": "x"}]}`)
	_, err := loadRegistry(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing provider")
}

func TestLoadRegistry_MissingBenchColumn(t *testing.T) {
	raw := []byte(`{"deployed_models": [{"model": "x", "provider": "aiand"}]}`)
	_, err := loadRegistry(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing bench_column")
}

// Guards against shipping one bad bundle that boots production with no
// cluster scorer at all.
func TestEmbeddedArtifacts_AllVersionsLoadable(t *testing.T) {
	versions, err := ListVersions()
	require.NoError(t, err)
	require.NotEmpty(t, versions, "expected at least one version directory under artifacts/")
	for _, v := range versions {
		v := v
		t.Run(v, func(t *testing.T) {
			bundle, err := LoadBundle(v)
			require.NoError(t, err, "committed bundle %s must parse end-to-end", v)
			assert.Equal(t, v, bundle.Version)
			assert.NotNil(t, bundle.Centroids)
			if bundle.IsV2 {
				assert.NotEmpty(t, bundle.QualityMeans)
			} else {
				assert.NotEmpty(t, bundle.Rankings)
			}
			assert.NotNil(t, bundle.Registry)
			assert.NotEmpty(t, bundle.Registry.DeployedModels)
		})
	}
}

// Enforces one deployed model per family on the latest bundle only; frozen
// historical bundles intentionally retain predecessors (BYOK, /force-model).
func TestLatestBundle_OneDeployedModelPerFamily(t *testing.T) {
	version, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	bundle, err := LoadBundle(version)
	require.NoError(t, err)

	dups := catalog.FamilyDuplicates(bundle.Registry.Models())
	if len(dups) == 0 {
		return
	}
	msg := fmt.Sprintf("latest bundle %s deploys more than one model per family — see docs/plans/ROUTER_MODEL_LIFECYCLE.md and the add-router-model skill for how to retire a superseded model from deployed_models:", version)
	for _, d := range dups {
		msg += "\n  - " + d.String()
	}
	t.Error(msg)
}

// Catches a typo'd latest pointer.
func TestResolveVersion_Latest(t *testing.T) {
	resolved, err := ResolveVersion(LatestVersion)
	require.NoError(t, err)
	assert.NotEmpty(t, resolved)
	versions, err := ListVersions()
	require.NoError(t, err)
	assert.Contains(t, versions, resolved, "latest pointer must name a committed version directory")
}

func TestResolveVersion_UnknownErrors(t *testing.T) {
	_, err := ResolveVersion("v99.99")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "v99.99")
}

// ListVersions must surface bundles in artifacts/ AND artifacts/legacy/,
// flattened into the same list, without leaking the "legacy" pseudo-name.
func TestListVersions_FlattensLegacyAndOmitsPseudoName(t *testing.T) {
	versions, err := ListVersions()
	require.NoError(t, err)
	require.NotEmpty(t, versions)
	for _, v := range versions {
		assert.NotEqual(t, "legacy", v, "the legacy subdirectory must not appear as a version")
	}
	// Sanity: at least one known legacy version is reachable.
	assert.Contains(t, versions, "v0.21", "legacy v0.21 must remain reachable after the move")
}

// bundleDirForVersion must resolve legacy bundles transparently.
func TestResolveVersion_LegacyBundleIsReachable(t *testing.T) {
	resolved, err := ResolveVersion("v0.21")
	require.NoError(t, err)
	assert.Equal(t, "v0.21", resolved)
	bundle, err := LoadBundle(resolved)
	require.NoError(t, err)
	assert.False(t, bundle.IsV2, "v0.21 is v1 format")
}

