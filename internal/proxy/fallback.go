package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/inference"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/catalog"
	"aiand-os/router/internal/router/cluster"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/sse"
	"aiand-os/router/internal/subscriptions/entitlement"
	"aiand-os/router/internal/translate"
)

// preludeBuffer and newPreludeBuffer keep the entrypoints outside Inference
// Execution compiling against the buffer while they still construct it here.
type preludeBuffer = dispatch.PreludeBuffer

func newPreludeBuffer(w http.ResponseWriter) *preludeBuffer { return dispatch.NewPreludeBuffer(w) }

// dispatchAttempt does one per-binding dispatch. Returns the upstream error
// unmodified so dispatchWithFallback can decide whether to retry.
type dispatchAttempt func(ctx context.Context, decision router.Decision, p providers.Client) error

// failoverInputs bundles the inputs dispatchWithFallback needs that don't
// belong to a single attempt.
type failoverInputs struct {
	// w is the real client writer; flushErr writes here directly (bypassing
	// buf) so the client sees the upstream error in its expected format.
	w http.ResponseWriter
	// buf is the writer per-attempt code writes through; its Committed()
	// bit gates retry. nil when failover is impossible (single binding,
	// BYOK, legacy mode) — the loop then skips discard/commit entirely.
	buf *dispatch.PreludeBuffer
	// initialDecision carries the model + cluster metadata from the
	// router. dispatchWithFallback rewrites Provider per-attempt.
	initialDecision router.Decision
	// bindings is the ordered (provider, upstream-id, price) list, filtered
	// to providers wired in this deploy. Index 0 is primary, >0 fallbacks.
	bindings []catalog.ProviderBinding
	// attempt does one per-binding dispatch.
	attempt dispatchAttempt
	// flushErr renders the final-attempt upstream error to w in the
	// entry point's wire format. Optional — nil means do nothing on
	// exhaustion (the error is still returned to the caller).
	flushErr func(w http.ResponseWriter, err error)
	// deferFlushOnExhaustion suppresses flushErr on exhaustion (still
	// discards the buffer and returns the error), letting the caller run a
	// higher-level fallback instead (e.g. ProxyMessages' baseline failover
	// re-dispatching the Anthropic model when a routed OSS model exhausts).
	deferFlushOnExhaustion bool
	// purpose names the registered operation the walk is authorized under;
	// origin names the override source that fixed the decision's model.
	purpose                         inference.Purpose
	origin                          policy.OverrideSource
	subscriptionStateRequest        *router.Request
	subscriptionStateHeaders        http.Header
	alternatives                    []router.Decision
	buildAlternative                func(router.Decision) (dispatchAttempt, error)
	onAlternative                   func(router.Decision)
	onSubscriptionStateTarget       func(router.Decision, []catalog.ProviderBinding)
	onSubscriptionStatePaidTarget   func(router.Decision, []catalog.ProviderBinding)
	subscriptionStateWinnerProvider *string
}

// errDispatchWithoutPurpose rejects a walk no registered purpose authorizes:
// every dispatch must resolve a plan before bytes reach a provider.
var errDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

