package proxy

import (
	"context"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const verificationWeakToolModel = "qwen/qwen3-235b-a22b-2507"

type verificationToolResolverRouter struct {
	resolver        *policy.Resolver
	observedRequest router.Request
	resolved        policy.ResolvedCandidates
}

func (r *verificationToolResolverRouter) Route(_ context.Context, request router.Request) (router.Decision, error) {
	r.observedRequest = request
	r.resolved = r.resolver.Resolve(request)
	trace := &router.SelectionTrace{SelectedGroup: "high", EffectiveOrders: map[string][]string{"high": {codexCoveredModel, verificationWeakToolModel}}, CandidateRosterIDs: r.resolved.CandidateModels()}
	for _, diagnostic := range r.resolved.Diagnostics {
		trace.ResolverExclusions = append(trace.ResolverExclusions, router.SelectionExclusion{CatalogID: diagnostic.CatalogID, RosterID: diagnostic.RosterID, Reason: string(diagnostic.Reason)})
	}
	return router.Decision{Provider: providers.ProviderAIAND, Model: codexCoveredModel, Reason: "test", Metadata: &router.RoutingMetadata{CandidateModels: r.resolved.CandidateModels(), SelectionTrace: trace}}, nil
}

// verificationAlternativeLeaser hands out a synthetic second subscription
// account so a turn can fail over to it.
type verificationAlternativeLeaser struct{ scriptedSubscriptionLeaser }

func (l *verificationAlternativeLeaser) Lease(_ context.Context, owner auth.SubscriptionOwner, _ subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	for _, id := range owner.ExcludedAccountIDs {
		if id == "alternative-account" {
			return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
		}
	}
	return subscriptions.Lease{AccountID: "alternative-account", OwnerID: "synthetic-owner", Tier: auth.SubscriptionTierPersonal, AccessToken: "alternative-seat", ProviderAccount: "synthetic-provider"}, true, nil
}
