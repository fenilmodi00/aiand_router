package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextWindowForRequest_ExtendedContextModelsReport1M is the premise for
// the overflow filter: a 1M roster model advertises its full catalog window,
// while a 262K roster model reports the smaller window it serves.
func TestContextWindowForRequest_ExtendedContextModelsReport1M(t *testing.T) {
	assert.Equal(t, 1_048_576, contextWindowForRequest("zai-org/glm-5.3"))
	assert.Equal(t, 1_048_576, contextWindowForRequest("deepseek-ai/deepseek-v4.1-flash"))
	assert.Equal(t, 262_144, contextWindowForRequest("qwen/qwen3.8-27b"))
}

// TestExcludeContextOverflowModels_KeepsExtendedContextModel is the regression
// for the debug-session bug: a ~250K-token first request was dispatched to a
// model at a window it could not serve and 400'd immediately. A 1M roster
// model must survive the pre-filter while a true 262K-only model is excluded.
func TestExcludeContextOverflowModels_KeepsExtendedContextModel(t *testing.T) {
	available := map[string]struct{}{
		"zai-org/glm-5.3":  {},
		"qwen/qwen3.8-27b": {},
	}

	out, overflowed := excludeContextOverflowModels(260_000, 0, 8_000, nil, nil, available)

	assert.Contains(t, overflowed, "qwen/qwen3.8-27b", "262K-only model overflows a 268K request")
	assert.NotContains(t, overflowed, "zai-org/glm-5.3", "1M model fits and must stay eligible")
	_, widestExcluded := out["zai-org/glm-5.3"]
	assert.False(t, widestExcluded, "the 1M model must not be added to the denylist")
}

// TestExcludeContextOverflowModels_NoOverflowUnderWindow leaves the denylist
// untouched when every model fits.
func TestExcludeContextOverflowModels_NoOverflowUnderWindow(t *testing.T) {
	available := map[string]struct{}{
		"zai-org/glm-5.3":       {},
		"zai-org/glm-5.3-flash": {},
	}

	out, overflowed := excludeContextOverflowModels(10_000, 0, 8_000, nil, nil, available)

	assert.Empty(t, overflowed)
	assert.Nil(t, out, "no additions returns the original (nil) denylist unchanged")
}

// TestExcludeContextOverflowModels_SignatureSavingsOnlyForStrippingTargets is
// the regression for the review finding: base64 thought-signatures are stripped
// before dispatch to a non-Anthropic-family target but kept for an Anthropic
// passthrough, so the signature savings must be applied only to stripping
// targets. Every roster model strips (AIand is OpenAI-compat), and an off-roster
// ID is unknown to the catalog — it keeps signatures and the conservative
// default window. Here the raw estimate overflows both; the savings pull the
// roster model back under its window but must NOT rescue the off-roster one.
func TestExcludeContextOverflowModels_SignatureSavingsOnlyForStrippingTargets(t *testing.T) {
	available := map[string]struct{}{
		"qwen/qwen3.8-27b":    {}, // aiand → OpenAI-compat, strips signatures, 262144 window
		"vendor/legacy-model": {}, // unknown to catalog and capability registry: keeps signatures, 128K default
	}

	// est+reserve = 268K overflows qwen's 262144 without savings; -20K savings = 248K fits.
	out, overflowed := excludeContextOverflowModels(260_000, 20_000, 8_000, nil, nil, available)

	assert.NotContains(t, overflowed, "qwen/qwen3.8-27b", "the roster target strips signatures, so the savings keep it under its 262K window")
	assert.Contains(t, overflowed, "vendor/legacy-model", "a keeps-signatures target gets no savings and overflows its default window")
	_, qwenExcluded := out["qwen/qwen3.8-27b"]
	assert.False(t, qwenExcluded, "stripping target must not be denylisted")
}

// TestSafetyExcludedModels_CatchesPolicyExcludedOverflow guards the bypass
// gap: the routing-path filter skips models already in excluded_models, so a
// both-policy-and-overflow model never lands on the routing denylist. The
// safety set re-runs against an empty base to close that gap.
func TestSafetyExcludedModels_CatchesPolicyExcludedOverflow(t *testing.T) {
	// A body large enough that ContextOverflowTokenEstimate (len/4) plus the
	// output reserve exceeds qwen's 262K window. ~1.3MB / 4 ≈ 325K > 262K.
	big := strings.Repeat("x", 1_300_000)
	env, err := translate.ParseAnthropic([]byte(`{"model":"qwen/qwen3.8-27b","messages":[{"role":"user","content":"` + big + `"}]}`))
	require.NoError(t, err)

	// qwen3.8-27b is a 262K-only model, so the big body overflows it. It is
	// also the requested model AND policy-excluded here.
	s := &Service{availableModels: map[string]struct{}{"qwen/qwen3.8-27b": {}}}

	// The routing-path filter, seeded with the policy exclusion, skips it (it is
	// already excluded) — so the overflow denylist it returns is empty.
	_, routingOverflowed := excludeContextOverflowModels(
		env.ContextOverflowTokenEstimate(), env.SignatureTokenSavings(), 8_000,
		nil, map[string]struct{}{"qwen/qwen3.8-27b": {}}, s.availableModels,
	)
	assert.NotContains(t, routingOverflowed, "qwen/qwen3.8-27b",
		"the routing filter skips a policy-excluded model — this is the gap safetyExcludedModels must close")

	// safetyExcludedModels re-runs against an empty base, so it DOES catch the
	// overflow regardless of policy exclusion.
	safety := s.safetyExcludedModels(env, 8_000, nil)
	_, blocked := safety["qwen/qwen3.8-27b"]
	assert.True(t, blocked, "a policy-excluded model that also overflows must land in the safety set so bypass blocks it")
}

