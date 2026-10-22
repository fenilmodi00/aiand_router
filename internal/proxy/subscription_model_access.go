package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
)

// anthropicSubscriptionModelRejected reports whether err is the upstream's
// model-availability rejection: a 404 whose body names the model. It can fire
// on the one live upstream, so it stays.
func anthropicSubscriptionModelRejected(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusNotFound {
		return false
	}
	return gjson.GetBytes(upstream.Body, "error.type").String() == "not_found_error" &&
		strings.HasPrefix(strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String()), "model:")
}

func (s *Service) resolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	return resolveAndInjectCredentials(ctx, provider, model, headers)
}
