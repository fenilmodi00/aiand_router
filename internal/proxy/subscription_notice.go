package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"aiand-os/router/internal/billing"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/translate"
)

// subscriptionSource names the caller-side subscription a funding notice
// describes. It selects the marker text, not a routing lane.
//
// The router has one upstream (ai&), so both ingress surfaces — the Anthropic
// Messages API and the OpenAI chat/responses APIs — resolve to the same
// funding mode. The distinction survives only in the marker wording.
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
const topUpURL = "https://app.workaiand.ai/organization/settings/aiand-router"

// subscriptionOnlyWarningMarker is prepended to a subscription-only turn so the
// customer sees why the router refused to fall back to a paid model and how to
// restore full routing. The marker is emitted only when terminal surfaces are
// enabled for the installation and request.
const subscriptionOnlyWarningMarker = routingMarkerPrefix +
	"your Aiand router credits are depleted, so this turn is running in subscription-only mode and paid model fallback is disabled. Add credits to restore full routing: " +
	topUpURL + "\n\n"

// subscriptionSpendCapWarningMarker is prepended to a subscription-only turn
// a spend cap barred from paid capacity. The organization may still hold
// credits, so it names the cap and carries no top-up CTA.
const subscriptionSpendCapWarningMarker = routingMarkerPrefix +
	"a Aiand router spend cap has been reached, so this turn is running in subscription-only mode and paid model fallback is disabled.\n\n"

// subscriptionFundingWarning keeps funding policy separate from notice encoding.
type subscriptionFundingWarning struct {
	reason billing.SubscriptionOnlyReason
}

func (w subscriptionFundingWarning) text() string {
	switch w.reason {
	case billing.SubscriptionOnlyCreditsDepleted:
		return subscriptionOnlyWarningMarker
	case billing.SubscriptionOnlySpendCapReached:
		return subscriptionSpendCapWarningMarker
	default:
		return ""
	}
}

func routerNoticeForRequest(ctx context.Context, headers http.Header, model, marker string) translate.RouterNotice {
	kind := translate.NoticeModelSelection
	reason, _ := billing.SubscriptionOnlyReasonFromContext(ctx)
	warning := subscriptionFundingWarning{reason: reason}
	echoed, _ := ctx.Value(subscriptionOnlyWarningsEchoedContextKey{}).(map[subscriptionFundingWarning]bool)
	if text := warning.text(); text != "" && !echoed[warning] {
		kind = translate.NoticeFundingWarning
		marker = text
	}
	return translate.NewRouterNotice(kind, suppressMarkerIfRequested(ctx, headers, marker), model, "")
}

type subscriptionOnlyWarningsEchoedContextKey struct{}

// subscriptionFundingWarnings lists every warning text the router can emit, so
// the echo check covers each one. A warning missing from this list repeats on
// every turn of the same conversation.
var subscriptionFundingWarnings = []subscriptionFundingWarning{
	{reason: billing.SubscriptionOnlyCreditsDepleted},
	{reason: billing.SubscriptionOnlySpendCapReached},
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