// dispatchWithFallback authorizes the routed decision under in.purpose and
// runs the attempt closure against each binding in order through the
// dispatch executor, retrying on providers.IsRetryable errors while no bytes
// have reached the client. Returns the winning (or last-tried) index and
// error. On final-attempt error it flushes the upstream's own envelope to w
// instead of a generic 502.
func (s *Service) dispatchWithFallback(ctx context.Context, in failoverInputs) (winnerIdx int, err error) {
	ctx = s.withSubscriptionFundingAuthorization(ctx, in.subscriptionStateHeaders)
	if in.subscriptionStateRequest != nil && subscriptionStateModelsEnabled(ctx) && in.buildAlternative != nil {
		return s.dispatchSubscriptionStateModels(ctx, in)
	}
	if subscriptionStateModelsEnabled(ctx) && !subscriptionAttemptOnly(ctx) {
		if _, allowed := modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx))[in.initialDecision.Model]; !allowed {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
	}
	if len(in.purpose) == 0 {
		return -1, errDispatchWithoutPurpose
	}
	// Terminal gate on the product boundary. Candidate filtering already
	// dropped ineligible models, so this only catches a decision minted
	// outside the routed pool — a session pin, a hard-pinned auxiliary turn,
	// a /force-model, a fallback table. Refusing beats serving what the plan
	// does not sell.
	if err := catalog.CheckEligibility(entitlement.ModelBoundaryFromContext(ctx), in.initialDecision.Model); err != nil {
		return -1, err
	}
	if len(in.bindings) == 0 {
		if in.initialDecision.Reason == translate.ReasonUserForceModel {
			err := &providers.UpstreamErrorResponse{
				Status: http.StatusServiceUnavailable,
				Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
					fmt.Sprintf("forced model %s unavailable: provider %s not configured", in.initialDecision.Model, in.initialDecision.Provider))),
			}
			if in.flushErr != nil && !in.deferFlushOnExhaustion {
				in.flushErr(in.w, err)
			}
			return -1, err
		}
		return -1, &providers.UpstreamStatusError{Status: http.StatusBadGateway}
	}
	plans, err := s.inferencePlans()
	if err != nil {
		return -1, err
	}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{
		Purpose:  policy.Purpose(in.purpose),
		Decision: in.initialDecision,
		Bindings: in.bindings,
		Origin:   in.origin,
	})
	if err != nil {
		return -1, err
	}
	return s.dispatchPlanned(ctx, in, plan)
}

// committed is a nil-safe shorthand for in.buf.Committed(); the
// single-binding fast path passes buf=nil and is treated as not committed.
func committed(b *dispatch.PreludeBuffer) bool {
	if b == nil {
		return false
	}
	return b.Committed()
}

// subscriptionRotationBudget bounds how long included-capacity accounts and
// model alternatives may be leased and admitted, shared across one request's
// rotation walk. It never cancels an attempt already in flight: provider
// header/stream watchdogs and the caller own inference lifetime.
const subscriptionRotationBudget = 10 * time.Second

func (s *Service) WithClock(clock auth.Clock) *Service {
	s.clock = clock
	return s
}

// shouldFailover reports failover eligibility. Customer-supplied
// credentials (BYOK or client key) bind to a single provider — retrying
// elsewhere would 401 unexpectedly, so we skip failover. A BYOK row for ANY
// provider disables it too: a failover binding re-resolves credentials and
// would prefer that key even if the primary binding used the deployment key.
func (s *Service) shouldFailover(ctx context.Context) bool {
	if s.byokOnly {
		return false
	}
	if CredentialsFromContext(ctx) != nil {
		return false
	}
	if len(externalKeysFromContext(ctx)) > 0 {
		return false
	}
	return true
}

// resolveBindingsForDispatch returns the ordered binding list to walk.
// When failover is disabled or unavailable, returns a single-element
// slice carrying the already-resolved decision provider.
func (s *Service) resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	req := dispatch.BindingRequest{
		Decision:   decision,
		ImageCount: requestImageCountFromContext(ctx),
		UserForced: isUserForcedReason(decision.Reason),
		ForceModel: decision.Reason == translate.ReasonUserForceModel,
	}
	if s.shouldFailover(ctx) && s.deploymentKeyedProviders != nil {
		req.DeploymentKeyedProviders = s.deploymentKeyedProviders
	}
	return dispatch.OrderedBindings(req)
}

