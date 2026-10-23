package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
)

// bypassSpanCollector is an in-process OTLP endpoint that records spans by name for assertion.
type bypassSpanCollector struct {
	srv    *httptest.Server
	mu     sync.Mutex
	byName map[string][]*tracev1.Span
}

func newBypassSpanCollector(t *testing.T) *bypassSpanCollector {
	t.Helper()
	c := &bypassSpanCollector{byName: make(map[string][]*tracev1.Span)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if r.URL.Path != "/v1/traces" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req coltracepb.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		c.mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					c.byName[sp.Name] = append(c.byName[sp.Name], sp)
				}
			}
		}
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func spanStr(t *testing.T, sp *tracev1.Span, key string) string {
	t.Helper()
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			sv, ok := kv.Value.Value.(*commonv1.AnyValue_StringValue)
			require.True(t, ok, "attr %q must be a string", key)
			return sv.StringValue
		}
	}
	t.Fatalf("attr %q not present on span", key)
	return ""
}

// bypassCaptureTelemetry records InsertTelemetryParams rows for assertions.
// Only InsertRequestTelemetry matters; the read methods satisfy the interface.
type bypassCaptureTelemetry struct {
	panicTelemetryRepo // inherit no-op reads
	mu                 sync.Mutex
	rows               []InsertTelemetryParams
	notify             chan struct{}
}

func newBypassCaptureTelemetry() *bypassCaptureTelemetry {
	return &bypassCaptureTelemetry{notify: make(chan struct{}, 4)}
}

func (c *bypassCaptureTelemetry) InsertRequestTelemetry(_ context.Context, p InsertTelemetryParams) error {
	c.mu.Lock()
	c.rows = append(c.rows, p)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return nil
}

func spanInt(t *testing.T, sp *tracev1.Span, key string) int64 {
	t.Helper()
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			iv, ok := kv.Value.Value.(*commonv1.AnyValue_IntValue)
			require.True(t, ok, "attr %q must be an int", key)
			return iv.IntValue
		}
	}
	t.Fatalf("attr %q not present on span", key)
	return 0
}

// openaiChatBody and anthropicMessagesBody are the two request shapes the
// proxy ingress tests dispatch.
func openaiChatBody() []byte {
	return []byte(`{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
}

func anthropicMessagesBody() []byte {
	return []byte(`{"model":"z-ai/glm-5.2","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"read main.go"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
}

// upstreamErr builds a buffered upstream error, the shape both classifiers read.
func upstreamErr(status int, body string) error {
	return &providers.UpstreamErrorResponse{Status: status, Body: []byte(body)}
}

// modelAccessError is Anthropic's "this subscription cannot use that model"
// refusal, the shape the model-denial cache keys on.
func modelAccessError() error {
	return upstreamErr(http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"model: z-ai/glm-5.2"}}`)
}

// bypassFakeProvider is an internal-package fake providers.Client for
// exercising the proxy surfaces without a live upstream.
type bypassFakeProvider struct {
	respBody     string
	proxyErr     error
	dispatches   int
	capturedBody []byte
}

func (f *bypassFakeProvider) SupportsSubscriptions() bool { return true }

func (f *bypassFakeProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	f.dispatches++
	if f.proxyErr != nil {
		return f.proxyErr
	}
	if f.respBody != "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(f.respBody))
	}
	return nil
}

func (f *bypassFakeProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}
