package proxy_test

import (
	"io"
	"net/http"
)

// responsesTextUpstream answers a text-only Responses stream whose usage
// reports a partially cached prefix.
func responsesTextUpstream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"cached answer"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"cached answer"}]}],"usage":{"input_tokens":40,"output_tokens":6,"input_tokens_details":{"cached_tokens":32}}}}`,
	} {
		_, _ = io.WriteString(w, "data: "+frame+"\n\n")
	}
}
