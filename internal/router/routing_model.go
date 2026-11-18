package router

import "context"

// RoutingModel names an automatic router instead of an upstream completion model.
type RoutingModel string

const (
	ModelAutomatic RoutingModel = "aiand-auto"
	ModelHMM       RoutingModel = "aiand-hmm-20261019-0001v1"
	ModelQwen      RoutingModel = "aiand-qwen-20261008-0001v2"
	ModelDecision  RoutingModel = "aiand-decision-model-20261019-0001v1"
)

// ModelAutomaticDisplayName is the human-facing name for ModelAutomatic that
// clients type into a custom-model field.
const ModelAutomaticDisplayName = "Aiand Auto"

// CanonicalRoutingModel maps a display name, or the upstream spellings clients
// still send, onto the routing model it selects. Matching stays exact and
// case-sensitive so other spellings are unchanged.
func CanonicalRoutingModel(model string) RoutingModel {
	switch model {
	case ModelAutomaticDisplayName, "Weave Auto", "weave-auto":
		return ModelAutomatic
	}
	return RoutingModel(model)
}

// IsRoutingModel reports whether model selects an automatic routing policy.
func IsRoutingModel(model string) bool {
	switch CanonicalRoutingModel(model) {
	case ModelAutomatic, ModelHMM, ModelQwen, ModelDecision:
		return true
	default:
		return false
	}
}

type routingModelContextKey struct{}

// WithRoutingModel records explicit selection so legacy preferences cannot override it.
func WithRoutingModel(ctx context.Context, model RoutingModel) context.Context {
	return context.WithValue(ctx, routingModelContextKey{}, model)
}

// RoutingModelFromContext returns the routing model explicitly selected by the client.
func RoutingModelFromContext(ctx context.Context) (RoutingModel, bool) {
	model, ok := ctx.Value(routingModelContextKey{}).(RoutingModel)
	return model, ok
}
