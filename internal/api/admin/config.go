package admin

import (
	"net/http"

	"weave-os/router/internal/config"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type configResponse struct {
	ClusterVersion       string `json:"cluster_version"`
	EmbedOnlyUserMsg     bool   `json:"embed_only_user_message"`
	StickyDecisionTTL    string `json:"sticky_decision_ttl_ms"`
	OtelEnabled          bool   `json:"otel_enabled"`
	SemanticCacheEnabled bool   `json:"semantic_cache_enabled"`
	// EnvProviderKeys lists provider names whose upstream API key is set
	// via env var on the deployment (e.g. OPENAI_API_KEY). The dashboard
	// renders these as read-only entries — they aren't stored in Postgres
	// and can only be unset by editing the deployment env + restarting.
	EnvProviderKeys []string `json:"env_provider_keys"`
	// Reachability answers "will this router route all needful models when
	// needed?" from the loaded cluster bundle: per deployed model, the clusters
	// it wins at shipped knobs (with and without the cost cap), where it sits
	// on the effective-cost ladder, and the alpha interval where it can win at
	// all. Null when the router carries no cluster scorer.
	Reachability *cluster.ReachabilityReport `json:"cluster_reachability"`
}

// ReachabilitySource reports which deployed models the default cluster bundle
// can route to on merit. Implemented by *cluster.Multiversion; a nil source
// (non-cluster router, or a unit test without one) leaves the field null
// rather than failing the whole config response.
type ReachabilitySource interface {
	DefaultReachability() (cluster.ReachabilityReport, error)
}

var _ ReachabilitySource = (*cluster.Multiversion)(nil)

// ConfigHandler serves GET /admin/v1/config: the deployment's own wiring —
// active cluster artifact, sticky-decision and cache settings, plus which
// provider keys are env-pinned — together with the loaded bundle's model
// reachability so an operator can see which models the cluster scorer will
// actually pick, instead of deriving it from an offline report.
//
// reachability may be nil: routers that aren't the cluster scorer then get the
// same config payload with a null reachability block. The field is admin-only
// and derived from the publicly published artifact, so it adds no leak surface.
func ConfigHandler(reachability ReachabilitySource) gin.HandlerFunc {
	return func(c *gin.Context) {
		if middleware.AdminPrincipalFrom(c) == nil && middleware.InstallationFrom(c) == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		// Iterate every known provider (sorted) rather than a hand-maintained
		// display list, so a newly-added provider's env key shows up in the
		// dashboard without a separate edit here.
		providerOrder := providers.AllProviders()
		envKeyed := make([]string, 0, len(providerOrder))
		for _, p := range providerOrder {
			if config.GetOr(providers.APIKeyEnvVar(p), "") != "" {
				envKeyed = append(envKeyed, p)
			}
		}
		var reach *cluster.ReachabilityReport
		if reachability != nil {
			report, err := reachability.DefaultReachability()
			if err != nil {
				// A scorer that can't report (e.g. a v1 bundle) must not cost
				// the operator the rest of the config; log and leave it null.
				observability.FromGin(c).Warn("Cluster reachability unavailable", "err", err)
			} else {
				reach = &report
			}
		}
		c.JSON(http.StatusOK, configResponse{
			ClusterVersion:       config.GetOr("ROUTER_CLUSTER_VERSION", "artifacts/latest"),
			EmbedOnlyUserMsg:     config.GetOr("ROUTER_EMBED_ONLY_USER_MESSAGE", "true") == "true",
			StickyDecisionTTL:    config.GetOr("ROUTER_STICKY_DECISION_TTL_MS", "0"),
			OtelEnabled:          config.GetOr("OTEL_EXPORTER_OTLP_ENDPOINT", "") != "",
			SemanticCacheEnabled: config.GetOr("ROUTER_SEMANTIC_CACHE_ENABLED", "true") == "true",
			EnvProviderKeys:      envKeyed,
			Reachability:         reach,
		})
	}
}
