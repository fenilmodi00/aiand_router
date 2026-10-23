package proxy_test

import (
	"fmt"
)

const (
	capAnthropicModel = "deepseek/deepseek-v4-pro"
	capChatModel      = "deepseek/deepseek-v4.1-flash"
	capResponsesModel = "gpt-5.6-luna"
	capGeminiModel    = "gemini-3-flash-preview"
)

func capWireResponse(wire string, stream, capped bool) string {
	switch wire {
	case "anthropic":
		reason := "end_turn"
		if capped {
			reason = "max_tokens"
		}
		if !stream {
			return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":%q,"usage":{"input_tokens":100,"output_tokens":32000}}`, reason)
		}
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":100}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":32000}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", reason)
	case "chat":
		reason := "stop"
		if capped {
			reason = "length"
		}
		if !stream {
			return fmt.Sprintf(`{"id":"chat_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":%q}],"usage":{"prompt_tokens":100,"completion_tokens":32000}}`, reason)
		}
		return "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
			fmt.Sprintf("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":32000}}\n\ndata: [DONE]\n\n", reason)
	case "gemini":
		reason := "STOP"
		if capped {
			reason = "MAX_TOKENS"
		}
		body := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":32000,"totalTokenCount":32100}}`, reason)
		if stream {
			return "data: " + body + "\n\n"
		}
		return body
	case "responses":
		status, details := "completed", "null"
		if capped {
			status, details = "incomplete", `{"reason":"max_output_tokens"}`
		}
		body := fmt.Sprintf(`{"id":"resp_1","object":"response","status":%q,"incomplete_details":%s,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":100,"output_tokens":32000}}`, status, details)
		if !stream {
			return body
		}
		return "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"answer\"}\n\n" +
			fmt.Sprintf("data: {\"type\":\"response.%s\",\"response\":%s}\n\n", status, body)
	}
	panic("unknown test wire format")
}
