package cluster

import (
	"fmt"
	"math"
	"sort"
)

// ModelReachability is one deployed model's answer to "can the cluster scorer
// ever pick this model on merit, and at what price?" — the runtime form of an
// analysis an operator would otherwise have to run offline against the bundle.
//
// Cluster wins are counted over cluster centroids (one vote per cluster, the
// same equal-weight accounting RoutingDistribution uses), never over live
// traffic. A model that wins no cluster is still reachable on demand — an
// explicit RequestedModel, a PreferredModels bonus, a pin, or a relaxed cap —
// so "0 clusters won" means "never chosen automatically", not "unusable".
type ModelReachability struct {
	Model                      string  `json:"model"`
	EffectiveCostPer1KInputUSD float64 `json:"effective_cost_per_1k_input_usd"`
	// CostMultipleVsCheapest is EffectiveCost divided by the cheapest deployed
	// lane's effective cost (1.0 for the cheapest lane itself).
	CostMultipleVsCheapest float64 `json:"cost_multiple_vs_cheapest"`
	// InsideCostCapLane reports whether the model survives the deployment-wide
	// cost cap at shipped knobs. Always true when the cap is disabled
	// (CostCapMultiple <= 0), which leaves the whole roster eligible. Under a
	// cluster-mode cap this is the roster-wide lane the global budget keeps; a
	// model outside it can still win the clusters where it leads (see
	// ClustersWonWithCostCap).
	InsideCostCapLane bool `json:"inside_cost_cap_lane"`
	// ClustersWonWithCostCap / ClustersWonWithoutCostCap list the cluster
	// indices whose centroid routes to this model at the bundle's shipped
	// knobs, with the deployment cost cap applied to the candidate set and
	// with the cap disabled (the blend's own reachability).
	ClustersWonWithCostCap    []int `json:"clusters_won_with_cost_cap"`
	ClustersWonWithoutCostCap []int `json:"clusters_won_without_cost_cap"`
	// WinsAlphaLow / WinsAlphaHigh bound the uniform-alpha interval in [0, 1]
	// where this model wins at least one cluster; nil when it wins none. The
	// sweep has ReachabilityReport.AlphaGridPoints resolution and ignores
	// alpha_floor — it measures the raw blend, which is what exposes a model
	// that wins nowhere at all.
	WinsAlphaLow  *float64 `json:"wins_alpha_low,omitempty"`
	WinsAlphaHigh *float64 `json:"wins_alpha_high,omitempty"`
}

// ReachabilityReport is the loaded bundle's answer to "will this router route
// all needful models when needed?" for one deployment: which deployed models
// can actually win traffic, where they sit on the cost ladder relative to the
// cap, and the alpha interval where each can win anything at all.
type ReachabilityReport struct {
	ClusterVersion string `json:"cluster_version"`
	ClusterCount   int    `json:"cluster_count"`
	// CostCapMultiple is the scorer's effective cap (0 = disabled).
	CostCapMultiple float64 `json:"cost_cap_multiple"`
	// CostCapMode is the budget shape the cap was applied with: "global" or
	// "cluster" (see Config.CostCapMode). Reported so a winner list is read
	// against the budget that produced it.
	CostCapMode string `json:"cost_cap_mode"`
	// AlphaGridPoints is the resolution of the uniform-alpha sweep behind
	// WinsAlphaLow/WinsAlphaHigh: reported so a narrow interval is not misread
	// as an exact boundary.
	AlphaGridPoints int `json:"alpha_grid_points"`
	// Models is the deployed roster, cheapest effective cost first.
	Models []ModelReachability `json:"models"`
	// WinnersWithCostCap / WinnersWithoutCostCap name the models that win at
	// least one cluster with the deployment cap applied / with it disabled.
	// A model absent from both is never chosen automatically.
	WinnersWithCostCap    []string `json:"winners_with_cost_cap"`
	WinnersWithoutCostCap []string `json:"winners_without_cost_cap"`
}

