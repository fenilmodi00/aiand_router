package proxy

import (
	"context"
	"net/http"

	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/inference"
)

// withSubscriptionFundingAuthorization prepares the request-local subscription
// attribution and installs the admission gate that refuses paid inference
// while the caller's own subscription still has quota.
//
// The gate never denies today: a client bearer is a plain API key, never a
// subscription credential, so the caller never holds subscription quota to
// protect. The seam stays because the dispatch executor consults it, and a
// future credential source that CAN be a subscription has exactly one place to
// enforce it.
func (s *Service) withSubscriptionFundingAuthorization(ctx context.Context, headers http.Header) context.Context {
	ctx = WithManagedSubscriptionUsage(ctx)
	if dispatch.AuthorizationFromContext(ctx) != nil {
		return ctx
	}
	return dispatch.WithAuthorization(ctx, func(context.Context, inference.Target) error {
		return nil
	})
}

// subscriptionAPIAuthorization reports whether the target may be dispatched
// under the request's funding mode. Nothing is ever funded by a caller
// subscription, so the answer is always "yes".
func (s *Service) subscriptionAPIAuthorization(ctx context.Context, provider, model string) error {
	ctx = s.withSubscriptionFundingAuthorization(ctx, nil)
	return dispatch.AuthorizationFromContext(ctx)(clearCredentials(ctx), inference.Target{Provider: provider, CatalogID: model})
}
