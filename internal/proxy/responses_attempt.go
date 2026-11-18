package proxy

import (
	"net/http"

	"aiand-os/router/internal/translate"
)

// resetResponsesAttempt drops the Responses writer's per-attempt state so a
// retried dispatch cannot replay a failed attempt's output items.
func resetResponsesAttempt(w http.ResponseWriter) {
	if rw, ok := w.(*translate.ResponsesWriter); ok {
		rw.ResetAttempt()
	}
}
