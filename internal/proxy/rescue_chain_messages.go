package proxy

import (
	"context"
	"net/http"

	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/flags"
	"aiand-os/router/internal/inference"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
)

// messagesRescue is the Anthropic Messages rescue chain: early-stop redo ahead
// of the shared sibling rescue.
type messagesRescue struct {
	*rescueChain
	feats           translate.RoutingFeatures
	agentShadowMode bool
	earlyStop       *dispatch.EarlyStopGate
	streamCut       *streamCutObserver
	attemptBuildErr error

	compactionHandoverOutcome handoverOutcome

	earlyStopMode         flags.EarlyStopRedoMode
	earlyStopTarget       router.Decision
	earlyStopCandidates   []string
	requestedWindowPassed bool

	earlyStopRedone      bool
	crossBindingRejected bool
	deadArmRejected      bool
}

func (m *messagesRescue) armRescues(ctx context.Context) context.Context {
	_, m.explicitRequestedModel = catalog.ByID(m.req.RequestedModel)
	return m.armSiblings(ctx, m.agentShadowMode || m.routeRes.CallerModelPassthrough || m.decision.Reason == translate.ReasonUserForceModel)
}

func (m *messagesRescue) armEarlyStop(ctx context.Context) {
	s := m.svc
	env := m.env
	routeRes := m.routeRes
	// A lower-tier model that ends a Claude Code tool-result turn without a
	// tool call is re-served on the turn's highest-tier candidate while the
	// client has seen nothing; shadow mode only logs that it would have been.
	m.earlyStopMode = s.earlyStopRedoMode(ctx, routeRes, m.decision, env.Stream(), m.agentShadowMode)
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		basis := rescueBasisForTurn(m.decision, routeRes)
		target, found := s.earlyStopRedoTarget(ctx, basis, m.overflowEstimate, env.SignatureTokenSavings(), m.outputReserve)
		if found && s.siblingRescueAllowed(ctx) && !paidFallbackForbiddenForModel(ctx, target.Model) {
			m.earlyStopTarget = target
			m.earlyStopCandidates = basis.Metadata.CandidateModels
			m.earlyStop.Observe(m.earlyStopMode == flags.EarlyStopRedoOn)
		} else {
			m.earlyStopMode = flags.EarlyStopRedoOff
		}
	}
}

func (m *messagesRescue) dispatchPrimary(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	env := m.env
	routeRes := m.routeRes
	feats := m.feats
	// A compaction turn is how the client recovers from overflow; answering it
	// with prompt-too-long would fail the compaction itself.
	m.requestedWindowPassed = routeRes.TurnType != turntype.Compaction &&
		pastRequestedWindow(feats.Model, m.decision.Model, m.overflowEstimate, m.outputReserve)
	if m.attemptBuildErr != nil {
		// Nothing was dispatched — enters the rescue chain as if every binding pre-committed failed.
		m.winnerIdx, m.proxyErr = -1, m.attemptBuildErr
	} else {
		m.dispatchPrimaryAttempt(ctx, m.rescueArmed, func(target router.Decision) {
			if !m.agentShadowMode && !routeRes.AuthoritativePerTurn && target.Provider != providers.ProviderAIAND && !routeRes.HardPinned && !routeRes.Handover.Invoked && routeRes.PrefixTrimmed {
				log.Info("Context trimming detected on selected exhausted-state route; rewriting context with handover summary", "decision_model", target.Model)
				m.compactionHandoverOutcome = s.runCompactionHandover(ctx, env, r.Header, target.Model, *m.req)
			}
		})
	}
}

