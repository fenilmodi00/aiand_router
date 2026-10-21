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
// failover must be attributed to the subscription path.
func subscriptionCredentialFallbackUsed(ctx context.Context) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.Finished && state.SubscriptionAttempted && (state.WinningCredentials == nil || !state.WinningCredentials.OAuth)
}

// costNeutralSubscriptionServed reports that the turn ran on the caller's own
// subscription plan, so it costs the organization nothing.
func (s *Service) costNeutralSubscriptionServed(ctx context.Context) bool {
	return servedOnSubscription(ctx)
}

// presentSubscriptionTokens returns the caller's Codex and Claude subscription
// tokens, "" when absent. Native harnesses send a Claude Code sk-ant-oat…
// bearer or Codex CLI JWT+account-id on Authorization / x-api-key. The token
// doubles as the usage-observer key, and equals the eventually-resolved
// credential, so record and read agree regardless of source.
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
	c := ExtractClientCredentials(providers.ProviderAIAND, headers)
	if c == nil || !c.OAuth {
		return "", ""
	}
	// One inbound bearer covers both consumer surfaces; the route decides which
	// lane the turn may take.
	return string(c.APIKey), string(c.APIKey)
}

// subscriptionServableProviders returns the provider lanes the caller's own
// subscription can enter: OpenAI for a Codex (ChatGPT) sub, Anthropic for a
// Claude sub. Codex model coverage is narrower than its provider lane and is
// applied by excludeCodexOAuthOnlyModels before routing.
func subscriptionServableProviders(ctx context.Context, headers http.Header) map[string]struct{} {
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	out := make(map[string]struct{}, 2)
	if codex != "" {
		out[providers.ProviderAIAND] = struct{}{}
	}
	if anthropic != "" {
		out[providers.ProviderAIAND] = struct{}{}
	}
	return out
}

// restrictToSubscriptionProviders intersects enabled with the providers the
// caller's subscription can serve, so subscription-only mode routes only to
// the caller's own plan and never a paid model.
// Returns enabled unchanged when the caller presents no usable subscription
// (defensive: the balance gate only flags subscription-only when a covering sub
// is present, so this keeps a misconfiguration from emptying the eligible set).
func restrictToSubscriptionProviders(ctx context.Context, headers http.Header, enabled map[string]struct{}) map[string]struct{} {
	sub := subscriptionServableProviders(ctx, headers)
	if len(sub) == 0 {
		return enabled
	}
	out := make(map[string]struct{}, len(sub))
	for p := range enabled {
		if _, ok := sub[p]; ok {
			out[p] = struct{}{}
		}
	}
	return out
}

// RequestPresentsCoveringSubscription reports whether the request carries a
// validated subscription credential that can serve at least one model on
// routePath: a Claude (sk-ant-oat…) sub for /v1/messages, a Codex sub for
// /v1/chat/completions and /v1/responses. Codex's exact model coverage is
// applied downstream before selection. Any other route returns false.
//
// Scoped to the covering family (not "any subscription present") so a Codex
// bearer on /v1/messages — which can't serve that route — doesn't exempt a
// turn that would debit the prepaid balance.
func RequestPresentsCoveringSubscription(ctx context.Context, headers http.Header, routePath string) bool {
	if subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	codex, anthropic := presentSubscriptionTokens(ctx, headers)
	switch routePath {
	case routePathMessages:
		return anthropic != ""
	case routePathChatCompletions, routePathResponses:
		return codex != ""
	default:
		return false
	}
}

// withUsageObserver records headers against the credential actually dispatched.
// Direct OAuth retains token-keyed observation.
func (s *Service) withUsageObserver(ctx context.Context, headers http.Header) context.Context {
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	if s.usageObserver == nil && anthroTok == "" {
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

		if s.usageObserver == nil {
			return
		}
		// Record only the call's resolved subscription credential. Gating on the
		// resolved credential also skips internal calls that don't use the sub,
		// e.g. the handover summarizer's deployment-key Anthropic call after
		// clearCredentials. The parser follows the matched token's family, so this
		// works for the inbound bearer.
		creds := CredentialsFromContext(callCtx)
		if creds == nil || !creds.OAuth {
			return
		}
		switch creds.Source {
		case credSourceCodexSubscription:
			if snap, ok := usage.ParseCodexHeaders(h); ok {
				s.usageObserver.Record(s.subscriptionUsageKey(creds), snap)
				if creds.SubscriptionAccountID != "" {
					s.usageObserver.Record(s.usageObserver.Key(creds.APIKey), snap)
				}
			}
		case credSourceSubscription:
			if snap, ok := usage.ParseAnthropicUnifiedHeaders(h); ok {
				s.usageObserver.Record(s.subscriptionUsageKey(creds), snap)
				if creds.SubscriptionAccountID != "" {
					s.usageObserver.Record(s.usageObserver.Key(creds.APIKey), snap)
				}
			}
		}
	}
	return providers.WithUpstreamHeaderObserver(ctx, obs)
}

func (s *Service) subscriptionUsageKey(creds *Credentials) usage.CredentialKey {
	if creds.SubscriptionAccountID != "" {
		return s.usageObserver.Key([]byte("subscription-account:" + creds.SubscriptionAccountID))
	}
	return s.usageObserver.Key(creds.APIKey)
}
