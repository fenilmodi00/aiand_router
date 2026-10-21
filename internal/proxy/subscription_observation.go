package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
)

// Inference route paths (gin FullPath templates) whose upstream cost a caller
// subscription can cover: the Anthropic Messages API is served by a Claude
// subscription, the OpenAI chat/responses APIs by a Codex subscription. Other
// gated routes (Gemini /v1beta, /v1/route) have no consumer subscription.
const (
	routePathMessages        = "/v1/messages"
	routePathChatCompletions = "/v1/chat/completions"
	routePathResponses       = "/v1/responses"
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
	Served                bool
	SubscriptionAttempted bool
	OverageInUse          bool
	SubscriptionAccountID string
	SubscriptionOwnerID   string
	SubscriptionTier      auth.SubscriptionTier
	IntendedModel         string
	Finished              bool
	WinningCredentials    *Credentials
}

// WithManagedSubscriptionUsage prepares request-local subscription attribution.
func WithManagedSubscriptionUsage(ctx context.Context) context.Context {
	if existing, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); existing != nil {
		return ctx
	}
	return context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{})
}

// CodexSubscriptionCoversModel reports whether model may receive the caller's
// ChatGPT OAuth credential. See requestcontext.CodexSubscriptionCoversModel.
func CodexSubscriptionCoversModel(model string) bool {
	return requestcontext.CodexSubscriptionCoversModel(model)
}

func codexSubscriptionCanAttemptModel(model string) bool {
	return requestcontext.CodexSubscriptionCanAttemptModel(model)
}

// WithUsageObserver wires physical-account quota observation for source selection.
func (s *Service) WithUsageObserver(obs *usage.Observer) *Service {
	s.usageObserver = obs
	return s
}

// subscriptionCredentialFallbackUsed reports that the turn first tried a
// subscription credential and then served on a non-subscription one, so the
// failover must be attributed to the subscription path. No subscription
// credential is ever resolved, so the turn never takes that path.
func subscriptionCredentialFallbackUsed(ctx context.Context) bool {
	return false
}

// costNeutralSubscriptionServed reports that the turn ran on the caller's own
// subscription plan, so it costs the organization nothing. No subscription
// credential is ever resolved, so the turn is never cost-neutral.
func (s *Service) costNeutralSubscriptionServed(ctx context.Context) bool {
	return false
}

// presentSubscriptionTokens returns the caller's Codex and Claude subscription
// tokens, "" when absent. A client bearer is a plain API key, never a
// subscription credential, so no subscription token is ever present.
//
// A free function (not a *Service method) because it reads only ctx + headers:
// the prepaid balance gate (server/middleware) has no Service handle but must
// agree with this path on what counts as "a subscription is present".
func presentSubscriptionTokens(ctx context.Context, headers http.Header) (codex, anthropic string) {
	// Subscription routing disabled: treat as absent so source selection, usage-bypass, and
	// balance gate all agree the turn is prepaid.
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return "", ""
	}
	return "", ""
}

// subscriptionServableProviders returns the provider lanes the caller's own
// subscription can enter. A client bearer is never a subscription credential,
// so no lane is ever servable.
func subscriptionServableProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	return nil
}

// RequestPresentsCoveringSubscription reports whether the request carries a
// validated subscription credential that can serve at least one model on
// routePath. A client bearer is a plain API key, never a subscription
// credential, so no route is ever covered.
func RequestPresentsCoveringSubscription(ctx context.Context, headers http.Header, routePath string) bool {
	return false
}

// withUsageObserver records headers against the credential actually dispatched.
// Only a subscription credential carries quota headers, and none is ever
// resolved, so the observer never records a snapshot.
func (s *Service) withUsageObserver(ctx context.Context, headers http.Header) context.Context {
	if s.usageObserver == nil {
		return ctx
	}
	ctx = withUnifiedLimitCapture(ctx)
	obs := func(callCtx context.Context, h http.Header) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					observability.FromContext(callCtx).Error("weave-capture Phase 0: recovered from panic capturing unified limit headers", "recover", r)
				}
			}()
			captureUnifiedLimitHeaders(callCtx, h)
		}()
	}
	return providers.WithUpstreamHeaderObserver(ctx, obs)
}
