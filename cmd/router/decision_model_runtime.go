package main

import (
	"log/slog"

	"aiand-os/router/internal/config"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/decisionmodel"
	openaiCompatProvider "aiand-os/router/internal/providers/openaicompat"
)

const defaultDecisionModelBaseURL = openaiCompatProvider.AIANDBaseURL

// buildDecisionModelRouter registers the decision model strategy only when a judge credential is configured.
func buildDecisionModelRouter(logger *slog.Logger) router.Router {
	apiKey := config.GetOr("ROUTER_DECISION_MODEL_API_KEY", config.GetOr("AIAND_API_KEY", ""))
	if apiKey == "" {
		logger.Info("decision model routing disabled: no judge credential")
		return nil
	}
	baseURL := config.GetOr("ROUTER_DECISION_MODEL_BASE_URL", config.GetOr("AIAND_BASE_URL", defaultDecisionModelBaseURL))
	timeout := parseEnvDurationMs("ROUTER_DECISION_MODEL_TIMEOUT_MS", decisionmodel.DefaultTimeout)
	logger.Info("decision model routing enabled", "base_url", baseURL, "timeout", timeout)
	return decisionmodel.New(decisionmodel.NewClient(baseURL, apiKey, timeout))
}
