package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// subscriptionSource names the caller-side subscription a funding notice
// describes. It selects the marker text, not a routing lane.
type subscriptionSource string

const (
	subscriptionSourceClaude subscriptionSource = "claude"
	subscriptionSourceCodex  subscriptionSource = "codex"
)

var (
	// ErrSubscriptionPoolExhausted marks a turn the caller's own subscription
	// could not serve, with no paid capacity left to fall back to.
	ErrSubscriptionPoolExhausted = errors.New("subscription account pool exhausted")
	// ErrSubscriptionPoolUnavailable marks a turn refused because the
	// caller's subscription quota could not be established.
	ErrSubscriptionPoolUnavailable = errors.New("subscription account pool unavailable")
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable)
}

// claudeSubscriptionExhausted reports whether the caller's Claude subscription
// is exhausted or actively drawing billable overage. True only when a Claude
// token has an observed spent/billable snapshot and a paid fallback key exists.
// The token key is derived identically to withUsageObserver so this read
// agrees with what the observer recorded. When true the caller suppresses the
// subscription credential so the turn serves on the Weave / BYOK key rather than
// the customer's credits.
func (s *Service) claudeSubscriptionExhausted(ctx context.Context, headers http.Header) bool {
	return s.anthropicSubscriptionObservedExhausted(ctx, headers)
}

// anthropicSubscriptionObservedExhausted reports whether the caller's present
// Claude subscription is exhausted or using paid overage per the observer,
// independent of whether a fallback key exists. claudeSubscriptionExhausted
// layers the fallback-key requirement on top for its suppress-and-serve-on-Weave
// -key path; subscription-only refusal uses this bare signal because paid
// fallback is disabled there — an exhausted sub can only 429, while using
// overage would charge the customer. The turn is refused with a controlled 402.
func (s *Service) anthropicSubscriptionObservedExhausted(ctx context.Context, headers http.Header) bool {
	if s.usageObserver == nil {
		return false
	}
	_, anthroTok := presentSubscriptionTokens(ctx, headers)
	if anthroTok == "" {
		return false
	}
	snap, ok := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(anthroTok)))
	return ok && snap.BillableOrExhausted()
}

// anthropicFallbackKeyAvailable reports whether a non-subscription Anthropic
// credential is configured to serve a Claude turn when the caller's subscription
// is spent: a per-request BYOK Anthropic key, or the deployment's own
// ANTHROPIC_API_KEY (tracked in deploymentKeyedProviders). Without one, dropping
// the subscription token would leave the turn with no Anthropic credential and
// 400 — strictly worse than the 429 — so the caller keeps using the subscription.
func (s *Service) anthropicFallbackKeyAvailable(ctx context.Context) bool {
	if byok := BuildCredentialsMap(externalKeysFromContext(ctx)); byok != nil {
		if _, ok := byok[providers.ProviderAIAND]; ok {
			return true
		}
	}
	if s.deploymentKeyedProviders != nil {
		if _, ok := s.deploymentKeyedProviders[providers.ProviderAIAND]; ok {
			return true
		}
	}
	return false
}

// anthropicOAuthCredentialRejected reports whether err is a buffered Anthropic
// 401 authentication_error or 403 permission_error — a rejected subscription
// OAuth token that gates the failover onto the BYOK/deployment key. Narrow by
// design so an unrelated 403 (e.g. content policy) stays terminal.
func anthropicOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	if buffered.Status != http.StatusUnauthorized && buffered.Status != http.StatusForbidden {
		return false
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &env); jsonErr != nil {
		return false
	}
	return env.Error.Type == "authentication_error" || env.Error.Type == "permission_error"
}

// topUpURL is the customer-facing page where org admins buy router credits.
// Duplicated from middleware.TopUpURL (proxy can't import the middleware
// adapter without an import cycle) so subscription-only warnings and the
// credits-exhausted 402 can surface the CTA.
const topUpURL = "https://app.workweave.ai/organization/settings/weave-router"

// subscriptionOnlyWarningMarker is prepended to a subscription-only bypass
// response so the customer sees why they're being served on their own plan and
// how to restore full routing. The marker is emitted only when terminal
// surfaces are enabled for the installation and request.
const subscriptionOnlyWarningMarker = routingMarkerPrefix +
	"your Weave router credits are depleted, so this turn is running on your own Anthropic subscription and paid model fallback is disabled. Add credits to restore full routing: " +
	topUpURL + "\n\n"

// subscriptionOnlyWarningMarkerCodex is the Codex/OpenAI-surface counterpart to
// subscriptionOnlyWarningMarker, prepended to a subscription-only turn served on
// the caller's own ChatGPT (Codex) subscription.
const subscriptionOnlyWarningMarkerCodex = routingMarkerPrefix +
	"your Weave router credits are depleted, so this turn is running on your own ChatGPT (Codex) subscription and paid model fallback is disabled. Add credits to restore full routing: " +
	topUpURL + "\n\n"

// subscriptionSpendCapWarningMarker is prepended to a subscription-only turn
// a spend cap barred from paid capacity. The organization may still hold
// credits, so it names the cap and carries no top-up CTA.
const subscriptionSpendCapWarningMarker = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own Anthropic subscription and paid model fallback is disabled.\n\n"

