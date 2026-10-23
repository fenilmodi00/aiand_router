package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExcludeContextOverflowModels_NoOverflowUnderWindow leaves the denylist
// untouched when every model fits.
func TestExcludeContextOverflowModels_NoOverflowUnderWindow(t *testing.T) {
	available := map[string]struct{}{
		"z-ai/glm-5.2":       {},
		"z-ai/glm-5.3-flash": {},
	}

	out, overflowed := excludeContextOverflowModels(10_000, 0, 8_000, nil, nil, available)

	assert.Empty(t, overflowed)
	assert.Nil(t, out, "no additions returns the original (nil) denylist unchanged")
}

// TestExcludeContextOverflowModels_UsesRoutableBinding verifies the first
// available catalog binding supplies the context window.
func TestExcludeContextOverflowModels_UsesRoutableBinding(t *testing.T) {
	available := map[string]struct{}{
		"deepseek/deepseek-v4-pro-0813": {},
	}
	enabledBoth := map[string]struct{}{
		providers.ProviderAIAND: {},
	}
	enabledFireworksOnly := map[string]struct{}{
		providers.ProviderAIAND: {},
	}

	// 546016 + 64K reserve is below Fireworks' 1M served window, so the
	// provider's old Together 512K limit must not exclude the model.
	outBoth, overflowedBoth := excludeContextOverflowModels(546_016, 0, 64_000, enabledBoth, nil, available)
	assert.NotContains(t, overflowedBoth, "deepseek/deepseek-v4-pro-0813")
	assert.NotContains(t, outBoth, "deepseek/deepseek-v4-pro-0813")

	_, overflowedFireworks := excludeContextOverflowModels(546_016, 0, 64_000, enabledFireworksOnly, nil, available)
	assert.NotContains(t, overflowedFireworks, "deepseek/deepseek-v4-pro-0813",
		"Fireworks serves the 1M window")

	// nil enabledProviders retains legacy model-level behavior.
	_, overflowedNil := excludeContextOverflowModels(546_016, 0, 64_000, nil, nil, available)
	assert.NotContains(t, overflowedNil, "deepseek/deepseek-v4-pro-0813",
		"the catalog model-level window is 1M")
}

func TestWithoutModels(t *testing.T) {
	set := map[string]struct{}{"a": {}, "b": {}}
	out := withoutModels(set, []string{"a"})
	assert.Equal(t, map[string]struct{}{"b": {}}, out)
	assert.Len(t, set, 2, "the input set is never mutated")
	assert.Nil(t, withoutModels(nil, []string{"a"}))
}

func TestWithoutModelsKeep_PreservesIndependentExclusions(t *testing.T) {
	set := map[string]struct{}{"gemini-3.1-pro-preview": {}, "z-ai/glm-5.3-flash": {}}
	admitted := []string{"gemini-3.1-pro-preview"}
	unsigned := []string{"gemini-3.1-pro-preview"}
	out := withoutModelsKeep(set, admitted, unsigned)
	assert.Equal(t, map[string]struct{}{"gemini-3.1-pro-preview": {}, "z-ai/glm-5.3-flash": {}}, out,
		"unsigned-history exclusion survives overflow readmission")
	assert.Equal(t, map[string]struct{}{"z-ai/glm-5.3-flash": {}}, withoutModelsKeep(set, admitted, nil),
		"with no keep list, overflow readmission still drops the overflowed model")
}

func TestContextWindowOnlyExclusions_DropsAdmittedAndUnsigned(t *testing.T) {
	overflowed := []string{"deepseek/deepseek-v4.1-flash", "z-ai/glm-5.2", "gemini-3.1-pro-preview"}
	out := contextWindowOnlyExclusions(overflowed, []string{"z-ai/glm-5.2"}, []string{"gemini-3.1-pro-preview"})
	assert.Equal(t, map[string]struct{}{"deepseek/deepseek-v4.1-flash": {}}, out,
		"admitted models already serve and unsigned-history models stay out for their own reason")
	assert.Nil(t, contextWindowOnlyExclusions(nil, nil, nil))
}

func TestIsUpstreamContextOverflow_ProviderShapes(t *testing.T) {
	overflow := func(status int, body string) error {
		return &providers.UpstreamErrorResponse{Status: status, Body: []byte(body)}
	}
	for name, err := range map[string]error{
		"anthropic": overflow(400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`),
		"openai":    overflow(400, `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
		"gemini":    overflow(400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`),
		"vllm":      overflow(400, `{"object":"error","message":"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens."}`),
		"wrapped":   fmt.Errorf("attempt 2: %w", overflow(400, `{"error":{"code":"context_length_exceeded"}}`)),
	} {
		assert.True(t, isUpstreamContextOverflow(err), name)
		assert.True(t, isContextOverflow(err), name)
	}
	for name, err := range map[string]error{
		"rate limit status": overflow(429, `{"error":{"code":"context_length_exceeded"}}`),
		"rate limit body":   overflow(400, `{"error":{"message":"rate limit: too many tokens per minute; maximum context length unaffected"}}`),
		"server error":      overflow(500, `{"error":{"message":"prompt is too long"}}`),
		"other 400":         overflow(400, `{"error":{"message":"tools.0.name: invalid"}}`),
		"not buffered":      errors.New("prompt is too long"),
	} {
		assert.False(t, isUpstreamContextOverflow(err), name)
	}
	assert.True(t, isContextOverflow(fmt.Errorf("router: %w", policy.ErrContextWindowExceeded)))
}

func TestClassifyDispatchError_ContextOverflowIsNative(t *testing.T) {
	for name, err := range map[string]error{
		"router":   fmt.Errorf("wrapped: %w", policy.ErrContextWindowExceeded),
		"upstream": &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)},
	} {
		cls, ok := ClassifyDispatchError(err)
		require.True(t, ok, name)
		assert.Equal(t, DispatchErrorContextWindowExceeded, cls.Kind, name)
		assert.Equal(t, http.StatusBadRequest, cls.Status, name)
		assert.True(t, strings.HasPrefix(cls.Message, "prompt is too long"), "Claude Code and opencode key compaction off this wording")
		assert.True(t, cls.Kind.IsClientError(), name)
		assert.Equal(t, "context_length_exceeded", OpenAIErrorCode(cls.Kind))
	}
	assert.Empty(t, OpenAIErrorCode(DispatchErrorUpstreamStatus))
}

func TestFlushHelpersLeaveOverflowToHandler(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)}
	for name, flush := range map[string]func(http.ResponseWriter, error){
		"buffered":  flushBufferedIfPresent,
		"anthropic": flushUpstreamErrorAsAnthropic,
	} {
		rec := httptest.NewRecorder()
		flush(rec, overflow)
		assert.Zero(t, rec.Body.Len(), "%s: the handler renders the client-native overflow", name)
		assert.False(t, rec.Flushed, name)

		rec = httptest.NewRecorder()
		flush(rec, &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"bad tool"}}`)})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: other upstream errors still flush", name)
		assert.Contains(t, rec.Body.String(), "bad tool")
	}
}

func TestSSEErrorEventsCarryNativeOverflow(t *testing.T) {
	overflow := &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`{"error":{"message":"maximum context length is 131072 tokens"}}`)}

	rec := httptest.NewRecorder()
	_ = emitAnthropicSSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)

	rec = httptest.NewRecorder()
	_ = emitOpenAISSEErrorEvent(rec, overflow)
	assert.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
	assert.Contains(t, rec.Body.String(), `prompt is too long`)
}
