package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/providers/openaicompat"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/requestcontext"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from upstream 4774d3ba (#1908), adapted to v0's aiand-only surface:
// the Messages case serves through the OpenAI-compatible aiand provider and
// the badge wording carries the Aiand Router brand.

const sessionlessClientApp = "copilot"

const sessionlessServedModel = "z-ai/glm-5.3"

func sessionlessMessagesBody(t *testing.T, announcement string) []byte {
	t.Helper()
	firstStep := []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": map[string]string{"command": "ls"}}}
	if announcement != "" {
		firstStep = append([]map[string]any{{"type": "text", "text": announcement}}, firstStep...)
	}
	body, err := json.Marshal(map[string]any{
		"model": "auto", "stream": true, "max_tokens": 4096,
		"tools": []map[string]any{{"name": "bash", "description": "run", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]string{"type": "string"}}}}},
		"messages": []map[string]any{
			{"role": "user", "content": "list the files, then count them"},
			{"role": "assistant", "content": firstStep},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_1", "content": "a.go\nb.go"}}},
		},
	})
	require.NoError(t, err)
	return body
}

func sessionlessMessagesService(t *testing.T, pins *fakePinStore) (*proxy.Service, *[][]byte) {
	t.Helper()
	var upstreamBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBodies = append(upstreamBodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"There are two files.\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAIAND, Model: sessionlessServedModel, Reason: "test"}},
		map[string]providers.Client{providers.ProviderAIAND: openaicompat.NewClient("test-key", upstream.URL)},
		nil, false, nil, pins, false, providers.ProviderAIAND, sessionlessServedModel, newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAIAND: {}}).WithRetrySleep(noRetrySleep)
	return svc, &upstreamBodies
}

// A client that sends no session identity gets no server-side pin history, so
// the announcement it replays is the only record of the model it has seen.
func TestSessionlessRoutingAnnouncementMessages(t *testing.T) {
	const announced = "✦ **Aiand Router** → " + sessionlessServedModel + " · best pick for this turn · request classified as low difficulty\n\n"
	const other = "✦ **Aiand Router** → z-ai/glm-5.3-flash · best pick for this turn\n\n"
	for _, test := range []struct {
		name         string
		announcement string
		priorServed  string
		wantBadge    bool
	}{
		{name: "tool-loop step after announcement", announcement: announced},
		{name: "tool-loop step after whitespace-trimmed announcement", announcement: strings.TrimSpace(announced)},
		{name: "replayed a different model", announcement: other, wantBadge: true},
		{name: "nothing announced yet", wantBadge: true},
		{name: "quoted prose is not an announcement", announcement: "Docs say: " + announced, wantBadge: true},
		{name: "server history still decides", announcement: announced, priorServed: "z-ai/glm-5.3-flash", wantBadge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pins := newFakePinStore()
			if test.priorServed != "" {
				pins.hasPin = true
				pins.pin = sessionpin.Pin{Provider: providers.ProviderAIAND, Model: sessionlessServedModel, LastServedModel: test.priorServed, PinnedUntil: time.Now().Add(time.Hour)}
			}
			svc, upstreamBodies := sessionlessMessagesService(t, pins)
			ctx := requestcontext.WithClientIdentity(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.ClientIdentity{ClientApp: sessionlessClientApp})
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, sessionlessMessagesBody(t, test.announcement), rec,
				httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
			assert.Equal(t, test.wantBadge, strings.Contains(rec.Body.String(), "Aiand Router** → "+sessionlessServedModel), rec.Body.String())
			assert.Contains(t, rec.Body.String(), "There are two files.")
			require.Len(t, *upstreamBodies, 1)
			assert.NotContains(t, string((*upstreamBodies)[0]), "**Aiand Router** →", "replayed announcements must stay out of provider input")
		})
	}
}

func announcementReplayBody(t *testing.T, history ...string) []byte {
	t.Helper()
	input := []map[string]any{{"role": "user", "content": "Repository instructions for a synthetic parent and child."}}
	for _, text := range history {
		input = append(input, map[string]any{"role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	}
	body, err := json.Marshal(map[string]any{"model": "auto", "stream": true, "instructions": "Complete the requested task.", "input": input})
	require.NoError(t, err)
	return body
}

// The upstream #1908 Responses end-to-end case (TestSessionlessRoutingAnnouncementResponses)
// was dropped: upstream exercises it through a non-passthrough (OpenRouter)
// decision where the routing badge reaches the client through the translating
// writer, while v0's aiand-only surface always verbatim-passthroughs Responses
// turns and suppresses the notice for non-terminal clients (pre-existing v0
// policy, see the notice.Text = "" suppression in ProxyOpenAIChatCompletion).
// The announcementTurnFor replayed-model fallback is covered by the Messages
// e2e above and the translate-level replay-history tests.
