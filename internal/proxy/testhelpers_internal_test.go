package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
)

const (
	budgetTestFable     = "z-ai/glm-5.3-flash"
	budgetTestHaiku     = "z-ai/glm-5.2"
	budgetTestUserAgent = "claude-cli/2.1.257 (external, sdk-ts)"
	budgetTestBeta      = "context-1m-2025-08-07"
)

// noRetrySleep replaces the retry backoff so a test does not wait it out.
func noRetrySleep(context.Context, time.Duration) error { return nil }

func smallClientBudget() router.ClientBudget {
	return resolveClientBudget(ClientIdentity{ClientApp: ClientAppClaudeCode, UserAgent: budgetTestUserAgent}, nil, budgetTestFable, false)
}

// Chunk sizes every buffered SSE consumer is exercised at: one byte, odd sizes
// that land boundaries mid-line and mid-delimiter, and the provider adapters'
// read size.
var fragmentationChunkSizes = []int{1, 3, 7, 13, 4096}

// largeFixtureBytes is the size past which a fixture is fed only in the
// adapters' 4 KiB reads: byte-at-a-time writes of a large body exercise nothing
// further and make the test's runtime a function of the scanner's speed.
const largeFixtureBytes = 2048

func chunkSizesFor(body string) []int {
	if len(body) > largeFixtureBytes {
		return []int{4096}
	}
	return fragmentationChunkSizes
}

// chunkEvery splits body into consecutive writes of at most size bytes.
func chunkEvery(body string, size int) []string {
	var chunks []string
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, body[start:min(start+size, len(body))])
	}
	return chunks
}

// writeChunks writes each chunk as its own Write call, failing the test on the
// first error.
func writeChunks(t *testing.T, w io.Writer, chunks []string) {
	t.Helper()
	for _, chunk := range chunks {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
}

// errSinkClosed is the error a failingRecorder returns while it still owes
// failures.
var errSinkClosed = errors.New("sink closed")

// failingRecorder rejects the first failures writes so a wrapper's behavior on
// a forward-write error can be observed before any byte is consumed.
type failingRecorder struct {
	*httptest.ResponseRecorder
	failures int
}

func (r *failingRecorder) Write(p []byte) (int, error) {
	if r.failures > 0 {
		r.failures--
		return 0, errSinkClosed
	}
	return r.ResponseRecorder.Write(p)
}

// serveSyntheticCodexQuota answers the subscription-pool quota probe.
func serveSyntheticCodexQuota(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/wham/usage" {
		return false
	}
	_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
	return true
}

// fixedRouter routes every request to one decision.
type fixedRouter struct{ decision router.Decision }

func (f fixedRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return f.decision, nil
}

// staticRouter answers every routing call with one decision.
type staticRouter struct{ decision router.Decision }

func (s staticRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return s.decision, nil
}

// writeChatCompletionText streams text as a complete chat/completions turn.
func writeChatCompletionText(w http.ResponseWriter, text string) {
	content, _ := json.Marshal(text)
	for _, frame := range []string{
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(content) + `},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`,
		"[DONE]",
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
}

// responsesRetryClient fails its first dispatch retryably and answers the next
// one with a complete non-streaming Responses body, recording each endpoint.
type responsesRetryClient struct {
	endpoints []providers.Endpoint
}

func (*responsesRetryClient) SupportsSubscriptions() bool { return true }

func (c *responsesRetryClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.endpoints = append(c.endpoints, prep.Endpoint)
	if len(c.endpoints) == 1 {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusServiceUnavailable,
			Body:   []byte(`{"error":{"message":"overloaded"}}`),
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if prep.Endpoint == providers.EndpointChatCompletions {
		writeChatCompletionText(w, "served after retry")
		return nil
	}
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"served after retry"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"served after retry"}]}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
	return nil
}

func (c *responsesRetryClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}