// TestShouldEnableExtendedContext gates the 1M-context beta on request size:
// ordinary turns stay on the standard window; a large request trips the beta
// well before the ÷5 estimate's undercount could let it reach the 200K wall.
func TestShouldEnableExtendedContext(t *testing.T) {
	assert.False(t, shouldEnableExtendedContext(20_000, 8_000), "small turn must not opt into the 1M window")
	assert.False(t, shouldEnableExtendedContext(extendedContextTriggerTokens-8_000, 8_000), "exactly at the trigger is not over it")
	assert.True(t, shouldEnableExtendedContext(extendedContextTriggerTokens, 8_000), "estimate above the trigger turns the beta on")
	// A ~250K-real-token request estimates well above the trigger even with the
	// ÷5 undercount, so the beta is enabled before it can 400 on the 200K default.
	assert.True(t, shouldEnableExtendedContext(180_000, 8_000), "near-200K request opts into 1M")
}

// TestExcludeContextOverflowModels_UsesRoutableBinding verifies the context
// pre-filter measures the request against the model's served window: 546K plus
// the 64K reserve stays under the model's 1M catalog window for every
// enabled-provider set, so the model is never excluded.
func TestExcludeContextOverflowModels_UsesRoutableBinding(t *testing.T) {
	available := map[string]struct{}{
		"deepseek-ai/deepseek-v4-pro": {},
	}
	enabledBoth := map[string]struct{}{
		providers.ProviderAIAND: {},
	}
	enabledAIANDOnly := map[string]struct{}{
		providers.ProviderAIAND: {},
	}

	// 546016 + 64K reserve is below the model's 1M served window, so the
	// enabled-provider set must not exclude the model.
	outBoth, overflowedBoth := excludeContextOverflowModels(546_016, 0, 64_000, enabledBoth, nil, available)
	assert.NotContains(t, overflowedBoth, "deepseek-ai/deepseek-v4-pro")
	assert.NotContains(t, outBoth, "deepseek-ai/deepseek-v4-pro")

	_, overflowedAIAND := excludeContextOverflowModels(546_016, 0, 64_000, enabledAIANDOnly, nil, available)
	assert.NotContains(t, overflowedAIAND, "deepseek-ai/deepseek-v4-pro",
		"the catalog model-level window is 1M")

	// nil enabledProviders retains legacy model-level behavior.
	_, overflowedNil := excludeContextOverflowModels(546_016, 0, 64_000, nil, nil, available)
	assert.NotContains(t, overflowedNil, "deepseek-ai/deepseek-v4-pro",
		"the catalog model-level window is 1M")
}

// TestAdmitWidestOnTotalOverflow pins the no-router-compaction contract: an
// estimate that rules out every model re-admits the largest-window ones so
// the upstream, not the ÷4 estimate, decides whether the request overflows.
func TestAdmitWidestOnTotalOverflow(t *testing.T) {
	available := map[string]struct{}{
		"zai-org/glm-5.3":  {},
		"qwen/qwen3.8-27b": {},
	}
	ruledOut, overflowed := excludeContextOverflowModels(2_000_000, 0, 8_000, nil, nil, available)
	require.ElementsMatch(t, []string{"zai-org/glm-5.3", "qwen/qwen3.8-27b"}, overflowed)
	assert.Equal(t, []string{"zai-org/glm-5.3"}, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"only the widest model is re-admitted")

	ruledOut, overflowed = excludeContextOverflowModels(250_000, 0, 8_000, nil, nil, available)
	assert.Nil(t, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"a request some model fits leaves the pre-filter's exclusions alone")

	policyExcluded := map[string]struct{}{"zai-org/glm-5.3": {}}
	ruledOut, overflowed = excludeContextOverflowModels(2_000_000, 0, 8_000, nil, policyExcluded, available)
	assert.Equal(t, []string{"qwen/qwen3.8-27b"}, admitWidestOnTotalOverflow(ruledOut, overflowed, available, nil),
		"a policy-excluded model is never re-admitted; the widest allowed one is")
}

func TestWithoutModels(t *testing.T) {
	set := map[string]struct{}{"a": {}, "b": {}}
	out := withoutModels(set, []string{"a"})
	assert.Equal(t, map[string]struct{}{"b": {}}, out)
	assert.Len(t, set, 2, "the input set is never mutated")
	assert.Nil(t, withoutModels(nil, []string{"a"}))
}

func TestWithoutModelsKeep_PreservesIndependentExclusions(t *testing.T) {
	set := map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}, "zai-org/glm-5.3-flash": {}}
	admitted := []string{"deepseek-ai/deepseek-v4-pro"}
	unsigned := []string{"deepseek-ai/deepseek-v4-pro"}
	out := withoutModelsKeep(set, admitted, unsigned)
	assert.Equal(t, map[string]struct{}{"deepseek-ai/deepseek-v4-pro": {}, "zai-org/glm-5.3-flash": {}}, out,
		"unsigned-history exclusion survives overflow readmission")
	assert.Equal(t, map[string]struct{}{"zai-org/glm-5.3-flash": {}}, withoutModelsKeep(set, admitted, nil),
		"with no keep list, overflow readmission still drops the overflowed model")
}

func TestContextWindowOnlyExclusions_DropsAdmittedAndUnsigned(t *testing.T) {
	overflowed := []string{"qwen/qwen3.8-27b", "zai-org/glm-5.3", "deepseek-ai/deepseek-v4-pro"}
	out := contextWindowOnlyExclusions(overflowed, []string{"zai-org/glm-5.3"}, []string{"deepseek-ai/deepseek-v4-pro"})
	assert.Equal(t, map[string]struct{}{"qwen/qwen3.8-27b": {}}, out,
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
