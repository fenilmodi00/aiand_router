package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestExcludeAdvisorOutrankingModels_NoAdvisorIsNoop(t *testing.T) {
	available := map[string]struct{}{"z-ai/glm-5.2": {}, "z-ai/glm-5.3": {}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	require.NoError(t, err)

	out, added := excludeAdvisorOutrankingModels(env, nil, available)
	assert.Nil(t, added)
	assert.Nil(t, out)
}
