package proxy

import (
	"context"
	"net/http"

	"aiand-os/router/internal/requestcontext"
)

func requestFactsFrom(ctx context.Context, headers http.Header) requestcontext.RequestFacts {
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	return requestcontext.RequestFacts{
		RequestID:      requestIDFor(ctx),
		APIKeyID:       apiKeyIDFromContext(ctx),
		ExternalID:     externalID,
		InstallationID: installationIDFromContext(ctx),
		Client:         ClientIdentityFrom(ctx),
		Headers:        headers,
		ExternalKeys:   externalKeysFromContext(ctx),
	}
}
