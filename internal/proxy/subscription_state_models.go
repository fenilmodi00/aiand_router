package proxy

import (
	"context"
	"net/http"

	"aiand-os/router/internal/billing"
	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/cluster"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/translate"
)

type subscriptionStateAllowedModelsKey struct{}

func subscriptionStateModelsEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(InstallationSubscriptionStateModelsEnabledContextKey{}).(bool)
	return !subscriptionFundingOutOfPlayForRequest(ctx) &&
		(enabled ||
			len(installationSubscriptionModelsWhenActiveFromContext(ctx)) > 0 ||
			len(installationSubscriptionModelsWhenInactiveFromContext(ctx)) > 0)
}

// subscriptionStateRequest preserves all request safety constraints while
// narrowing eligibility. An empty configured state refuses rather than widening.
func (s *Service) subscriptionStateRequest(ctx context.Context, req router.Request, models []string) (context.Context, router.Request) {
	allowed := make(map[string]struct{})
	for _, model := range models {
		if _, excluded := req.ExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.AutomaticExcludedModels[model]; excluded && model != req.ForceModel {
			continue
		}
		if _, excluded := req.SafetyExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.UnsignedHistoryExcludedModels[model]; excluded {
			continue
		}
		if req.AllowedModels != nil && model != req.ForceModel {
			if _, permitted := req.AllowedModels[model]; !permitted {
				continue
			}
		}
		allowed[model] = struct{}{}
	}
	ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, allowed)
	req.AllowedModels = allowed
	req.ExcludedModels = mergeExcludedModels(req.ExcludedModels, s.excludedModelsForRequest(ctx))
	return ctx, req
}

// dispatchSubscriptionStateModels keeps model eligibility separate from
// funding. No upstream serves a caller's included subscription quota, so the
// only eligible targets are the exhausted-model set the installation
// configured, dispatched on paid capacity.
func (s *Service) dispatchSubscriptionStateModels(ctx context.Context, in failoverInputs) (int, error) {
	request := *in.subscriptionStateRequest
	sessionUnavailableModels := modelSet(sessionDemotedModelsFromContext(ctx))
	if sessionUnavailableModels == nil {
		sessionUnavailableModels = make(map[string]struct{})
	}
	for model, cooldownUntil := range sessionCooldownModelsFromContext(ctx) {
		if cooldownUntil.After(s.clockNow()) {
			sessionUnavailableModels[model] = struct{}{}
		}
	}
	request.AutomaticExcludedModels = mergeExcludedModels(request.AutomaticExcludedModels, sessionUnavailableModels)
	if request.ForceModel == "" && in.initialDecision.Reason == translate.ReasonUserForceModel {
		// runTurnLoop already validated and readmitted a strict force-model pin;
		// preserve that exception when rebuilding the request for state routing.
		request.ForceModel = in.initialDecision.Model
	}
	in.subscriptionStateRequest = nil
	in.alternatives = nil
	hasFixedTarget := request.ForceModel != "" || callerModelPassthroughActive(ctx) ||
		in.origin == policy.OverrideSourceDeployment || in.origin == policy.OverrideSourceRequest
	if billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return -1, subscriptionPoolExhausted(ctx)
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	paidModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, request, paidModels)
	paidCtx = billing.ReleaseLinkedFirst(paidCtx)
	paidCtx = context.WithValue(paidCtx, subscriptionAPIOnlyKey{}, true)
	target := in.initialDecision
	for {
		_, permitted := paidReq.AllowedModels[target.Model]
		if permitted && s.subscriptionStatePaidTargetAvailable(paidCtx, in.subscriptionStateHeaders, target) {
			break
		}
		if hasFixedTarget {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
		if permitted {
			request.ExcludedModels = mergeExcludedModels(request.ExcludedModels, map[string]struct{}{target.Model: {}})
			paidCtx, paidReq = s.subscriptionStateRequest(paidCtx, request, paidModels)
		}
		if len(paidReq.AllowedModels) == 0 {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
		var err error
		target, err = s.Route(paidCtx, paidReq)
		if err != nil {
			return -1, err
		}
	}
	observability.FromContext(ctx).Info("Included subscription targets unavailable; using exhausted model set", "model", target.Model, "provider", target.Provider)
	return s.dispatchSubscriptionStateTarget(paidCtx, in, target, false)
}

func (s *Service) subscriptionStatePaidTargetAvailable(ctx context.Context, headers http.Header, target router.Decision) bool {
	resolvedCredentials := s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, headers)
	if s.subscriptionAPIAuthorization(s.withSubscriptionFundingAuthorization(ctx, headers), target.Provider, target.Model) != nil {
		return false
	}
	return len(s.resolveBindingsForDispatch(resolvedCredentials, target)) > 0
}

func (s *Service) dispatchSubscriptionStateTarget(ctx context.Context, in failoverInputs, target router.Decision, usesIncludedSubscription bool) (int, error) {
	ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.subscriptionStateHeaders)
	resolvedTargetBindings := s.resolveBindingsForDispatch(ctx, target)
	if !usesIncludedSubscription && in.onSubscriptionStatePaidTarget != nil {
		in.onSubscriptionStatePaidTarget(target, resolvedTargetBindings)
	}
	attempt, err := in.buildAlternative(target)
	if err != nil {
		return -1, err
	}
	if in.buf != nil {
		in.buf.Discard()
	}
	in.initialDecision = target
	in.attempt = attempt
	in.bindings = resolvedTargetBindings
	if usesIncludedSubscription {
		// A subscription-capable binding cannot rotate onto a paid gateway.
		in.bindings = []catalog.ProviderBinding{{Provider: target.Provider}}
		in.deferFlushOnExhaustion = true
	}
	if in.onSubscriptionStateTarget != nil {
		in.onSubscriptionStateTarget(target, in.bindings)
	} else if in.onAlternative != nil {
		in.onAlternative(target)
	}
	winner, err := s.dispatchWithFallback(ctx, in)
	if err == nil && in.subscriptionStateWinnerProvider != nil && winner >= 0 && winner < len(resolvedTargetBindings) {
		*in.subscriptionStateWinnerProvider = resolvedTargetBindings[winner].Provider
	}
	return winner, err
}
