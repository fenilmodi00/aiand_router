package proxy_test

import (
	"context"
	"time"
)

// noRetrySleep replaces the retry backoff so a test does not wait it out.
func noRetrySleep(context.Context, time.Duration) error { return nil }
