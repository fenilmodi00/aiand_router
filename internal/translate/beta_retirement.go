package translate

import (
	"net/http"

	"weave-os/router/internal/requestcontext"
)

// BetaRetiredMessage is shared with historical-control stripping so replies never become prompts.
const BetaRetiredMessage = "Beta has been retired. Internal routing requires authenticated account enrollment."

// WriteRetiredBetaRequest recognizes only the legacy control shape and emits a protocol-local reply.
// Ordinary request bytes are never emitted from the temporary parsing envelope.
func WriteRetiredBetaRequest(w http.ResponseWriter, r *http.Request, body []byte, surface requestcontext.ConversationSurface) (bool, error) {
	var envelope *RequestEnvelope
	var err error
	switch surface {
	case requestcontext.ConversationAnthropic:
		envelope, err = ParseAnthropic(body)
	case requestcontext.ConversationChat:
		envelope, err = ParseOpenAI(body)
	case requestcontext.ConversationResponses:
		var converted ResponsesConversion
		converted, err = ConvertResponsesToChatCompletions(body)
		if err == nil {
			envelope, err = ParseOpenAI(converted.Body)
		}
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, found := envelope.ExtractBetaCommand(); !found {
		return false, nil
	}
	text := "✦ **Weave Router** → " + BetaRetiredMessage + "\n\n"
	switch surface {
	case requestcontext.ConversationAnthropic:
		return true, WriteSyntheticAnthropicResponse(w, envelope, text, 0)
	case requestcontext.ConversationResponses:
		writer := NewResponsesWriter(w, "weave-router")
		if err := WriteSyntheticOpenAIResponse(writer, envelope, text, 0); err != nil {
			return true, err
		}
		return true, writer.Finalize()
	default:
		return true, WriteSyntheticOpenAIResponse(w, envelope, text, 0)
	}
}
