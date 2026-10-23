package proxy_test

import (
	"bytes"
	"log/slog"
	"testing"

	"aiand-os/router/internal/observability"
)

const streamCutTurnBody = `{"model":"z-ai/glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func captureCompletionLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	// Prime observability's sync.Once before overriding slog.Default.
	observability.Get()
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &logBuf
}
