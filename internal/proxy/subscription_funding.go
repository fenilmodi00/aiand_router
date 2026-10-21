package proxy

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router/catalog"
)

// withSubscriptionFundingAuthorization prepares the request-local subscription
// attribution and installs the admission gate that refuses paid inference
// while the caller's own subscription still has quota.
func (s *Service) withSubscriptionFundingAuthorization(ctx context.Context, headers http.Header) context.Context {
	ctx = WithManagedSubscriptionUsage(ctx)
	if dispatch.AuthorizationFromContext(ctx) != nil {
		return ctx
	}
	codexToken, claudeToken := presentSubscriptionTokens(ctx, headers)
	admissionCtx := ctx
	return dispatch.WithAuthorization(ctx, func(callCtx context.Context, target inference.Target) error {
		source, compatible := subscriptionSourceForModel(target)
		if !compatible {
			return nil
		}
		if err := s.authorizeSubscriptionAPI(admissionCtx, source, codexToken, claudeToken); err != nil {
			observability.FromContext(callCtx).Warn("Blocked API inference without subscription quota-exhaustion evidence", "model", target.CatalogID, "provider", target.Provider, "subscription_source", source, "err", err)
			return err
		}
		return nil
	})
}

// subscriptionSourceForModel names the caller-side subscription that could
// serve target. Only the AI& upstream exists, and it serves Claude models.
func subscriptionSourceForModel(target inference.Target) (subscriptionSource, bool) {
	if entry, known := catalog.ByID(target.CatalogID); known {
		for _, binding := range entry.Providers {
			if binding.Provider == providers.ProviderAIAND {
				return subscriptionSourceClaude, true
			}
		}
	}
	if target.Provider == providers.ProviderAIAND {
		return subscriptionSourceClaude, true
	}
	return "", false
}

func (s *Service) subscriptionAPIAuthorization(ctx context.Context, provider, model string) error {
	ctx = s.withSubscriptionFundingAuthorization(ctx, nil)
	return dispatch.AuthorizationFromContext(ctx)(clearCredentials(ctx), inference.Target{Provider: provider, CatalogID: model})
}

// authorizeSubscriptionAPI refuses paid inference while the caller's own
// subscription still has quota: the plan pays for the turn, so the router must
// not spend the organization's credits on it. Paid inference is admitted only
// once the observer proves the included quota is spent.
func (s *Service) authorizeSubscriptionAPI(ctx context.Context, source subscriptionSource, codexToken, claudeToken string) error {
	token := claudeToken
	if source == subscriptionSourceCodex {
		token = codexToken
	}
	if token == "" {
		return nil
	}
	if s.usageObserver != nil {
		if snapshot, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token))); observed && includedQuotaSpent(snapshot, s.clockNow()) {
			return nil
		}
	}
	return fmt.Errorf("%w: caller subscription quota is available or unknown", ErrSubscriptionPoolUnavailable)
}

// includedQuotaSpent treats observed paid overage as spent included quota until
// the reported plan reset. Undated overage never authorizes paid inference.
func includedQuotaSpent(snapshot usage.Snapshot, now time.Time) bool {
	return (snapshot.OverageInUse && snapshot.UnifiedResetAt.After(now)) || snapshot.ExhaustedAsOf(now)
}
