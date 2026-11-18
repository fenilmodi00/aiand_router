package proxy

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/hmm"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
)

const compactionSessionPinTier = "compaction_session_pin"

// routeCompaction serves a client's own compaction turn on the model serving
// the session, whose prompt cache is warm, and otherwise scores it like any
// other turn. A utility hard pin is never a fallback: its fixed roster may be
// excluded or too small for the transcript, which left the client retrying a
// compaction that could not route.
func (s *Service) routeCompaction(
	ctx context.Context,
	req router.Request,
	res turnLoopResult,
	reqHeaders http.Header,
	sessionKey [sessionpin.SessionKeyLen]byte,
	installationID uuid.UUID,
	forceModelFound bool,
	forceModelPin sessionpin.Pin,
) (turnLoopResult, error) {
	if s.pinStore == nil {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	res.SessionKey = sessionKey
	if forceModelFound && forcedPinEligible(forceModelPin, req) {
		return s.routeWithoutPin(ctx, req, res, reqHeaders, forceModelFound, forceModelPin)
	}
	session := compactionSession{
		pins:      s.pinStore,
		providers: s.clients.NameSet(),
		available: s.availableModels,
		now:       s.clock.Now(),
	}
	decision, strikes, ok := session.decision(ctx, sessionKey, res.PinRole, req)
	req = strikes.apply(req)
	if ok {
		// The scorer's roster gives the session model sibling rescue candidates.
		scoreReq := req
		scoreReq.PolicyTurnContext = buildPolicyTurnContext(req, res, sessionpin.Pin{}, sessionpin.Pin{}, s.clock.Now())
		if fresh, err := s.routeFor(ctx, scoreReq); err == nil {
			res.Fresh = fresh
		}
		res.Decision = decision
		res.StickyHit = true
		res.PinModel = decision.Model
		res.PinTier = compactionSessionPinTier
		res.Origin = policy.OverrideSourceSession
		return res, nil
	}
	res, err := s.routeWithoutPin(ctx, req, res, reqHeaders, false, sessionpin.Pin{})
	if err != nil || res.CallerModelPassthrough {
		return res, err
	}
	s.writeNewPin(ctx, installationID, sessionKey, res.PinRole, res.Decision)
	return res, nil
}

// compactionSession decides which session model a client compaction turn
// stays on: the model that last served the session, when this request can
// still use it. The active thread pin and HMM history compete by completion
// time; both are read across strategies because a compaction may arrive under
// a different strategy than the turns it summarizes.
type compactionSession struct {
	pins sessionpin.Store
	// providers binds the served model when the request leaves
	// EnabledProviders unrestricted.
	providers map[string]struct{}
	available map[string]struct{}
	now       time.Time
}

// sessionStrikes are the providers and models a session's failures withdrew
// from automatic selection.
type sessionStrikes struct {
	disabledProviders []string
	demotedModels     []string
}

// apply withdraws the strikes from the request so the scorer cannot reselect
// them.
func (st sessionStrikes) apply(req router.Request) router.Request {
	if req.EnabledProviders != nil && len(st.disabledProviders) > 0 {
		enabled := maps.Clone(req.EnabledProviders)
		for _, provider := range st.disabledProviders {
			delete(enabled, provider)
		}
		req.EnabledProviders = enabled
	}
	if len(st.demotedModels) > 0 {
		demoted := make(map[string]struct{}, len(st.demotedModels))
		for _, model := range st.demotedModels {
			demoted[model] = struct{}{}
		}
		req.AutomaticExcludedModels = mergeExcludedModels(req.AutomaticExcludedModels, demoted)
	}
	return req
}

func (c compactionSession) decision(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (router.Decision, sessionStrikes, bool) {
	var served sessionpin.Pin
	var strikes sessionStrikes
	for _, pinRole := range []string{role, hmmHistoryRole(role)} {
		pin, found, err := c.pins.Get(ctx, sessionKey, pinRole)
		if err != nil {
			observability.FromContext(ctx).Error("Compaction session history unavailable", "role", pinRole, "err", err)
			continue
		}
		if !found {
			continue
		}
		strikes.disabledProviders = mergeSessionStrikes(strikes.disabledProviders, pin.DisabledProviders)
		strikes.demotedModels = mergeSessionStrikes(strikes.demotedModels, pin.DemotedModels)
		if pinRole == role && !pin.PinnedUntil.After(c.now) {
			continue
		}
		served = latestServedPin(served, pin)
	}
	model := served.LastServedModel
	if model == "" {
		model = served.Model
	}
	model, effort := hmm.SplitEffort(model)
	if model == "" {
		return router.Decision{}, strikes, false
	}
	if effort == "" && model == served.Model {
		effort = served.Effort
	}
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = c.providers
	}
	binding, bound := servedBinding(model, served.Provider, providerSet, req.CustomBindings)
	if !bound {
		return router.Decision{}, strikes, false
	}
	if c.available != nil {
		if _, available := c.available[model]; !available {
			return router.Decision{}, strikes, false
		}
	}
	pin := sessionpin.Pin{Provider: binding.Provider, Model: model, Effort: effort, Reason: compactionSessionPinTier}
	fits := req.EstimatedInputTokens <= 0 ||
		req.EstimatedInputTokens+contextWindowOutputReserve <= contextWindowForRequest(model, binding.Provider)
	struck := slices.Contains(strikes.disabledProviders, binding.Provider) || slices.Contains(strikes.demotedModels, model)
	if !fits || struck || !automaticPinEligible(pin, req) {
		return router.Decision{}, strikes, false
	}
	return pinDecision(pin), strikes, true
}

// latestServedPin picks the pin whose last turn ended most recently among
// those that record a served model.
func latestServedPin(pins ...sessionpin.Pin) sessionpin.Pin {
	var latest sessionpin.Pin
	found := false
	for _, pin := range pins {
		if pin.LastServedModel == "" && pin.Model == "" {
			continue
		}
		if !found || pin.LastTurnEndedAt.After(latest.LastTurnEndedAt) {
			latest = pin
			found = true
		}
	}
	return latest
}

// servedBinding resolves the provider binding for a session's served model:
// the provider that served it when that provider is still in providerSet and
// binds the model, else the first binding in providerSet (a failed turn may
// leave the stored provider stale).
func servedBinding(model, servedProvider string, providerSet map[string]struct{}, custom map[string][]string) (catalog.ProviderBinding, bool) {
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, custom); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, custom)
}
