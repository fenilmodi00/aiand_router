package router

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingModelIDsUseVersionedSnapshotNames(t *testing.T) {
	versionedID := regexp.MustCompile(`^aiand-(hmm|qwen)-[0-9]{8}-[0-9]{4}v[0-9]+$`)
	require.Equal(t, RoutingModel("aiand-auto"), ModelAutomatic)
	require.Regexp(t, versionedID, string(ModelHMM))
	require.Regexp(t, versionedID, string(ModelQwen))
	require.NotEqual(t, RoutingModel("aiand-hmm"), ModelHMM)
	require.NotEqual(t, RoutingModel("aiand-qwen"), ModelQwen)
}
