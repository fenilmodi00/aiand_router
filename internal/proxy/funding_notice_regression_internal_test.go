package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/translate"
)

func TestFundingNoticePolicyAndReplay(t *testing.T) {
	const model = "synthetic-model"
	const marker = "✦ **Weave Router** → synthetic-model\n\n"
	for _, warning := range subscriptionFundingWarnings {
		t.Run(string(warning.reason), func(t *testing.T) {
			ctx := billing.WithSubscriptionOnly(context.Background(), warning.reason)
			notice := routerNoticeForRequest(ctx, http.Header{}, model, marker)
			assert.Equal(t, translate.NoticeFundingWarning, notice.Kind)
			assert.Equal(t, translate.NoticeFromRouter, notice.Provenance)
			assert.Equal(t, model, notice.Model)
			assert.Contains(t, notice.Text, "subscription-only mode")
			if warning.reason == billing.SubscriptionOnlyCreditsDepleted {
				assert.Contains(t, notice.Text, "credits are depleted")
				assert.Contains(t, notice.Text, topUpURL)
			} else {
				assert.Contains(t, notice.Text, "spend cap has been reached")
				assert.NotContains(t, notice.Text, topUpURL)
			}
			rec := httptest.NewRecorder()
			caps := translate.NoticeCapabilities{Format: translate.FormatOpenAIResponses, Streaming: true, Presentation: translate.NoticeText}
			delivery := translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(rec, caps), caps)
			_, err := delivery.Deliver(notice)
			require.NoError(t, err)
			var responsesText string
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if payload, ok := strings.CutPrefix(line, "data: "); ok {
					if delta := gjson.Get(payload, "delta"); delta.Type == gjson.String {
						responsesText += delta.String()
					}
				}
			}
			require.Contains(t, responsesText, strings.TrimSpace(notice.Text))
			for _, body := range []string{
				`{"messages":[{"role":"assistant","content":` + jsonString(notice.Text+"provider answer") + `}]}`,
				`{"messages":[{"role":"assistant","content":[{"type":"text","text":` + jsonString(notice.Text+"provider answer") + `}]}]}`,
				`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + jsonString(responsesText+"provider answer") + `}]}]}`,
			} {
				cleaned, err := translate.StripRoutingMarkerFromMessages([]byte(body))
				require.NoError(t, err)
				cleaned, err = translate.StripRoutingBadgeFromResponsesInput(cleaned)
				require.NoError(t, err)
				assert.NotContains(t, string(cleaned), "paid model fallback is disabled")
				assert.Contains(t, string(cleaned), "provider answer")
				echoCtx := withSubscriptionOnlyWarningEcho(ctx, []byte(body))
				echoCtx = withSubscriptionOnlyWarningEcho(echoCtx, []byte(`{"messages":[]}`))
				replayed := routerNoticeForRequest(echoCtx, http.Header{}, model, marker)
				assert.Equal(t, translate.NoticeModelSelection, replayed.Kind)
				assert.Equal(t, marker, replayed.Text)
			}
		})
	}
}

func TestFundingNoticeBufferedAndStreamingCapabilities(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/stream=%v", format, streaming), func(t *testing.T) {
				rec := httptest.NewRecorder()
				caps := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText}
				delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
				notice := translate.NewRouterNotice(translate.NoticeFundingWarning, subscriptionSpendCapWarningMarker, "synthetic-model", "")
				outcome, err := delivery.Deliver(notice)
				require.NoError(t, err)
				if !streaming && format != translate.FormatOpenAIResponses {
					assert.Equal(t, translate.NoticeSuppressed, outcome)
					assert.Empty(t, rec.Body.String())
					return
				}
				assert.Equal(t, translate.NoticeDelivered, outcome)
				if !streaming {
					_, err = delivery.Write([]byte(`{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"provider answer"}]}]}`))
					require.NoError(t, err)
				}
				assert.Contains(t, rec.Body.String(), "spend cap has been reached")
				assert.Contains(t, rec.Body.String(), "subscription-only mode")
			})
		}
	}
}
