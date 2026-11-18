package openai

import (
	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// ResponsesHandler accepts OpenAI Responses API requests (the surface that
// Codex CLI requires after wire_api="chat" was retired) and routes them
// through the chat-completions proxy via translate.ResponsesToChatCompletions.
// Errors are written in the Responses error shape, which matches the OpenAI
// chat error envelope verbatim, so writeOpenAIError is reused.
func ResponsesHandler(svc *proxy.Service, authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, ok := prepareOpenAIProxyRequest(c, authSvc)
		if !ok {
			return
		}
		writeOpenAIProxyError(c, svc.ProxyOpenAIResponses(c.Request.Context(), body, c.Writer, c.Request))
	}
}