// A released prelude can only take an SSE error frame, so every dispatch in
// the chain renders through this — a JSON envelope appended to a live stream
// is unparseable to the client.
func (m *messagesRescue) flushErrAsAnthropic(w http.ResponseWriter, err error) {
	env := m.env
	preludeBuf := m.preludeBuf
	err = asRequestedWindowOverflow(err, m.requestedWindowPassed, preludeBuf.Committed())
	if env.Stream() && preludeBuf.PreludeSent() {
		_ = emitAnthropicSSEErrorEvent(w, err)
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if isSubscriptionPoolError(err) {
		return
	}
	flushUpstreamErrorAsAnthropic(w, err)
}

func (m *messagesRescue) redoIfStoppedEarly(ctx context.Context) {
	s := m.svc
	r := m.r
	log := m.log
	routeRes := m.routeRes
	opts := m.opts
	preludeBuf := m.preludeBuf
	if m.earlyStopMode != flags.EarlyStopRedoOff {
		stopped := m.proxyErr == nil && m.earlyStop.EarlyStop()
		if releaseCause := m.earlyStop.ReleaseReason(); !stopped && releaseCause != "" {
			log.Debug("Early-stop gate released the turn", "release_cause", releaseCause, "served_model", m.decision.Model)
		}
		if stopped {
			log.Info("Tool-result turn ended without a tool call below the top candidate tier",
				"served_model", m.decision.Model,
				"redo_model", m.earlyStopTarget.Model,
				"early_stop_redo_mode", string(m.earlyStopMode),
				"withheld", m.earlyStop.Withheld())
		}
		if stopped && m.earlyStop.Withheld() && !preludeBuf.Committed() {
			m.earlyStopRedone, m.proxyErr = s.redoEarlyStop(ctx, earlyStopRedo{
				gate:    m.earlyStop,
				prelude: preludeBuf,
				header:  m.contentSink.Header(),
				log:     log,
				target:  m.earlyStopTarget,
				build: func(buildCtx context.Context, target router.Decision) (dispatchAttempt, []catalog.ProviderBinding, string, effortResolution, context.Context, error) {
					redoOpts := opts
					redoOpts.TargetModel = target.Model
					redoOpts.TargetProvider = target.Provider
					redoOpts.Capabilities = router.Lookup(target.Model)
					redoOpts.ModelSwitched = true
					redoEffort := s.resolveEffort(buildCtx, target, redoOpts.Capabilities, routeRes.EscalateEffort)
					redoEffort.apply(&redoOpts)
					redoCtx := s.resolveCredentials(subscriptionStatePaidRescueContext(buildCtx, target.Model), target.Provider, r.Header)
					redoMarker := suppressMarkerIfRequested(buildCtx, r.Header, modelSelectionMarkerForRequest(buildCtx, routeRes, siblingRoutingMarkerFor(routeRes, m.decision.Model, target.Model), target.Model, markerReasonEarlyStop))
					redoAttempt, buildErr := m.buildAttempt(target, redoOpts, redoMarker)
					return redoAttempt, s.resolveBindingsForDispatch(redoCtx, target), redoMarker, redoEffort, redoCtx, buildErr
				},
				dispatch: func(redoCtx context.Context, target router.Decision, redoBindings []catalog.ProviderBinding, redoAttempt dispatchAttempt) (int, error) {
					return s.dispatchWithFallback(redoCtx, failoverInputs{
						w:                      m.contentSink,
						buf:                    preludeBuf,
						initialDecision:        target,
						bindings:               redoBindings,
						attempt:                redoAttempt,
						flushErr:               m.flushErrAsAnthropic,
						deferFlushOnExhaustion: true,
						purpose:                routeRes.dispatchPurpose(inference.PurposeAnthropicMessages),
						origin:                 routeRes.dispatchOrigin(target),
					})
				},
				served: &earlyStopServed{
					decision:  &m.decision,
					bindings:  &m.bindings,
					marker:    &m.marker,
					winnerIdx: &m.winnerIdx,
					summary:   m.respSummary,
					reqStats:  m.reqStats,
					effort:    &m.effortServed,
				},
			})
			if m.earlyStopRedone {
				recordTurnFailure(ctx, m.primaryModel)
				for _, model := range lowerTierCandidates(m.earlyStopTarget.Model, m.earlyStopCandidates) {
					recordTurnFailure(ctx, model)
				}
			}
		}
		if releaseErr := m.earlyStop.Disarm(); releaseErr != nil && m.proxyErr == nil {
			m.proxyErr = releaseErr
		}
	}
}

// The deferred upstream error must reach the client exactly once: each
// rescue hands ownership to the next, and whichever declines to run flushes.
// Writing it forecloses any later rescue.
func (m *messagesRescue) flushDeferredErr() {
	env := m.env
	preludeBuf := m.preludeBuf
	if m.deferredErrFlushed {
		return
	}
	m.deferredErrFlushed = true
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())
	if preludeBuf.Committed() {
		// Before synthesis, for the same reason as the native path.
		m.streamCut.noteCut(m.proxyErr)
	}
	if env.Stream() && preludeBuf.PreludeSent() {
		// The handler cannot render anything once the client has bytes,
		// so an in-stream frame is the only way the turn reports at all —
		// including for a pool sentinel, which would otherwise truncate
		// the stream silently. Keep the sentinel as the returned error so
		// the handler still classifies and logs it.
		sseErr := emitAnthropicSSEErrorEvent(m.contentSink, m.proxyErr)
		if !m.subscriptionPoolFailure {
			m.proxyErr = sseErr
		}
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if m.subscriptionPoolFailure {
		return
	}
	flushUpstreamErrorAsAnthropic(m.contentSink, m.proxyErr)
}

func (m *messagesRescue) classifyPrimaryFailure() {
	// A cross-binding rescuable 400: another model's compiler accepts the
	// schemas, or another model ignores the Gemini thoughtSignature Gemini 3.x
	// rejected in the history.
	m.crossBindingRejected = providers.IsUpstreamSchemaRejection(m.proxyErr) || providers.IsUpstreamThoughtSignatureRejection(m.proxyErr)
	// A provably-dead-arm rejection (schema/capability/intrinsically-incompatible)
	// is snapshotted before any rescue runs — the rescue nils proxyErr on
	// success, which would otherwise hide the rejection from the post-rescue
	// pin-eviction decision below.
	m.deadArmRejected = m.crossBindingRejected || translate.IsIntrinsicallyIncompatible(m.proxyErr)
}

func (m *messagesRescue) finish(ctx context.Context) {
	routeRes := m.routeRes
	preludeBuf := m.preludeBuf
	// No rescuer served (none ran, or the last one failed pre-commit); surface
	// the held error now so it's never dropped.
	m.proxyErr = asCompactionUnservable(routeRes.TurnType, m.decision, m.proxyErr)
	m.proxyErr = withClientTrimCounts(ctx, m.proxyErr, m.rescuedPrimaryErr)
	if m.siblingRescueOwed && m.proxyErr != nil && !preludeBuf.Committed() {
		m.flushDeferredErr()
	}
	m.proxyErr = asRequestedWindowOverflow(m.proxyErr, m.requestedWindowPassed, preludeBuf.Committed())

	m.finalProvider = m.servedProvider(m.primaryProvider)
	m.decision.Provider = m.finalProvider
	m.failoverUsed = m.finalProvider != m.primaryProvider || m.siblingFailoverUsed || m.earlyStopRedone
	m.failoverAttempted = m.failoverUsed || m.siblingRescueRan
}
