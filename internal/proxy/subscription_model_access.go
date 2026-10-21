package proxy

import (
	"context"
	"errors"
	"hash/maphash"
	"net/http"
	"strings"
	"sync"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/tidwall/gjson"
)

const subscriptionModelDenialTTL = 15 * time.Minute

type subscriptionModelKey struct {
	token    uint64
	provider string
	model    string
}

type subscriptionModelAccess struct {
	once    sync.Once
	seed    maphash.Seed
	entries *lru.Cache[subscriptionModelKey, time.Time]
}

func (a *subscriptionModelAccess) cache() *lru.Cache[subscriptionModelKey, time.Time] {
	a.once.Do(func() {
		a.seed = maphash.MakeSeed()
		a.entries, _ = lru.New[subscriptionModelKey, time.Time](4096)
	})
	return a.entries
}

func (a *subscriptionModelAccess) key(token []byte, model string) subscriptionModelKey {
	return a.keyForProvider(token, "", model)
}

func (a *subscriptionModelAccess) keyForProvider(token []byte, provider, model string) subscriptionModelKey {
	a.cache()
	return subscriptionModelKey{token: maphash.Bytes(a.seed, token), provider: provider, model: router.StripDateSuffix(model)}
}

func (a *subscriptionModelAccess) denied(token []byte, model string, now time.Time) bool {
	return a.deniedForProvider(token, "", model, now)
}

func (a *subscriptionModelAccess) deniedForProvider(token []byte, provider, model string, now time.Time) bool {
	until, ok := a.cache().Get(a.keyForProvider(token, provider, model))
	return ok && now.Before(until)
}

func anthropicSubscriptionModelRejected(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusNotFound {
		return false
	}
	return gjson.GetBytes(upstream.Body, "error.type").String() == "not_found_error" &&
		strings.HasPrefix(strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String()), "model:")
}

// anthropicSubscriptionThirdPartyRefused reports whether err is Anthropic's
// refusal to serve a non-Claude-Code client on a subscription's plan limits
// ("Third-party apps now draw from your extra usage, not your plan limits").
// It arrives as a 400 invalid_request_error, so neither IsRetryable nor the
// OAuth-rejection gate catches it; the account is healthy for Claude Code
// traffic, so it is a per-client-app capacity failure, not a bad credential.
func anthropicSubscriptionThirdPartyRefused(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusBadRequest {
		return false
	}
	if gjson.GetBytes(upstream.Body, "error.type").String() != "invalid_request_error" {
		return false
	}
	message := strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String())
	return strings.Contains(message, "third-party apps") && strings.Contains(message, "extra usage")
}

func (s *Service) recordSubscriptionModelRejection(ctx context.Context, provider, model string, err error) {
	// A model rejection is only remembered per subscription credential, and no
	// credential is ever a subscription bearer.
}

type suppressClaudeModelContextKey struct{}

func claudeModelSuppressed(ctx context.Context, model string) bool {
	models, _ := ctx.Value(suppressClaudeModelContextKey{}).(map[string]struct{})
	_, suppressed := models[router.StripDateSuffix(model)]
	return suppressed
}

func (s *Service) resolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	return resolveAndInjectCredentials(ctx, provider, model, headers)
}
