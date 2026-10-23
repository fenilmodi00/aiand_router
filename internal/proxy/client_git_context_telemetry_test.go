package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic Claude Code system prompt: a plain instruction block followed by
// the gitStatus block as a separate content block, the shape Claude Code sends.
const clientGitContextBody = `{
	"model":"z-ai/glm-5.3",
	"system":[
		{"type":"text","text":"You are Claude Code."},
		{"type":"text","text":"gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\nCurrent branch: feature/synthetic-branch\n\nMain branch (you will usually use this for PRs): main\n\nGit user: Synthetic User\n\nStatus:\n M internal/example.go\n?? notes.txt\n\nRecent commits:\nabc1234 synthetic subject one\ndef5678 synthetic subject two"}
	],
	"messages":[{"role":"user","content":"original prompt"}]
}`

func trialCtx(installationID string) context.Context {
	return context.WithValue(authedCtx(installationID), proxy.InstallationTrialCaptureContextKey{}, true)
}

func proxyClientGitContextTurn(t *testing.T, ctx context.Context, store *fakePinStore) proxy.InsertTelemetryParams {
	t.Helper()
	telem := newCaptureTelemetry()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3-flash", Reason: "cluster:v0.2"}}
	svc := newPinSvcWithTelemetry(fr, store, telem)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(clientGitContextBody), rec, httpReq))
	return telem.firstRow(t)
}

func assertClientGitContextNull(t *testing.T, row proxy.InsertTelemetryParams) {
	t.Helper()
	assert.Empty(t, row.ClientGitHeadSHA)
	assert.Empty(t, row.ClientGitBranch)
	assert.Nil(t, row.ClientGitDirty)
}
