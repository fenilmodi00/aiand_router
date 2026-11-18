package proxy

import (
	"net/http"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router/turntype"
	"aiand-os/router/internal/translate"
)

func prepareAnthropicForTurn(env *translate.RequestEnvelope, in http.Header, opts translate.EmitOptions, turnType turntype.TurnType) (providers.PreparedRequest, error) {
	if turnType == turntype.Classifier && env.SourceFormat() == translate.FormatAnthropic && opts.TargetModel == env.Model() {
		return env.PrepareAnthropicVerbatim(in), nil
	}
	return env.PrepareAnthropic(in, opts)
}

func prepareOpenAIForTurn(env *translate.RequestEnvelope, in http.Header, opts translate.EmitOptions, turnType turntype.TurnType) (providers.PreparedRequest, error) {
	if turnType == turntype.Classifier {
		if prep, ok := env.PrepareOpenAIVerbatim(opts); ok {
			return prep, nil
		}
	}
	return env.PrepareOpenAI(in, opts)
}