// flushBufferedIfPresent writes an *UpstreamErrorResponse through to the
// client verbatim. No-op for other error types.
//
// Content-Length is dropped: MaxBufferedErrorBytes caps the body we hold, so
// the upstream's advertised length may exceed what we actually write, breaking
// HTTP framing. net/http recomputes Content-Length itself. Content-Encoding is
// kept because these are still the upstream's bytes.
func flushBufferedIfPresent(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	// An overflow is left to the ingress handler, which answers in the
	// client's native prompt-too-long shape.
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	copyReplayedErrorHeaders(w.Header(), resp.Headers, false)
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// copyReplayedErrorHeaders forwards an upstream error's headers, keeping the
// body's encoding and type only while the bytes written are still the
// upstream's: once a flush re-renders the body, those headers would mislabel
// fresh JSON as the upstream's encoded payload.
func copyReplayedErrorHeaders(dst, upstream http.Header, bodyRewritten bool) {
	for k, vs := range upstream {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop || canon == "Content-Length" {
			continue
		}
		if bodyRewritten && (canon == "Content-Encoding" || canon == "Content-Type") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// emitAnthropicSSEErrorEvent writes an Anthropic-shape `event: error` SSE
// frame and returns *UpstreamStatusError so flushErr becomes a no-op.
// Used when the upstream errors after Prelude already committed HTTP 200 +
// message_start — a JSON error envelope would corrupt the SSE stream, but
// an `event: error` frame terminates cleanly.
func emitAnthropicSSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream stream failed"}}`)
	switch cls, classified := ClassifyDispatchError(err); {
	case classified && cls.Kind == DispatchErrorContextWindowExceeded:
		status = cls.Status
		body = anthropicErrorFrameBody(cls)
	case errors.As(err, &resp):
		status = resp.Status
		body = translate.OpenAIToAnthropicError(resp.Body)
	case classified:
		status = cls.Status
		body = anthropicErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("event: error\ndata: "))
	_, _ = sse.WriteJSONChunk(sink, body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status, Cause: err}
}

// emitOpenAISSEErrorEvent is emitAnthropicSSEErrorEvent's OpenAI-shape
// counterpart: writes a verbatim `data: {...}` frame, used after
// OpenAIRoutingMarkerWriter has already committed HTTP 200.
func emitOpenAISSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body := []byte(`{"error":{"message":"upstream stream failed","type":"server_error","code":"upstream_error"}}`)
	switch cls, classified := ClassifyDispatchError(err); {
	case classified && cls.Kind == DispatchErrorContextWindowExceeded:
		status = cls.Status
		body = openAIErrorFrameBody(cls)
	case errors.As(err, &resp):
		status = resp.Status
		body = resp.Body
	case classified:
		status = cls.Status
		body = openAIErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("data: "))
	_, _ = sse.WriteJSONChunk(sink, body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status, Cause: err}
}

// anthropicErrorFrameBody renders a classified dispatch error as an
// Anthropic error body. The ingress handler cannot write its own envelope
// once the stream is committed, so the frame carries the class's own message
// rather than the generic upstream-failure text.
func anthropicErrorFrameBody(cls DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`,
		errorFrameType(cls), cls.Message))
}

// openAIErrorFrameBody is anthropicErrorFrameBody's OpenAI-shape counterpart.
func openAIErrorFrameBody(cls DispatchErrorClass) []byte {
	code := OpenAIErrorCode(cls.Kind)
	if code == "" {
		code = "upstream_error"
	}
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"code":%q}}`,
		cls.Message, errorFrameType(cls), code))
}

func errorFrameType(cls DispatchErrorClass) string {
	if cls.Kind.IsClientError() {
		return "invalid_request_error"
	}
	return "api_error"
}

// flushUpstreamErrorAsAnthropic is ProxyMessages' flushErr: translates the
// OpenAI-compat upstream's error body to Anthropic-shape JSON. No-op when
// err is not an *UpstreamErrorResponse.
func flushUpstreamErrorAsAnthropic(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) || isContextOverflow(err) {
		return
	}
	body := translate.OpenAIToAnthropicError(resp.Body)
	copyReplayedErrorHeaders(w.Header(), resp.Headers, !bytes.Equal(body, resp.Body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(body)
}

// attemptIdxLabel formats i for the x-router-fallback-attempt header.
// Caller gates on i > 0.
func attemptIdxLabel(i int) string {
	return strconv.Itoa(i)
}

type subscriptionOnlyAttemptKey struct{}
type subscriptionAPIOnlyKey struct{}

func subscriptionAttemptOnly(ctx context.Context) bool {
	value, _ := ctx.Value(subscriptionOnlyAttemptKey{}).(bool)
	return value
}
func subscriptionAPIOnly(ctx context.Context) bool {
	value, _ := ctx.Value(subscriptionAPIOnlyKey{}).(bool)
	return value
}
