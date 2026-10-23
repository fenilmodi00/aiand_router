package openai_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/api/openai"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type overflowRouter struct{}

func (overflowRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Provider: providers.ProviderAIAND, Model: "deepseek/deepseek-v4.1-flash", Reason: "test"}, nil
}

type overflowProvider struct{}

func (overflowProvider) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	return &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
	}
}

func (overflowProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestChatCompletionHandler_CrossFormatOverflowReturnsNativeError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			svc := proxy.NewService(
				overflowRouter{}, map[string]providers.Client{providers.ProviderAIAND: overflowProvider{}},
				nil, false, nil, nil, false, providers.ProviderAIAND, "deepseek/deepseek-v4.1-flash", nil,
			)
			engine := gin.New()
			engine.POST("/v1/chat/completions", openai.ChatCompletionHandler(svc, nil))
			body := fmt.Sprintf(`{"model":"deepseek/deepseek-v4.1-flash","max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if stream {
				require.Equal(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
				assert.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
				assert.Contains(t, rec.Body.String(), "prompt is too long")
			} else {
				require.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Equal(t, "context_length_exceeded", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
				assert.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				assert.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "context window")
			}
			assert.NotContains(t, rec.Body.String(), "upstream Responses request failed")
		})
	}
}
