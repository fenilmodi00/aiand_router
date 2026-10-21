package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const (
	rescuedPrimaryModel = "z-ai/glm-5.2"
	rescuerModel        = "gpt-5.6-luna"
)

// failingClient answers every dispatch with the same error, so in-binding
// retries exhaust without the primary ever serving.
type failingClient struct {
	err   error
	calls int
}

func (c *failingClient) Proxy(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	c.calls++
	return c.err
}

func (c *failingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// servingClient streams one completed Responses turn on every dispatch.
type servingClient struct {
	err   error
	calls int
}

func (c *servingClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if prep.Endpoint == providers.EndpointChatCompletions {
		writeChatCompletionText(w, "served by sibling")
		return nil
	}
	for _, frame := range []string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"served by sibling"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"served by sibling"}]}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
	return nil
}

func (c *servingClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func rescuableDecision(reason string, withSibling bool) router.Decision {
	d := router.Decision{
		Provider: providers.ProviderAIAND,
		Model:    rescuedPrimaryModel,
		Reason:   reason,
	}
	if withSibling {
		d.Metadata = &router.RoutingMetadata{
			CandidateModels:    []string{rescuedPrimaryModel, rescuerModel},
			CandidateProviders: map[string]string{rescuerModel: providers.ProviderAIAND},
		}
	}
	return d
}

func newRescuedFailureTurnService(store sessionpin.Store, decision router.Decision, primary, sibling providers.Client, flagOn bool) *Service {
	return NewService(
		staticRouter{decision: decision},
		map[string]providers.Client{
			providers.ProviderAIAND: primary,
		},
		nil, false, nil, store, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderAIAND: {},
	}).WithRetrySleep(func(context.Context, time.Duration) error { return nil }).WithRescuedFailureArmDemotion(flagOn)
}

func rescuedFailureCtx() context.Context {
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-1")
	return context.WithValue(ctx, InstallationIDContextKey{}, uuid.New().String())
}

// assertRescuedFailureStrikes checks that exactly the primary was struck, on
// the turn's pin role and its HMM history row, and never the rescuer.
func assertRescuedFailureStrikes(t *testing.T, demotions []demotionCall, wantDemoted bool, wantDemotionReason sessionpin.DemotionReason) {
	t.Helper()
	if !wantDemoted {
		assert.Empty(t, demotions, "the primary must stay eligible")
		return
	}
	require.Len(t, demotions, 2, "the strike must land on the pin row and its HMM history row")
	assert.Equal(t, hmmHistoryRole(demotions[0].role), demotions[1].role)
	if wantDemotionReason == "" {
		wantDemotionReason = sessionpin.DemotionReasonRescuedFailure
	}
	for _, d := range demotions {
		assert.Equal(t, rescuedPrimaryModel, d.model, "only the primary is struck, never the rescuer")
		assert.Equal(t, wantDemotionReason, d.reason)
	}
}

type rescuedFailureTurnCase struct {
	name               string
	flagOn             bool
	withSibling        bool
	primaryErr         error
	siblingErr         error
	reason             string
	wantTurnErr        bool
	wantDemoted        bool
	wantDemotionReason sessionpin.DemotionReason
}

func rescuedFailureTurnCases(t *testing.T) []rescuedFailureTurnCase {
	t.Helper()
	upstream502 := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"type":"api_error","message":"bad gateway"}}`)}
	overloaded := &providers.UpstreamErrorResponse{Status: providerOverloadedStatus, Body: []byte(`{"error":{"type":"overloaded_error","message":"Overloaded"}}`)}
	notFound := &providers.UpstreamErrorResponse{Status: http.StatusNotFound, Body: []byte(`{"error":{"type":"not_found_error","message":"model: claude-opus-5"}}`)}
	headerTimeout := responseHeaderTimeoutErr(t)
	authoritative := "hmm:authoritative model=" + rescuedPrimaryModel
	return []rescuedFailureTurnCase{
		{name: "sibling serves after primary 502", flagOn: true, withSibling: true, primaryErr: upstream502, reason: authoritative, wantDemoted: true},
		{name: "sibling also fails after primary 502", flagOn: true, withSibling: true, primaryErr: upstream502, siblingErr: upstream502, reason: authoritative, wantTurnErr: true, wantDemoted: true},
		{name: "sibling serves after primary response header timeout", flagOn: true, withSibling: true, primaryErr: headerTimeout, reason: authoritative, wantDemoted: true, wantDemotionReason: sessionpin.DemotionReasonResponseHeaderTimeout},
		{name: "sibling also fails after primary response header timeout", flagOn: true, withSibling: true, primaryErr: headerTimeout, siblingErr: upstream502, reason: authoritative, wantTurnErr: true, wantDemoted: true, wantDemotionReason: sessionpin.DemotionReasonResponseHeaderTimeout},
		{name: "flag off", flagOn: false, withSibling: true, primaryErr: upstream502, reason: authoritative},
		{name: "no sibling to rescue with", flagOn: true, withSibling: false, primaryErr: upstream502, reason: authoritative, wantTurnErr: true},
		{name: "unrescued response header timeout", flagOn: true, withSibling: false, primaryErr: headerTimeout, reason: authoritative, wantTurnErr: true, wantDemoted: true, wantDemotionReason: sessionpin.DemotionReasonResponseHeaderTimeout},
		{name: "primary overloaded 529", flagOn: true, withSibling: true, primaryErr: overloaded, reason: authoritative},
		{name: "gateway lacks primary", flagOn: true, withSibling: true, primaryErr: notFound, reason: authoritative},
		{name: "user forced primary", flagOn: true, withSibling: true, primaryErr: upstream502, reason: translate.ReasonUserForceModel, wantTurnErr: true},
	}
}

func TestProxyMessages_UnrescuedIdleFailureDemotesPrimary(t *testing.T) {
	for _, ingress := range []struct {
		name   string
		path   string
		body   []byte
		openAI bool
	}{
		{name: "anthropic_messages", path: "/v1/messages", body: anthropicMessagesBody()},
		{name: "openai_chat", path: "/v1/chat/completions", body: openaiChatBody(), openAI: true},
	} {
		t.Run(ingress.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			primary := &failingClient{err: providers.ErrUpstreamIdleTimeout}
			svc := newRescuedFailureTurnService(store, rescuableDecision("hmm:authoritative model="+rescuedPrimaryModel, false), primary, &servingClient{}, true)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(ingress.body)))
			var err error
			if ingress.openAI {
				err = svc.ProxyOpenAIChatCompletion(rescuedFailureCtx(), ingress.body, rec, req)
			} else {
				err = svc.ProxyMessages(rescuedFailureCtx(), ingress.body, rec, req)
			}

			require.ErrorIs(t, err, providers.ErrUpstreamIdleTimeout)
			assert.Positive(t, primary.calls)
			require.Len(t, store.demotions, 2, "a precommit stall must protect the next automatic turn even without a rescue")
			for _, demotion := range store.demotions {
				assert.Equal(t, rescuedPrimaryModel, demotion.model)
				assert.Equal(t, sessionpin.DemotionReasonUnrescuedStall, demotion.reason)
			}
		})
	}
}
