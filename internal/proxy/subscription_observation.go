package proxy

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

// SubscriptionOwnerContextKey carries the linked-account owner resolved by the
// auth middleware.
type SubscriptionOwnerContextKey struct{}

// WithSubscriptionOwner records which linked accounts this request may serve.
func WithSubscriptionOwner(ctx context.Context, owner auth.SubscriptionOwner) context.Context {
	return context.WithValue(ctx, SubscriptionOwnerContextKey{}, owner)
}

// subscriptionOwnerFromContext resolves request identity for primary admission.
// Enrollment key identity never partitions serving capacity.
func subscriptionOwnerFromContext(ctx context.Context) auth.SubscriptionOwner {
	if _, ok := requestcontext.InternalTestIdentityFrom(ctx); ok {
		return auth.SubscriptionOwner{}
	}
	if owner, ok := ctx.Value(SubscriptionOwnerContextKey{}).(auth.SubscriptionOwner); ok && owner.Valid() {
		return owner
	}
	return auth.SubscriptionOwner{APIKeyID: apiKeyIDFromContext(ctx)}
}

// ManagedSubscriptionUsageContextKey carries per-request billing attribution.
type ManagedSubscriptionUsageContextKey struct{}

// ManagedSubscriptionUsage is request-local attribution shared by the auth
// middleware context and provider-specific dispatch attempt contexts.
type ManagedSubscriptionUsage struct {
	IntendedModel      string
	Finished           bool
	WinningCredentials *Credentials
}

// WithManagedSubscriptionUsage prepares request-local subscription attribution.
func WithManagedSubscriptionUsage(ctx context.Context) context.Context {
	if existing, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); existing != nil {
		return ctx
	}
	return context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{})
}

