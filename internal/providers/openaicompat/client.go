// Package openaicompat is a generic providers.Client adapter for upstreams that speak OpenAI Chat Completions.
package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"aiand-os/router/internal/observability"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/providers/httputil"
	"aiand-os/router/internal/requestcontext"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/timing"
)

const (
	// AIANDBaseURL is ai&'s OpenAI-compatible Chat Completions surface.
	AIANDBaseURL = "https://api.aiand.com/v1"
)

type Client struct {
	apiKey    string
	baseURL   string
	http      *http.Client
	modelHTTP *http.Client
	// modelIDMap rewrites the request body's "model" field before sending, when
	// the router's public slug differs from the upstream's canonical ID
	// (ai& publishes vendor-prefixed IDs). Nil/empty = no rewrite.
	modelIDMap map[string]string
	// sseIdleTimeout overrides httputil.DefaultSSEIdleTimeout when > 0; tests
	// set it small so the output-stall watchdog fires before this one.
	sseIdleTimeout time.Duration
	// outputStall overrides httputil.DefaultOutputStallTimeout when > 0; used
	// by tests to trip output-stall without waiting out the real budget.
	outputStall time.Duration
	// throughputWindow/MinElapsed/MinDeltas override the minimum-throughput
	// watchdog budgets when set; used by tests to trip slow-throughput without
	// waiting out the real warmup.
	throughputWindow     time.Duration
	throughputMinElapsed time.Duration
	throughputMinDeltas  int
	throughputOverride   bool
	// defaultHeaders are set on every upstream request before prep.Headers
	// apply, so per-request values override them.
	defaultHeaders http.Header
	// protectedHeaders are set after prep.Headers / inbound headers apply, so
	// provider-mandated values cannot be overridden.
	protectedHeaders http.Header
	// versionMemo resolves gateway base URLs that mount chat/completions one
	// "/v1" below where the stored base URL points.
	versionMemo providers.GatewayVersionMemo
}

// Option configures a Client at construction.
type Option func(*Client)

// WithModelListHTTPClient supplies the client used only for model discovery.
// A nil client is ignored so a misconfigured option cannot strip the
// constructor's destination-checked default and panic on the first call.
func WithModelListHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client == nil {
			return
		}
		c.modelHTTP = client
	}
}

func NewClient(apiKey, baseURL string, opts ...Option) *Client {
	return NewClientWithModelIDMap(apiKey, baseURL, nil, opts...)
}

// NewClientWithModelIDMap builds a client that rewrites the body's top-level
// "model" field before forwarding when the requested model has a mapping.
// Pass nil to disable rewriting.
func NewClientWithModelIDMap(apiKey, baseURL string, modelIDMap map[string]string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = AIANDBaseURL
	}
	return newClient(apiKey, baseURL, modelIDMap, opts...)
}

// NewGatewayClient builds a client for a customer-supplied OpenAI-spec
// endpoint. Unlike NewClient, no default base URL is applied: an unconfigured
// BYOK endpoint must fail rather than silently dispatch the tenant's token to
// the deployment's upstream.
func NewGatewayClient(apiKey, baseURL string, opts ...Option) *Client {
	return newClient(apiKey, baseURL, nil, opts...)
}

