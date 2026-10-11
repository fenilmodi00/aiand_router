package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failover_used credits a turn to the key that served it, so it must stay
// false when the rescue also failed; failover_attempted is what shows a
// rescue was dispatched at all.
//
// ADAPTED: upstream also covers the subscription-credit lane here
// (subscriptionFailoverAttempted). That lane was cut in the AIand-only split
// — subscriptionFailoverUsed is a retained hard false and no attempt counter
// survives — so sibling rescue carries the "attempted but failed" half.
func TestSiblingRescue_RecordsAttemptSeparatelyFromUse(t *testing.T) {
	upstream502 := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"type":"api_error","message":"bad gateway"}}`)}
	cases := []struct {
		name       string
		siblingErr error
		wantUsed   bool
	}{
		{name: "failed sibling rescue", siblingErr: upstream502},
		{name: "served sibling rescue", wantUsed: true},
	}
	ingresses := []struct {
		name string
		path string
		body []byte
		call func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{name: "messages", path: "/v1/messages", body: anthropicMessagesBody(), call: (*Service).ProxyMessages},
		{name: "chat", path: "/v1/chat/completions", body: openaiChatBody(), call: (*Service).ProxyOpenAIChatCompletion},
	}
	for _, ingress := range ingresses {
		for _, tc := range cases {
			t.Run(ingress.name+"/"+tc.name, func(t *testing.T) {
				svc := newRescuedFailureTurnService(&demotionStubPinStore{},
					rescuableDecision("hmm:authoritative model="+rescuedPrimaryModel, true),
					&failingClient{err: upstream502}, &servingClient{err: tc.siblingErr}, true)
				telemetry := &auxTelemetryRepo{}
				svc.telemetry = telemetry

				err := ingress.call(svc, rescuedFailureCtx(), ingress.body, httptest.NewRecorder(),
					httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(ingress.body))))
				if tc.wantUsed {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}

				rows := telemetry.waitForRows(1)
				require.Len(t, rows, 1)
				require.NotNil(t, rows[0].FailoverAttempted)
				assert.True(t, *rows[0].FailoverAttempted)
				if tc.wantUsed {
					assert.True(t, *rows[0].FailoverUsed)
				}
			})
		}
	}
}
