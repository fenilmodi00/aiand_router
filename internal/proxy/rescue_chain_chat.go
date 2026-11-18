package proxy

import (
	"context"
	"net/http"
	"strings"

	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/translate"
)

// chatRescue is the OpenAI Chat / Responses rescue chain: the shared sibling
// rescue ahead of the served-provider resolution.
type chatRescue struct {
	*rescueChain
	isResponses         bool
	responsesPreludeBuf *preludeBuffer
}

func (o *chatRescue) armRescues(ctx context.Context) context.Context {
	_, o.explicitRequestedModel = catalog.ByID(o.req.RequestedModel)
	return o.armSiblings(ctx, o.routeRes.CallerModelPassthrough || strings.HasPrefix(o.decision.Reason, translate.ReasonUserForceModel))
}

func (o *chatRescue) dispatchPrimary(ctx context.Context) {
	o.dispatchPrimaryAttempt(ctx, o.rescueArmed, nil)
}

// A released prelude can only take an SSE error frame, so every dispatch in
// the chain renders through this — a JSON envelope appended to a live stream
// is unparseable to the client.
func (o *chatRescue) flushErrAsOpenAI(w http.ResponseWriter, err error) {
	// Native Responses bytes are passed through verbatim; there is no
	// error frame we can synthesize into that stream.
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		_ = emitOpenAISSEErrorEvent(w, err)
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if isSubscriptionPoolError(err) {
		return
	}
	flushBufferedIfPresent(w, err)
}

// The deferred upstream error must reach the client exactly once: the rescue
// owns it, and flushes it itself when it declines to run. A managed-pool
// failure is rendered by the handler from the classified sentinel, so the
// proxy must not write it here as well.
func (o *chatRescue) flushDeferredErr() {
	if o.deferredErrFlushed {
		return
	}
	o.deferredErrFlushed = true
	if o.isResponses && o.responsesPreludeBuf != nil && o.responsesPreludeBuf.PreludeSent() {
		return
	}
	if o.env.Stream() && o.preludeBuf.PreludeSent() {
		// The handler cannot render anything once the client has bytes,
		// so an in-stream frame is the only way the turn reports at all —
		// including for a pool sentinel, which would otherwise truncate
		// the stream silently. Keep the sentinel as the returned error so
		// the handler still classifies and logs it.
		sseErr := emitOpenAISSEErrorEvent(o.contentSink, o.proxyErr)
		if !o.subscriptionPoolFailure {
			o.proxyErr = sseErr
		}
		return
	}
	// Pre-commit, the handler renders the classified sentinel itself.
	if o.subscriptionPoolFailure {
		return
	}
	flushBufferedIfPresent(o.contentSink, o.proxyErr)
}

func (o *chatRescue) finish(ctx context.Context) {
	// No rescuer served (none ran, or the last one failed pre-commit); surface
	// the held error now so it's never dropped.
	o.proxyErr = asCompactionUnservable(o.routeRes.TurnType, o.decision, o.proxyErr)
	o.proxyErr = withClientTrimCounts(ctx, o.proxyErr, o.rescuedPrimaryErr)
	if (o.siblingRescueOwed || o.overflowRescueOnly) && o.proxyErr != nil && !o.preludeBuf.Committed() {
		o.flushDeferredErr()
	}

	o.finalProvider = o.servedProvider(o.decision.Provider)
	o.decision.Provider = o.finalProvider
	o.failoverUsed = o.finalProvider != o.primaryProvider || o.siblingFailoverUsed
	o.failoverAttempted = o.failoverUsed || o.siblingRescueRan
}
