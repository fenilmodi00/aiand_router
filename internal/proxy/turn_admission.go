package proxy

import (
	"context"
	"net/http"
	"time"

	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/dispatch"
	"aiand-os/router/internal/requestcontext"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/telemetry"
)

type turnAdmissionServices interface {
	withClassifierInput(ctx context.Context, body []byte, endpoint router.TranslationEndpoint) (context.Context, error)
	effectiveCaptureMode(ctx context.Context) ContentCaptureMode
	checkUserMonthlySpendLimit(ctx context.Context) (context.Context, error)
	withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger)
	newTelemetryRecorder() telemetry.Recorder
}

var _ turnAdmissionServices = (*Service)(nil)

func withTurnInput(ctx context.Context, s turnAdmissionServices, body []byte, headers http.Header, endpoint router.TranslationEndpoint) (context.Context, error) {
	ctx, err := s.withClassifierInput(ctx, body, endpoint)
	if err != nil {
		return ctx, err
	}
	return requestcontext.WithContentLogging(ctx, s.effectiveCaptureMode(ctx) != CaptureOff), nil
}

type admittedTurn struct {
	rateLimit *dispatch.ThrottleLedger
	facts     requestcontext.RequestFacts
	start     time.Time
}

func admitTurn(ctx context.Context, s turnAdmissionServices, clock auth.Clock, r *http.Request) (context.Context, admittedTurn, error) {
	ctx, err := s.checkUserMonthlySpendLimit(ctx)
	if err != nil {
		return ctx, admittedTurn{}, err
	}
	ctx = withTurnFailures(ctx)
	ctx, rateLimit := s.withRateLimitTurn(ctx)
	turn := admittedTurn{rateLimit: rateLimit, start: clock.Now(), facts: requestFactsFrom(ctx, r.Header)}
	return telemetry.WithRecorder(ctx, s.newTelemetryRecorder()), turn, nil
}
