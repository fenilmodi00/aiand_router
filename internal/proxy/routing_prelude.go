package proxy

import (
	"context"
	"net/http"

	"aiand-os/router/internal/translate"
)

// The coordinator outlives binding attempts; the buffer's provider commitment
// remains the retry gate even after a notice becomes visible.
type requestNoticeStream struct {
	delivery   *translate.NoticeDelivery
	lastNotice translate.RouterNotice
	modelShown bool
}

func (s *requestNoticeStream) prepare(ctx context.Context, sink http.ResponseWriter, prelude *preludeBuffer, format translate.Format, streaming bool, notice translate.RouterNotice) (http.ResponseWriter, error) {
	if s.delivery == nil {
		capabilities := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText, Eager: streaming && (format == translate.FormatOpenAI || format == translate.FormatOpenAIResponses)}
		s.delivery = translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(sink, capabilities), capabilities)
	}
	retainNotices := prelude == nil || prelude.PreludeSent()
	s.delivery.ResetAttempt(retainNotices)
	if !retainNotices {
		s.lastNotice = translate.RouterNotice{}
		s.modelShown = false
	}
	if s.suppressed(notice) {
		return s.delivery, nil
	}
	// Eager streams show the first marker immediately. Once one is visible, a
	// fallback candidate's marker waits for its output, so failed candidates
	// never add markers and only the model that served can correct the first.
	if s.modelShown && notice.Kind == translate.NoticeModelSelection && s.delivery.DeferIfEager(notice) {
		return s.delivery, nil
	}
	if err := s.deliver(notice); err != nil {
		return nil, err
	}
	return s.delivery, nil
}

func (s *requestNoticeStream) suppressed(notice translate.RouterNotice) bool {
	return notice.Text == "" || (notice.Kind == s.lastNotice.Kind && notice.Text == s.lastNotice.Text)
}

// deliver shows notice within the attempt prepare started, keeping that
// attempt's deferred marker.
func (s *requestNoticeStream) deliver(notice translate.RouterNotice) error {
	if s.suppressed(notice) {
		return nil
	}
	outcome, err := s.delivery.Deliver(notice)
	if err != nil {
		return err
	}
	if outcome == translate.NoticeDelivered {
		s.lastNotice = notice
		s.modelShown = s.modelShown || notice.Kind == translate.NoticeModelSelection
	}
	return nil
}
