package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
)

// codexSubscriptionModelRejected reports an explicit model-availability
// rejection from the Codex backend. Generic 400/403 responses can reflect
// request or account policy and must not trigger paid fallback.
func codexSubscriptionModelRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || (buffered.Status != http.StatusBadRequest && buffered.Status != http.StatusNotFound) {
		return false
	}
	var errorEnvelope struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(buffered.Body, &errorEnvelope); jsonErr != nil {
		return false
	}
	switch errorEnvelope.Error.Code {
	case "model_not_found", "unsupported_model", "model_not_available":
		return true
	}
	return errorEnvelope.Error.Param == "model" && errorEnvelope.Error.Type == "invalid_request_error"
}

// codexOAuthCredentialRejected reports whether err is a buffered OpenAI 401/403
// — a rejected or expired ChatGPT OAuth token, which the Weave-key retry can
// still serve. Narrower than treating every 403 as credential-related would be
// on Anthropic: the OpenAI backend answers a content-policy refusal with 400,
// not 403.
func codexOAuthCredentialRejected(err error) bool {
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	return buffered.Status == http.StatusUnauthorized || buffered.Status == http.StatusForbidden
}
