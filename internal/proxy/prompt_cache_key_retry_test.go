package proxy_test

import (
	"context"
	"net/http"
	"strings"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
)

// pckStrictGatewayProvider rejects any body carrying prompt_cache_key
// with an unknown-field 400; everything else succeeds.
type pckStrictGatewayProvider struct {
	fakeProvider
}

func (p *pckStrictGatewayProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	saved := make([]byte, len(prep.Body))
	copy(saved, prep.Body)
	p.proxyBodies = append(p.proxyBodies, saved)
	if strings.Contains(string(prep.Body), `"prompt_cache_key"`) {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusBadRequest,
			Body:   []byte(`{"error":{"message":"prompt_cache_key: extra inputs are not permitted"}}`),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	return nil
}
