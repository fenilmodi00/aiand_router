package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from upstream 7d1eed8a (#1862), with catalog model names adapted to
// v0's aiand catalog. The upstream integration tests were dropped: they drive
// the multi-provider dispatch path v0 does not serve.

const earlyStopStoppedStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestEarlyStopGate_WithholdsAStopSplitAcrossWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, slog.Default())
	g.observe(true)
	for i := 0; i < len(earlyStopStoppedStream); i += 7 {
		end := min(i+7, len(earlyStopStoppedStream))
		_, err := g.Write([]byte(earlyStopStoppedStream[i:end]))
		require.NoError(t, err)
	}
	assert.True(t, g.earlyStop())
	assert.True(t, g.withheld())
	assert.Empty(t, rec.Body.String(), "nothing reaches the client while withheld")
	_, body := g.takeWithheld()
	assert.Equal(t, earlyStopStoppedStream, string(body))
}

func TestEarlyStopGate_ReleasesAtFirstToolUse(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, slog.Default())
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	assert.Empty(t, rec.Body.String())
	_, _ = g.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"Bash\"}}\n\n"))
	assert.False(t, g.withheld())
	assert.Contains(t, rec.Body.String(), "message_start")
	assert.Contains(t, rec.Body.String(), "tool_use")
	assert.False(t, g.earlyStop())
}

func TestEarlyStopGate_ReleasesAfterHoldMaxWithoutAnotherWrite(t *testing.T) {
	rec := newSyncRecorder()
	g := newEarlyStopGate(rec, slog.Default())
	g.holdMax = 10 * time.Millisecond
	g.observe(true)
	_, _ = g.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
	require.Eventually(t, func() bool { return !g.withheld() }, time.Second, 5*time.Millisecond,
		"a silent upstream must not hold the client past holdMax")
	assert.Equal(t, "hold_timeout", g.releaseReason())
	assert.Contains(t, rec.body(), "message_start")
}

func TestEarlyStopGate_ShadowPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, slog.Default())
	g.observe(false)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	assert.Equal(t, earlyStopStoppedStream, rec.Body.String())
	assert.True(t, g.earlyStop())
	assert.False(t, g.withheld())
}

func TestEarlyStopGate_DiscardStartsTheNextAttemptClean(t *testing.T) {
	rec := httptest.NewRecorder()
	g := newEarlyStopGate(rec, slog.Default())
	g.observe(true)
	_, _ = g.Write([]byte(earlyStopStoppedStream))
	g.resetAttempt()
	assert.False(t, g.earlyStop())
	assert.True(t, g.withheld())
	_, body := g.takeWithheld()
	assert.Empty(t, body)
}

func TestHigherTierCandidates_TopTierFirst(t *testing.T) {
	got := higherTierCandidates(catalog.TierLow,
		[]string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro"},
		map[string]float32{"z-ai/glm-5.3": 0.9, "moonshotai/kimi-k3.1-pro": 0.4, "deepseek/deepseek-v4-pro": 0.99})
	assert.Equal(t, []string{"z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro", "deepseek/deepseek-v4-pro"}, got)
	assert.Empty(t, higherTierCandidates(catalog.TierHigh, []string{"z-ai/glm-5.3-flash", "z-ai/glm-5.3"}, nil))
}

func TestLowerTierCandidates(t *testing.T) {
	assert.Equal(t, []string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro"},
		lowerTierCandidates("z-ai/glm-5.3", []string{"z-ai/glm-5.3-flash", "deepseek/deepseek-v4-pro", "z-ai/glm-5.3", "moonshotai/kimi-k3.1-pro"}))
}

// syncRecorder is an httptest recorder safe to read while the hold timer writes.
type syncRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{rec: httptest.NewRecorder()} }

func (r *syncRecorder) Header() http.Header { return r.rec.Header() }

func (r *syncRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(status)
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}
