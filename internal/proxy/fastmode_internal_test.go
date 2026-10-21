package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	fastOpusModel = "z-ai/glm-5.2"
	fastLunaModel = "gpt-5.6-luna"
)

func fastModeCtx(models ...string) context.Context {
	return context.WithValue(context.Background(), InstallationFastModeModelsContextKey{}, models)
}

func newFastModeService(decision router.Decision, upstream providers.Client) (*Service, *bypassCaptureTelemetry) {
	telemetry := newBypassCaptureTelemetry()
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderAIAND: upstream,
	}, nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", telemetry)
	return svc, telemetry
}

func TestProxyMessages_FastModeOffLeavesRequestAndListPrice(t *testing.T) {
	const (
		inputTokens  = 1200
		outputTokens = 340
	)
	upstream := &bypassFakeProvider{respBody: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"z-ai/glm-5.2","stop_reason":"end_turn","usage":{"input_tokens":1200,"output_tokens":340}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAIAND, Model: fastOpusModel}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx(fastLunaModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "speed").Exists())

	base, ok := catalog.PriceFor(providers.ProviderAIAND, fastOpusModel)
	require.True(t, ok)
	want := routerResponseCostFromPricing(base, providers.ProviderAIAND, inputTokens, outputTokens, 0, 0, catalog.UsageModifiers{})
	assert.Equal(t, strconv.FormatFloat(want.TotalUSD, 'f', -1, 64), rec.Header().Get(HeaderRouterCostUSD))
}

// fastQuotaFakeProvider refuses every fast-tier body with Anthropic's
// fast-mode quota 429 and answers standard-speed bodies with respBody,
// recording each dispatched body in order.
type fastQuotaFakeProvider struct {
	respBody string
	bodies   [][]byte
}

const anthropicFastQuota429 = `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your organization's rate limit of 0 fast mode input tokens per minute."}}`

func (f *fastQuotaFakeProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	f.bodies = append(f.bodies, append([]byte(nil), prep.Body...))
	if gjson.GetBytes(prep.Body, "speed").Str == "fast" {
		return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(anthropicFastQuota429)}
	}
	_, _ = io.WriteString(w, f.respBody)
	return nil
}

func (f *fastQuotaFakeProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func TestProxyMessages_FastModeGatewayNeverGetsFastFields(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"qwen/qwen3-235b-a22b-2507","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`}
	svc, _ := newFastModeService(router.Decision{Provider: providers.ProviderAIAND, Model: "qwen/qwen3-235b-a22b-2507"}, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, svc.ProxyMessages(fastModeCtx("qwen/qwen3-235b-a22b-2507", fastOpusModel), body, rec, req))

	require.Equal(t, 1, upstream.dispatches)
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "service_tier").Exists())
	assert.False(t, gjson.GetBytes(upstream.capturedBody, "speed").Exists())
}
