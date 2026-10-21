package proxy_test

import (
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func attrInt(sp *tracev1.Span, key string) (int64, bool) {
	for _, kv := range sp.Attributes {
		if kv.Key != key {
			continue
		}
		if iv, ok := kv.Value.Value.(*commonv1.AnyValue_IntValue); ok {
			return iv.IntValue, true
		}
	}
	return 0, false
}
