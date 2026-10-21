package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestExcludeAdvisorOutrankingModels_ExcludesModelsTheAdvisorCannotAdvise(t *testing.T) {
	available := map[string]struct{}{
		"z-ai/glm-5.2":                 {},
		"z-ai/glm-5.3":                 {},
		"deepseek/deepseek-v4-pro":     {},
		"deepseek/deepseek-v4.1-flash": {},
		"moonshotai/kimi-k3":           {},
	}
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"type":"advisor_20260301","name":"advisor","model":"z-ai/glm-5.2-20260801"}]}`))
	require.NoError(t, err)

	out, modelsAdvisorCannotAdvise := excludeAdvisorOutrankingModels(env, map[string]struct{}{"z-ai/glm-5.3": {}}, available)
	assert.Equal(t, []string{"z-ai/glm-5.3", "z-ai/glm-5.2", "deepseek/deepseek-v4.1-flash"}, modelsAdvisorCannotAdvise,
		"already-excluded and unranked models are reported so later re-admission keeps them out")
	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3": {}, "z-ai/glm-5.2": {}, "deepseek/deepseek-v4.1-flash": {}}, out)
}

func TestExcludeAdvisorOutrankingModels_NoAdvisorIsNoop(t *testing.T) {
	available := map[string]struct{}{"z-ai/glm-5.2": {}, "z-ai/glm-5.3": {}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`))
	require.NoError(t, err)

	out, added := excludeAdvisorOutrankingModels(env, nil, available)
	assert.Nil(t, added)
	assert.Nil(t, out)
}

func TestSafetyExcludedModelsIncludesAdvisorOutranking(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"z-ai/glm-5.2": {}, "z-ai/glm-5.3": {}}}
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"advisor_20260301","name":"advisor","model":"z-ai/glm-5.2"}]}`))
	require.NoError(t, err)

	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3": {}}, s.safetyExcludedModels(env, 0, nil))
}

func TestAdvisorRejectsModel(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"advisor_20260301","name":"advisor","model":"z-ai/glm-5.2"}]}`))
	require.NoError(t, err)
	assert.True(t, advisorRejectsModel(env, "claude-fable-5-1"))
	assert.True(t, advisorRejectsModel(env, "claude-sonnet-4-5"), "unranked Claude models take no advisor")
	assert.False(t, advisorRejectsModel(env, "claude-opus-5"))
	assert.False(t, advisorRejectsModel(env, "gpt-5.5"))
}
