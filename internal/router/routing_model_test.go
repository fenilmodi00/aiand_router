package router

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingModelIDsUseVersionedSnapshotNames(t *testing.T) {
	versionedID := regexp.MustCompile(`^aiand-(hmm|qwen)-[0-9]{8}-[0-9]{4}v[0-9]+$`)
	require.Equal(t, ModelAutomatic, RoutingModel("aiand-auto"))
	require.Regexp(t, versionedID, string(ModelHMM))
	require.Regexp(t, versionedID, string(ModelQwen))
	require.NotEqual(t, ModelHMM, RoutingModel("aiand-hmm"))
	require.NotEqual(t, ModelQwen, RoutingModel("aiand-qwen"))
}

func TestAutomaticRoutingModelAcceptsDisplayName(t *testing.T) {
	require.True(t, IsRoutingModel("aiand-auto"))
	require.True(t, IsRoutingModel(ModelAutomaticDisplayName))
	require.True(t, IsRoutingModel("Weave Auto"))
	require.True(t, IsRoutingModel("weave-auto"))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel(ModelAutomaticDisplayName))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel("aiand-auto"))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel("Weave Auto"))
	require.Equal(t, ModelAutomatic, CanonicalRoutingModel("weave-auto"))
	for _, near := range []string{"aiand auto", "AIAND AUTO", "Aiand  Auto", " Aiand Auto", "Aiand Auto ", "Aiand-Auto", "Aiand Router", "weave auto", "WEAVE AUTO"} {
		require.False(t, IsRoutingModel(near), near)
		require.Equal(t, RoutingModel(near), CanonicalRoutingModel(near), near)
	}
}
