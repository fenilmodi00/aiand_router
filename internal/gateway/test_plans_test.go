package gateway

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTestPlanSurfaceAllowsInferenceRoutesOnly(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "anthropic messages", method: http.MethodPost, path: "/v1/messages", want: true},
		{name: "anthropic count tokens", method: http.MethodPost, path: "/v1/messages/count_tokens", want: true},
		{name: "chat completions", method: http.MethodPost, path: "/v1/chat/completions", want: true},
		{name: "responses", method: http.MethodPost, path: "/v1/responses", want: true},
		{name: "other method", method: http.MethodPut, path: "/v1/responses"},
		{name: "unknown path", method: http.MethodPost, path: "/v1/embeddings"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parsedURL, err := url.Parse(testCase.path)
			require.NoError(t, err)
			request := &http.Request{Method: testCase.method, URL: parsedURL}
			require.Equal(t, testCase.want, testPlanSurface(request))
		})
	}
}
