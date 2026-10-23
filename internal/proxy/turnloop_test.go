package proxy_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/proxy"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/handover"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fakeSummarizer is a deterministic handover.Summarizer: returns summary,
// or errOnCall if set. calls counts invocations.
type fakeSummarizer struct {
	summary   string
	errOnCall error
	calls     atomic.Int32
}

func (f *fakeSummarizer) Summarize(ctx context.Context, env *translate.RequestEnvelope, _ router.Request) (string, handover.Usage, error) {
	f.calls.Add(1)
	if f.errOnCall != nil {
		return "", handover.Usage{}, f.errOnCall
	}
	return f.summary, handover.Usage{}, nil
}

func (f *fakeSummarizer) Provider() string { return providers.ProviderAIAND }

// usageProvider writes an Anthropic response with the configured token
// usage so the OTel UsageExtractor surfaces it to the cache-stats writeback.
type usageProvider struct {
	in       int
	out      int
	cacheIn  int
	cacheOut int
}

func (*usageProvider) SupportsSubscriptions() bool { return true }

func (p *usageProvider) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	body := `{"id":"m","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":` +
		itoa(p.in) + `,"output_tokens":` + itoa(p.out) +
		`,"cache_creation_input_tokens":` + itoa(p.cacheOut) +
		`,"cache_read_input_tokens":` + itoa(p.cacheIn) + `}}`
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
	return nil
}

func (p *usageProvider) Passthrough(ctx context.Context, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

func itoa(n int) string {
	// Tiny helper that avoids importing strconv into the test body.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// largeBody yields ~10k input tokens so the planner's EV math clears threshold.
func largeBody(t *testing.T) []byte {
	t.Helper()
	prompt := strings.Repeat("aaaa ", 8000) // ~10k tokens
	return []byte(`{
		"model":"z-ai/glm-5.3",
		"system":"sys",
		"messages":[{"role":"user","content":"` + prompt + `"}]
	}`)
}

// largeMultiTurnBody yields a 6-message conversation so a trim-to-last-3
// fallback would be observable (6→3), proving the handover failure path
// preserves full history instead.
func largeMultiTurnBody(t *testing.T) []byte {
	t.Helper()
	chunk := strings.Repeat("aaaa ", 1600) // ~2k tokens each
	msgs := []string{
		`{"role":"user","content":"FIRST-USER-MARKER ` + chunk + `"}`,
		`{"role":"assistant","content":"` + chunk + `"}`,
		`{"role":"user","content":"` + chunk + `"}`,
		`{"role":"assistant","content":"` + chunk + `"}`,
		`{"role":"user","content":"` + chunk + `"}`,
		`{"role":"user","content":"latest question"}`,
	}
	return []byte(`{"model":"z-ai/glm-5.3","system":"sys","messages":[` + strings.Join(msgs, ",") + `]}`)
}

// forwardedMessageCount returns the number of messages in the body the
// orchestrator forwarded to the upstream provider on its first Proxy call.
func forwardedMessageCount(t *testing.T, p *fakeProvider) int {
	t.Helper()
	require.NotEmpty(t, p.proxyBodies, "upstream must have been called")
	return int(gjson.GetBytes(p.proxyBodies[0], "messages.#").Int())
}

// newPinSvcCapturing mirrors newPinSvc but returns the upstream fakeProvider
// so a test can inspect the body forwarded after handover.
func newPinSvcCapturing(fr *fakeRouter, store *fakePinStore) (*proxy.Service, *fakeProvider) {
	p := &fakeProvider{}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAIAND: p},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAIAND,
		"z-ai/glm-5.3-flash",
		nil,
	)
	return svc, p
}

const toolResultPinnedBody = `{
	"model":"z-ai/glm-5.3",
	"system":"sys",
	"messages":[
		{"role":"user","content":"plan"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"R","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}
	]
}`

// trimSessionTurn builds an alternating user/assistant body with a constant
// first message, so every turn derives the same session key.
func trimSessionTurn(t *testing.T, msgCount int) []byte {
	t.Helper()
	require.True(t, msgCount%2 == 1, "odd msgCount keeps the trailing message a user turn")
	msgs := make([]string, 0, msgCount)
	for i := 0; i < msgCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, `{"role":"`+role+`","content":"TRIM-SESSION turn `+itoa(i)+`"}`)
	}
	msgs[0] = `{"role":"user","content":"TRIM-SESSION refactor the dispatch loop"}`
	return []byte(`{"model":"z-ai/glm-5.3","system":"sys","messages":[` + strings.Join(msgs, ",") + `]}`)
}

// warmOpusPin's prior turn billed 1.5k input tokens seconds ago (warm), so an
// opus→haiku switch is an EV-negative stay unless something prices the cache
// as cold.
func warmOpusPin() sessionpin.Pin {
	return sessionpin.Pin{
		Provider:        providers.ProviderAIAND,
		Model:           "z-ai/glm-5.3",
		Reason:          "cluster:v0.2",
		PinnedUntil:     time.Now().Add(time.Hour),
		LastInputTokens: 1500,
		LastTurnEndedAt: time.Now().Add(-30 * time.Second),
	}
}

// recordingTelemetry is a no-op repo that flips usageRequired() on; we
// don't assert on telemetry rows here.
type recordingTelemetry struct{}

func (recordingTelemetry) InsertRequestTelemetry(ctx context.Context, p proxy.InsertTelemetryParams) error {
	return nil
}
func (recordingTelemetry) GetTelemetrySummary(ctx context.Context, installationID string, from, to time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}
func (recordingTelemetry) GetTelemetryTimeseries(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}
func (recordingTelemetry) GetTelemetrySummaryAll(ctx context.Context, from, to time.Time) (proxy.TelemetrySummary, error) {
	return proxy.TelemetrySummary{}, nil
}
func (recordingTelemetry) GetTelemetryTimeseriesAll(ctx context.Context, from, to time.Time, granularity string) ([]proxy.TelemetryBucket, error) {
	return nil, nil
}
func (recordingTelemetry) GetTelemetryRows(ctx context.Context, installationID string, from, to time.Time, limit int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}
func (recordingTelemetry) GetTelemetryRowsAll(ctx context.Context, from, to time.Time, limit int32) ([]proxy.TelemetryRow, error) {
	return nil, nil
}
func (recordingTelemetry) GetTelemetryModelBreakdown(ctx context.Context, installationID string, from, to time.Time, granularity string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}
func (recordingTelemetry) GetTelemetryModelBreakdownAll(ctx context.Context, from, to time.Time, granularity string) ([]proxy.TelemetryModelBucket, error) {
	return nil, nil
}

func (recordingTelemetry) GetSessionCost(ctx context.Context, installationID, sessionID string) (proxy.SessionCost, error) {
	return proxy.SessionCost{}, proxy.ErrSessionCostNotFound
}

func (recordingTelemetry) GetTelemetryBySessionSequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, seq int) (proxy.TelemetryTurnResult, error) {
	return proxy.TelemetryTurnResult{}, nil
}
