package proxy

import (
	"context"
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type flowSpanRouter struct {
	spanID   trace.SpanID
	decision router.Decision
}

func (r *flowSpanRouter) Route(ctx context.Context, _ router.Request) (router.Decision, error) {
	r.spanID = trace.SpanContextFromContext(ctx).SpanID()
	if r.decision.Model != "" {
		return r.decision, nil
	}
	return router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    "z-ai/glm-5.3-flash",
		Reason:   "cluster",
	}, nil
}

type flowSpanProvider struct {
	spanID trace.SpanID
}

func (p *flowSpanProvider) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	p.spanID = trace.SpanContextFromContext(ctx).SpanID()
	return nil
}

func (p *flowSpanProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func proxyEndedSpanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	require.FailNow(t, "span not found", "name=%s", name)
	return nil
}

func proxySpanAttribute(t *testing.T, attrs []attribute.KeyValue, key string) attribute.Value {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value
		}
	}
	require.FailNow(t, "span attribute not found", "key=%s", key)
	return attribute.Value{}
}
