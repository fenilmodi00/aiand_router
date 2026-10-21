package proxy_test

import (
	"fmt"
	"io"
	"net/http"
)

const reasoningProgressModel = "z-ai/glm-5.3"

func completedReasoningFrame(index int) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"reasoning","id":"rs_%d","encrypted_content":"opaque_%d","summary":[]}}`, index, index, index)
}

func flushResponsesFrame(w http.ResponseWriter, frame string) bool {
	_, err := io.WriteString(w, "data: "+frame+"\n\n")
	w.(http.Flusher).Flush()
	return err == nil
}