func newClient(apiKey, baseURL string, modelIDMap map[string]string, opts ...Option) *Client {
	client := &Client{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		http:       httputil.NewClient(httputil.NewTransport(5*time.Second, 5*time.Second)),
		modelHTTP:  httputil.NewDefaultModelDiscoveryClient(),
		modelIDMap: modelIDMap,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// DeploymentPrincipal fingerprints the account this client's own key
// authenticates as; see providers.DeploymentPrincipal.
func (c *Client) DeploymentPrincipal() string {
	return providers.CredentialPrincipal(c.baseURL, c.apiKey)
}

// WithDefaultHeaders returns c with headers set on every upstream request.
// Prepared and inbound per-request headers can override these values.
func (c *Client) WithDefaultHeaders(h http.Header) *Client {
	c.defaultHeaders = h.Clone()
	return c
}

// WithProtectedHeaders returns c with headers that cannot be overridden by
// prepared or inbound per-request headers.
func (c *Client) WithProtectedHeaders(h http.Header) *Client {
	c.protectedHeaders = h.Clone()
	return c
}

// applyDefaultHeaders sets c.defaultHeaders on req before callers layer their
// own headers on top.
func (c *Client) applyDefaultHeaders(req *http.Request) {
	for k, vs := range c.defaultHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

// applyProtectedHeaders restores c.protectedHeaders after callers layer their
// own headers.
func (c *Client) applyProtectedHeaders(req *http.Request) {
	for k, vs := range c.protectedHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

// NewClientWithHeaderTimeouts is NewClient with an injected header-timeout
// value so tests can exercise transport selection without real-latency waits.
func NewClientWithHeaderTimeouts(apiKey, baseURL string, defaultTimeout time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(5*time.Second, 5*time.Second, defaultTimeout))
	return c
}

// NewClientWithStallTimeouts is NewClient with injected byte-idle and
// output-stall watchdog budgets. Exists so a test can drive the
// output-progress watchdog with a small budget while keeping the byte-idle
// watchdog large enough that it isn't what fires.
func NewClientWithStallTimeouts(apiKey, baseURL string, sseIdleTimeout, outputStall time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.sseIdleTimeout = sseIdleTimeout
	c.outputStall = outputStall
	return c
}

// NewClientWithHeaderAndStallTimeouts is NewClient with injected
// header-timeout, byte-idle and output-stall budgets, so a test can exercise
// every upstream watchdog at once.
func NewClientWithHeaderAndStallTimeouts(apiKey, baseURL string, headerTimeout, sseIdleTimeout, outputStall time.Duration) *Client {
	c := NewClientWithHeaderTimeouts(apiKey, baseURL, headerTimeout)
	c.sseIdleTimeout = sseIdleTimeout
	c.outputStall = outputStall
	return c
}

// NewClientWithTimeouts is NewClient with injected header-timeout and
// byte-idle budgets, so a test can exercise the upstream watchdog without
// real-latency waits.
func NewClientWithTimeouts(apiKey, baseURL string, headerTimeout, sseIdleTimeout time.Duration) *Client {
	c := NewClientWithHeaderTimeouts(apiKey, baseURL, headerTimeout)
	c.sseIdleTimeout = sseIdleTimeout
	return c
}

// idleTimeout returns the byte-idle watchdog budget: the injected test override
// when set, else httputil.DefaultSSEIdleTimeout.
func (c *Client) idleTimeout() time.Duration {
	if c.sseIdleTimeout > 0 {
		return c.sseIdleTimeout
	}
	return httputil.DefaultSSEIdleTimeout
}

// outputStallTimeout returns the output-progress watchdog budget: the injected
// test override when set, else httputil.DefaultOutputStallTimeout.
func (c *Client) outputStallTimeout() time.Duration {
	if c.outputStall > 0 {
		return c.outputStall
	}
	return httputil.DefaultOutputStallTimeout
}

// throughputParams returns the minimum-throughput watchdog budgets: the injected
// test overrides when set, else the httputil defaults.
func (c *Client) throughputParams() (window, minElapsed time.Duration, minDeltas int) {
	if c.throughputOverride {
		return c.throughputWindow, c.throughputMinElapsed, c.throughputMinDeltas
	}
	return httputil.DefaultThroughputWindow, httputil.DefaultThroughputMinElapsed, httputil.DefaultMinThroughputDeltasPerWindow
}

// NewClientWithThroughputGuard is NewClient with injected minimum-throughput
// watchdog budgets, so a test can drive a slow-throughput trip with a tiny
// warmup/window while keeping the idle and output-stall watchdogs large enough
// that they aren't what fire.
func NewClientWithThroughputGuard(apiKey, baseURL string, window, minElapsed time.Duration, minDeltas int) *Client {
	c := NewClient(apiKey, baseURL)
	c.throughputOverride = true
	c.throughputWindow = window
	c.throughputMinElapsed = minElapsed
	c.throughputMinDeltas = minDeltas
	return c
}

// rewriteModelField rewrites the body's top-level "model" field according to
// modelIDMap. Returns the input unchanged when the map is empty, the body
// isn't a JSON object, or the model isn't mapped.
func rewriteModelField(body []byte, modelIDMap map[string]string) []byte {
	if len(modelIDMap) == 0 || len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	upstream, ok := modelIDMap[model]
	if !ok {
		return body
	}
	out, err := sjson.SetBytes(body, "model", upstream)
	if err != nil {
		return body
	}
	return out
}

// effectiveBaseURL resolves this request's base URL, preferring the BYOK
// credential's own endpoint over the deployment-level one.
func (c *Client) effectiveBaseURL(ctx context.Context) string {
	return requestcontext.EffectiveBaseURL(ctx, c.baseURL)
}

// setAuth sets the Authorization header, preferring BYOK credentials over the deployment-level key.
func (c *Client) setAuth(ctx context.Context, upstream *http.Request) {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		upstream.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
		return
	}
	if c.apiKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *Client) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	body := rewriteModelField(prep.Body, c.modelIDMap)
	// Applied after the catalog map so a BYOK endpoint's own naming wins.
	body = requestcontext.ApplyModelAlias(ctx, body, decision.Model)
	baseURL := c.effectiveBaseURL(ctx)

	// EndpointResponses is the Responses surface: reasoning models reject a tool
	// turn on chat/completions, and gateways that mount /v1/responses (Snowflake
	// Cortex) serve it there. The proxy re-emits onto chat/completions when the
	// gateway answers that it has no such surface.
	suffix := "/chat/completions"
	if prep.Endpoint == providers.EndpointResponses {
		suffix = "/responses"
	}

	// 404s are buffered before reaching w, so a missing "/v1" segment can be retried.
	// A non-404 on the versioned path is the real error and is memoized; only a
	// second 404 falls back to the probe so a genuine model-not-found is preserved.
	urls := c.versionMemo.URLs(baseURL, suffix)
	firstErr := c.proxyTo(ctx, cancel, urls[0], baseURL, body, decision, prep, w, r)
	if len(urls) == 1 || !providers.IsUpstreamModelNotFound(firstErr) {
		return firstErr
	}
	err := c.proxyTo(ctx, cancel, urls[1], baseURL, body, decision, prep, w, r)
	if err == nil || !providers.IsUpstreamModelNotFound(err) {
		c.versionMemo.Learn(baseURL)
		return err
	}
	return firstErr
}

func (c *Client) proxyTo(ctx context.Context, cancel context.CancelCauseFunc, url, baseURL string, body []byte, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	c.applyDefaultHeaders(upstream)
	upstream.Header.Set("Content-Type", "application/json")
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.setAuth(ctx, upstream)
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("Accept"); v != "" {
		upstream.Header.Set("Accept", v)
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer resp.Body.Close()
	t.StampUpstreamHeaders()

	if resp.StatusCode >= 400 {
		// Buffer the error — do NOT touch w; the failover loop decides whether
		// to retry or flush this buffer to the client.
		bufBody, totalRead, drainErr := httputil.ReadCapped(resp.Body, providers.MaxBufferedErrorBytes)
		if len(bufBody) > 0 {
			t.StampUpstreamFirstByte()
		}
		if drainErr == nil {
			t.StampUpstreamEOF()
		}
		httputil.LogUpstreamStatus(
			ctx,
			"Upstream OpenAI-compatible provider returned error status",
			resp.StatusCode,
			"base_url", baseURL,
			"routed_model", decision.Model,
			"body_preview", httputil.PreviewBytes(bufBody),
			"body_total_bytes", totalRead,
		)
		errHeaders := http.Header{}
		providers.CopyUpstreamHeaders(httputil.HeaderCapture{H: errHeaders}, resp)
		return &providers.UpstreamErrorResponse{
			Status:     resp.StatusCode,
			Headers:    errHeaders,
			Body:       bufBody,
			BodyBytes:  totalRead,
			BodyCapped: totalRead > int64(len(bufBody)),
		}
	}

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	// Output callbacks feed stall, latency, and throughput tracking. Responses
	// reasoning feeds only the stall clock; buffered/no-hook writers decline.
	if arm, ok := w.(providers.OutputProgressArmer); ok {
		outMark, outStop := httputil.StartIdleWatchdogCause(ctx, cancel, c.outputStallTimeout(), httputil.ErrUpstreamOutputStall)
		tpWindow, tpMinElapsed, tpMinDeltas := c.throughputParams()
		tpMark, tpStop := httputil.StartThroughputWatchdog(ctx, cancel, tpWindow, tpMinElapsed, tpMinDeltas, httputil.ErrUpstreamSlowThroughput)
		combined := func() {
			outMark()
			tpMark()
		}
		if arm.ArmOutputProgress(timing.FirstOutputMark(ctx, combined)) {
			defer outStop()
			defer tpStop()
			if reasoningArmer, ok := w.(providers.ReasoningProgressArmer); ok {
				reasoningArmer.ArmReasoningProgress(outMark)
			}
		} else {
			outStop()
			tpStop()
		}
	}

	streamErr := httputil.StreamBody(ctx, cancel, c.idleTimeout(), resp.Body, resp.StatusCode, w, t)
	if errors.Is(streamErr, httputil.ErrUpstreamIdleTimeout) || errors.Is(streamErr, httputil.ErrUpstreamOutputStall) || errors.Is(streamErr, httputil.ErrUpstreamSlowThroughput) {
		logStreamStall(ctx, decision.Model, baseURL, streamErr)
	}
	return streamErr
}

// logStreamStall reports the watchdog cause without promising a retry after
// client output has committed.
func logStreamStall(ctx context.Context, model, baseURL string, cause error) {
	stallKind := "byte_idle"
	switch {
	case errors.Is(cause, httputil.ErrUpstreamOutputStall):
		stallKind = "output_idle"
	case errors.Is(cause, httputil.ErrUpstreamSlowThroughput):
		stallKind = "slow_throughput"
	}
	observability.FromContext(ctx).Error("Upstream OpenAI-compatible stream stalled mid-response; aborting stream",
		"model", model,
		"base_url", baseURL,
		"stall_kind", stallKind,
	)
}

// Passthrough strips the inbound /v1 prefix to avoid double-prefixing with the configured baseURL.
func (c *Client) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	suffix := strings.TrimPrefix(r.URL.Path, "/v1")
	baseURL := c.effectiveBaseURL(ctx)
	url := baseURL + suffix
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	upstream, err := http.NewRequestWithContext(ctx, r.Method, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build upstream passthrough request: %w", err)
	}
	c.applyDefaultHeaders(upstream)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstream.Header.Set("Content-Type", ct)
	}
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.setAuth(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("Accept"); v != "" {
		upstream.Header.Set("Accept", v)
	}

	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream passthrough call: %w", err)
	}
	defer resp.Body.Close()

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(r.Context(), w, resp, nil, nil, "Upstream OpenAI-compatible provider returned error status (passthrough)", "base_url", baseURL, "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

var _ providers.Client = (*Client)(nil)
