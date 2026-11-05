package translate

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// generateChatCmplID mints a chat.completion id for synthetic chat frames.
func generateChatCmplID() string {
	return "chatcmpl-" + randomHex(8)
}

// generateAnthropicMsgID mints a message id for synthetic Anthropic frames.
func generateAnthropicMsgID() string {
	return "msg_" + randomHex(8)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}

// clampToModelOutputCap caps v to the model's max output token limit.
func clampToModelOutputCap(v int64, model string) int64 {
	outputCap := modelMaxOutputTokens[model]
	if outputCap == 0 {
		outputCap = defaultMaxOutputTokenCap
	}
	if v > int64(outputCap) {
		return int64(outputCap)
	}
	return v
}
