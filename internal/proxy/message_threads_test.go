package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProxyMessages_ThreadCreateServedStateless(t *testing.T) {
	svc, _, p := bypassFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("anthropic-beta", "claude-code-20250219,message-threads-2026-08-12,context-1m-2025-08-07")
	body := []byte(`{"model":"` + bypassRequestedMdl + `","messages":[{"role":"user","content":"hi"}],"thread":{"type":"create"}}`)

	require.NoError(t, svc.ProxyMessages(bypassCtx(), body, rec, req))

	require.Len(t, p.proxyBodies, 1)
	assert.False(t, gjson.GetBytes(p.proxyBodies[0], "thread").Exists(), "a thread create must reach the upstream as a stateless request")
	assert.Contains(t, gjson.GetBytes(p.proxyBodies[0], "messages.0.content").Raw, `"hi"`, "the full transcript must still be forwarded")
	// The one wired upstream speaks the OpenAI-compatible surface, so the
	// Anthropic beta header is not forwarded at all — and in particular the
	// message-threads token never reaches it.
	beta := p.proxyHeaders[0].Get("anthropic-beta")
	assert.NotContains(t, beta, "message-threads")
}

func TestProxyMessages_ThreadContinueAsksClientForFullHistory(t *testing.T) {
	svc, _, p := bypassFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("anthropic-beta", "message-threads-2026-08-12")
	body := []byte(`{"model":"` + bypassRequestedMdl + `","messages":[{"role":"user","content":"next"}],"thread":{"type":"continue","previous_message_id":"msg_1"}}`)

	require.NoError(t, svc.ProxyMessages(bypassCtx(), body, rec, req))

	assert.Empty(t, p.proxyBodies, "a continue carries only the delta, so it must never be served as a stateless transcript")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	assert.Equal(t, "thread_unsupported_request", gjson.Get(rec.Body.String(), "error.details.error_code").String(), "Claude Code resends statelessly only on this error code")
}