// Reachability scores every cluster centroid through the live blend at the
// bundle's shipped knobs — once with the deployment cost cap applied to the
// candidate set and once without — then sweeps a uniform alpha to bound the
// interval each model can win in.
//
// Read-only: it reuses Route's own inputs (defaultActiveKnobs, blendScoresV2,
// argmax, applyCostCap, effectiveCost) and feeds nothing back, so calling it
// cannot move a routing decision. Costs O(K*TopP) blends at shipped knobs plus
// qualityBiasCalibrationGrid * K for the alpha sweep — a few milliseconds on
// K=16, so callers may serve it per request without caching.
//
// Errors on v1 bundles, which carry no quality_means to blend (same contract as
// RoutingDistribution).
func (s *Scorer) Reachability() (ReachabilityReport, error) {
	if !s.isV2 {
		return ReachabilityReport{}, fmt.Errorf("%w: reachability requires a v2 bundle", ErrClusterUnavailable)
	}
	knobs := s.defaultActiveKnobs()
	k := s.centroids.K
	centroidTop := s.allCentroidTopClusters()

	// The deployment-wide capped candidate order, priced at shipped knobs —
	// Route's own gate. Cluster mode computes a per-prompt order instead (see
	// the loop below), which can keep a premium lane routable on the clusters
	// where it leads; the global lane set is still reported per model as
	// InsideCostCapLane so the roster-wide ladder stays readable.
	capMode := s.cfg.CostCapMode
	if mode, ok := NormalizeCostCapMode(capMode); !ok {
		capMode = CostCapModeCluster
	} else {
		capMode = mode
	}
	capped := s.models
	if s.cfg.CostCapMultiple > 0 {
		capped = s.applyCostCap(s.models, s.cfg.CostCapMultiple, knobs)
	}

	wonCapped := make(map[string][]int, len(s.models))
	wonUncapped := make(map[string][]int, len(s.models))
	for c := range k {
		// One blend per centroid: the score vector covers the deployed set
		// either way, so the winner is an argmax over a candidate order —
		// exactly what Route does after applyCostCap(Cluster).
		top := centroidTop[c]
		scores := s.blendScoresV2(top, knobs, s.models, nil, nil)
		if w, _ := argmax(scores, s.models); w != "" {
			wonUncapped[w] = append(wonUncapped[w], c)
		}
		allowed := capped
		if capMode == CostCapModeCluster {
			allowed = s.applyCostCapCluster(top, knobs, s.models, s.cfg.CostCapMultiple, nil, nil)
		}
		if w, _ := argmax(scores, allowed); w != "" {
			wonCapped[w] = append(wonCapped[w], c)
		}
	}

	// Cost ladder over the deployed roster, on the same effective cost the
	// blend and the cap use (input + output_cost_ratio * output * verbosity).
	minCost := math.Inf(1)
	for _, m := range s.models {
		if c := s.effectiveCost(m, knobs); c < minCost {
			minCost = c
		}
	}
	if math.IsInf(minCost, 1) || math.IsNaN(minCost) {
		return ReachabilityReport{}, fmt.Errorf("%w: deployed roster has no finite effective cost", ErrClusterUnavailable)
	}

	// Mirrors computeDialCalibration's sweep (same grid, same centroids, same
	// blend) but records the first and last uniform alpha each model wins in
	// instead of the mix-change breakpoints. AlphaFloor is deliberately not
	// applied: this measures what the blend can do, not what the dial reaches.
	alphaLo := make(map[string]float64, len(s.models))
	alphaHi := make(map[string]float64, len(s.models))
	for g := range qualityBiasCalibrationGrid {
		a := float64(g) / float64(qualityBiasCalibrationGrid-1)
		sweep := knobs
		sweep.Alpha = make([]float64, k)
		for i := range sweep.Alpha {
			sweep.Alpha[i] = a
		}
		for c := range k {
			w, _ := argmax(s.blendScoresV2(centroidTop[c], sweep, s.models, nil, nil), s.models)
			if w == "" {
				continue
			}
			if _, seen := alphaLo[w]; !seen {
				alphaLo[w] = a
			}
			alphaHi[w] = a
		}
	}

	winnersCapped := make([]string, 0, len(s.models))
	winnersUncapped := make([]string, 0, len(s.models))
	rows := make([]ModelReachability, 0, len(s.models))
	// The deployment-wide lane set, reported per model so a cluster-mode
	// deployment still shows which lanes the global cap keeps routable
	// everywhere (cluster mode budgets per prompt, so a lane outside this set
	// can still win the clusters where it leads — see the capped cluster wins).
	lane := make(map[string]struct{}, len(capped))
	for _, m := range capped {
		lane[m] = struct{}{}
	}
	for _, m := range s.models {
		cost := s.effectiveCost(m, knobs)
		_, inLane := lane[m]
		row := ModelReachability{
			Model:                      m,
			EffectiveCostPer1KInputUSD: cost,
			CostMultipleVsCheapest:     cost / minCost,
			InsideCostCapLane:          s.cfg.CostCapMultiple <= 0 || inLane,
			ClustersWonWithCostCap:     cloneClusterIDs(wonCapped[m]),
			ClustersWonWithoutCostCap:  cloneClusterIDs(wonUncapped[m]),
		}
		if lo, ok := alphaLo[m]; ok {
			low, high := lo, alphaHi[m]
			row.WinsAlphaLow, row.WinsAlphaHigh = &low, &high
		}
		if len(wonCapped[m]) > 0 {
			winnersCapped = append(winnersCapped, m)
		}
		if len(wonUncapped[m]) > 0 {
			winnersUncapped = append(winnersUncapped, m)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CostMultipleVsCheapest != rows[j].CostMultipleVsCheapest {
			return rows[i].CostMultipleVsCheapest < rows[j].CostMultipleVsCheapest
		}
		return rows[i].Model < rows[j].Model
	})

	return ReachabilityReport{
		ClusterVersion:        s.version,
		ClusterCount:          k,
		CostCapMultiple:       s.cfg.CostCapMultiple,
		CostCapMode:           capMode,
		AlphaGridPoints:       qualityBiasCalibrationGrid,
		Models:                rows,
		WinnersWithCostCap:    winnersCapped,
		WinnersWithoutCostCap: winnersUncapped,
	}, nil
}

// cloneClusterIDs copies a winner list so an empty one serializes as [] rather
// than null and callers can't mutate the report's internals.
func cloneClusterIDs(in []int) []int {
	out := make([]int, len(in))
	copy(out, in)
	return out
}

// DefaultReachability reports which deployed models the default version's
// bundle can route to on merit — the version live traffic routes through absent
// a per-request override, matching DefaultRoutingDistribution's choice. A nil
// Multiversion (a router that isn't the cluster scorer) is not an error: it
// means there is no bundle to report on, and callers render the block as null.
func (m *Multiversion) DefaultReachability() (ReachabilityReport, error) {
	if m == nil {
		return ReachabilityReport{}, fmt.Errorf("cluster multiversion: no router wired")
	}
	s, ok := m.Versions[m.Default]
	if !ok {
		return ReachabilityReport{}, fmt.Errorf("cluster multiversion: default version %q not built: %w", m.Default, ErrClusterUnavailable)
	}
	return s.Reachability()
}