const subscriptionSpendCapWarningMarkerCodex = routingMarkerPrefix +
	"a Weave router spend cap has been reached, so this turn is running on your own ChatGPT (Codex) subscription and paid model fallback is disabled.\n\n"

// subscriptionFundingWarning keeps funding policy separate from notice encoding.
type subscriptionFundingWarning struct {
	reason billing.SubscriptionOnlyReason
	source subscriptionSource
}

func (w subscriptionFundingWarning) text() string {
	switch w.reason {
	case billing.SubscriptionOnlyCreditsDepleted:
		if w.source == subscriptionSourceCodex {
			return subscriptionOnlyWarningMarkerCodex
		}
		return subscriptionOnlyWarningMarker
	case billing.SubscriptionOnlySpendCapReached:
		if w.source == subscriptionSourceCodex {
			return subscriptionSpendCapWarningMarkerCodex
		}
		return subscriptionSpendCapWarningMarker
	default:
		return ""
	}
}

func routerNoticeForRequest(ctx context.Context, headers http.Header, source subscriptionSource, model, marker string) translate.RouterNotice {
	kind := translate.NoticeModelSelection
	reason, _ := billing.SubscriptionOnlyReasonFromContext(ctx)
	warning := subscriptionFundingWarning{reason: reason, source: source}
	echoed, _ := ctx.Value(subscriptionOnlyWarningsEchoedContextKey{}).(map[subscriptionFundingWarning]bool)
	if text := warning.text(); text != "" && !echoed[warning] {
		kind = translate.NoticeFundingWarning
		marker = text
	}
	return translate.NewRouterNotice(kind, suppressMarkerIfRequested(ctx, headers, marker), model, "")
}

type subscriptionOnlyWarningsEchoedContextKey struct{}

var subscriptionFundingWarnings = []subscriptionFundingWarning{
	{reason: billing.SubscriptionOnlyCreditsDepleted, source: subscriptionSourceClaude},
	{reason: billing.SubscriptionOnlyCreditsDepleted, source: subscriptionSourceCodex},
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptionSourceClaude},
	{reason: billing.SubscriptionOnlySpendCapReached, source: subscriptionSourceCodex},
}

// Capture replay evidence before cleanup, retaining it through Responses delegation.
func withSubscriptionOnlyWarningEcho(ctx context.Context, body []byte) context.Context {
	texts := make([]string, 0, len(subscriptionFundingWarnings))
	for _, warning := range subscriptionFundingWarnings {
		texts = append(texts, warning.text())
	}
	seen := translate.EchoedInAssistantText(body, texts)
	echoed := make(map[subscriptionFundingWarning]bool, len(subscriptionFundingWarnings))
	prior, _ := ctx.Value(subscriptionOnlyWarningsEchoedContextKey{}).(map[subscriptionFundingWarning]bool)
	for _, warning := range subscriptionFundingWarnings {
		echoed[warning] = seen[warning.text()] || prior[warning]
	}
	return context.WithValue(ctx, subscriptionOnlyWarningsEchoedContextKey{}, echoed)
}

// ErrCreditsExhaustedSubscriptionUnavailable is returned by ProxyMessages and
// ProxyOpenAIChatCompletion when the org is in subscription-only mode but the
// turn cannot be served on the caller's own
// subscription (Claude or Codex) at all — routing resolved to a paid model, or
// (Anthropic bypass) the subscription is already rate-limit exhausted. Paid
// failover is disabled in this mode, so the turn is refused (HTTP 402) rather
// than billed against an already-negative balance. A runtime failure on a turn
// that DID resolve onto the subscription surfaces the raw upstream error
// instead — it's the caller's own plan failing, with nowhere to fail over to.
var ErrCreditsExhaustedSubscriptionUnavailable = errors.New("credits exhausted and subscription unavailable for this turn")

// ErrSpendCapSubscriptionUnavailable is the ErrCreditsExhaustedSubscriptionUnavailable
// variant for a turn a spend cap, not a depleted balance, made subscription-only.
// It wraps the credits sentinel so every errors.Is refusal check still matches.
var ErrSpendCapSubscriptionUnavailable = fmt.Errorf("spend cap reached: %w", ErrCreditsExhaustedSubscriptionUnavailable)

// subscriptionOnlyUnavailable returns the refusal for a subscription-only
// turn the caller's subscription cannot serve, naming the gate that disabled
// paid fallback.
func subscriptionOnlyUnavailable(ctx context.Context) error {
	if reason, _ := billing.SubscriptionOnlyReasonFromContext(ctx); reason == billing.SubscriptionOnlySpendCapReached {
		return ErrSpendCapSubscriptionUnavailable
	}
	return ErrCreditsExhaustedSubscriptionUnavailable
}

// subscriptionPoolExhausted is the pool-exhaustion refusal. When a depleted
// balance or reached cap disabled paid fallback, it also carries that gate's
// refusal so the client is told why, not just to retry.
func subscriptionPoolExhausted(ctx context.Context) error {
	if reason, ok := billing.SubscriptionOnlyReasonFromContext(ctx); ok && reason != billing.SubscriptionOnlyLinkedFirst {
		return errors.Join(subscriptionOnlyUnavailable(ctx), ErrSubscriptionPoolExhausted)
	}
	return ErrSubscriptionPoolExhausted
}
