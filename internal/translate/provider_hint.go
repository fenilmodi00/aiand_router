package translate

import "strings"

// isGLM53Flash reports whether the model id is GLM-5.3-Flash under either
// known namespace: z-ai/ (the OpenRouter-era form) or zai-org/ (AIand's
// catalog form). It needs tool_stream=true opted in
// (docs.z.ai/guides/vlm/glm-5.3-flash). Unlike GLM-5.1, thinking can't be
// disabled, so it gets no chat_template_kwargs handling.
func isGLM53Flash(model string) bool {
	return model == "z-ai/glm-5.3-flash" || model == "zai-org/glm-5.3-flash"
}

// isQwen3Family reports whether the model id belongs to the qwen3.x family.
// These variants drift into tool-call/thinking loops without the model
// card's recommended sampling defaults, which we layer in when unset.
func isQwen3Family(model string) bool {
	return strings.HasPrefix(model, "qwen/qwen3")
}

// Qwen3 sampling defaults from the official model card
// (huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507), applied only when the
// client hasn't set the field. presence_penalty=1.5 suppresses the
// "same tool, same args, N times" loop the Instruct variant is prone to.
const (
	qwen3Temperature       = 0.7
	qwen3TopP              = 0.8
	qwen3PresencePenalty   = 1.5
	qwen3RepetitionPenalty = 1.05
)
