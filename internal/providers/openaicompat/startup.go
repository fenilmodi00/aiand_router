package openaicompat

import (
	"aiand-os/router/internal/providers/httputil"
	"context"
)

// WarmTransport initializes the configured origin using the serving transports.
func (c *Client) WarmTransport(ctx context.Context, requiredOrigin string) (bool, error) {
	return httputil.WarmTransport(ctx, c.baseURL, requiredOrigin, c.http)
}
