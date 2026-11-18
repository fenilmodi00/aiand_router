package proxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
)

var classifierForwardingBody = `{"model":"` + catalog.ModelIDDeepSeekV41Flash.String() + `","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"abc"}`

// A classifier turn whose target model is the model it asked for goes upstream
// byte-for-byte, prompt_cache_key included: the classifier's own machinery
// depends on the exact body it sent.
func TestPrepareOpenAIForTurn_ClassifierSameModelIsVerbatim(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(classifierForwardingBody))
	require.NoError(t, err)

	prep, err := prepareOpenAIForTurn(env, nil, translate.EmitOptions{
		TargetModel: catalog.ModelIDDeepSeekV41Flash.String(),
	}, turntype.Classifier)
	require.NoError(t, err)

	assert.Equal(t, classifierForwardingBody, string(prep.Body))
	assert.Empty(t, prep.Headers)
}

// A classifier turn that would have to be rewritten is emitted normally: the
// verbatim path never silently ignores the target model.
func TestPrepareOpenAIForTurn_ClassifierRewrittenModelIsPrepared(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(classifierForwardingBody))
	require.NoError(t, err)

	prep, err := prepareOpenAIForTurn(env, nil, translate.EmitOptions{
		TargetModel: catalog.ModelIDDeepSeekV4Pro.String(),
	}, turntype.Classifier)
	require.NoError(t, err)

	assert.NotEqual(t, classifierForwardingBody, string(prep.Body))
	assert.Contains(t, string(prep.Body), `"model":"`+catalog.ModelIDDeepSeekV4Pro.String()+`"`)
}

// When the gateway rejected prompt_cache_key and the turn must strip it, a
// classifier body that carries the field cannot go verbatim: the strip has to
// happen even on classifier retries.
func TestPrepareOpenAIForTurn_ClassifierStrippedCacheKeyIsPrepared(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(classifierForwardingBody))
	require.NoError(t, err)

	prep, err := prepareOpenAIForTurn(env, nil, translate.EmitOptions{
		TargetModel:         catalog.ModelIDDeepSeekV41Flash.String(),
		StripPromptCacheKey: true,
	}, turntype.Classifier)
	require.NoError(t, err)

	assert.NotEqual(t, classifierForwardingBody, string(prep.Body),
		"the prepared path still runs, so the emitter's defaults are applied")
	assert.Contains(t, string(prep.Body), `"max_tokens"`)
}

// Non-classifier turns keep the prepared path even when the body would qualify
// for verbatim.
func TestPrepareOpenAIForTurn_NonClassifierIsPrepared(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(classifierForwardingBody))
	require.NoError(t, err)

	prep, err := prepareOpenAIForTurn(env, nil, translate.EmitOptions{
		TargetModel:         catalog.ModelIDDeepSeekV41Flash.String(),
		StripPromptCacheKey: true,
	}, turntype.MainLoop)
	require.NoError(t, err)

	assert.NotEqual(t, classifierForwardingBody, string(prep.Body))
	assert.Contains(t, string(prep.Body), `"max_tokens"`)
}

// A classifier turn on the Anthropic ingress forwards the client's betas and
// body untouched; the prepared path filters thinking betas.
func TestPrepareAnthropicForTurn_ClassifierKeepsClientBetas(t *testing.T) {
	const beta = "interleaved-thinking-2025-05-14,context-management-2025-06-27"
	body := `{"model":"` + catalog.ModelIDDeepSeekV41Flash.String() + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	in := http.Header{}
	in.Set("anthropic-version", "2023-06-01")
	in.Set("anthropic-beta", beta)

	prep, err := prepareAnthropicForTurn(env, in, translate.EmitOptions{
		TargetModel: catalog.ModelIDDeepSeekV41Flash.String(),
	}, turntype.Classifier)
	require.NoError(t, err)

	assert.Equal(t, body, string(prep.Body))
	assert.Equal(t, beta, prep.Headers.Get("anthropic-beta"))
	assert.Equal(t, "2023-06-01", prep.Headers.Get("anthropic-version"))

	prepared, err := prepareAnthropicForTurn(env, in, translate.EmitOptions{
		TargetModel: catalog.ModelIDDeepSeekV41Flash.String(),
	}, turntype.MainLoop)
	require.NoError(t, err)
	assert.NotEqual(t, beta, prepared.Headers.Get("anthropic-beta"),
		"the prepared path filters thinking betas")
}

// A classifier turn that would need an Anthropic rewrite is emitted normally.
func TestPrepareAnthropicForTurn_ClassifierRewrittenModelIsPrepared(t *testing.T) {
	body := `{"model":"` + catalog.ModelIDDeepSeekV41Flash.String() + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)

	prep, err := prepareAnthropicForTurn(env, nil, translate.EmitOptions{
		TargetModel: catalog.ModelIDDeepSeekV4Pro.String(),
	}, turntype.Classifier)
	require.NoError(t, err)

	assert.Contains(t, string(prep.Body), `"model":"`+catalog.ModelIDDeepSeekV4Pro.String()+`"`)
}

func TestForcePinBindsNonClassifierTurn(t *testing.T) {
	assert.True(t, forcePinBindsNonClassifierTurn(true, turntype.MainLoop))
	assert.False(t, forcePinBindsNonClassifierTurn(true, turntype.Classifier))
	assert.False(t, forcePinBindsNonClassifierTurn(false, turntype.MainLoop))
}
