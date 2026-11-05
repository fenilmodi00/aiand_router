package router

// ExclusionKind classifies why a model carries a hard exclusion on a request.
type ExclusionKind uint8

const (
	// ExclusionRequested marks the catch-all hard exclusion: installation
	// excluded_models, allowlist desugaring, the product boundary, and
	// per-turn router decisions. Never lifted by an explicit force.
	ExclusionRequested ExclusionKind = iota
	// ExclusionSafety marks hard request-time constraints — context overflow,
	// image caps, unsigned tool-call history, maxed-out loop guards — that
	// keep blocking usage-bypass and in-turn rescues even when a pinned
	// model's union membership is lifted.
	ExclusionSafety
	// ExclusionContextWindow marks models the context-window pre-filter
	// excluded and nothing else did: never policy-excluded, not re-admitted
	// on total overflow, no other hard reason. An explicit force pin on one
	// of these is still dispatched, so the provider's exact token count
	// decides instead of the estimate.
	ExclusionContextWindow
	// ExclusionImageLimit marks models excluded only because every binding
	// caps images below the request's count. An explicit force pin re-admits
	// them and clears the request's image count.
	ExclusionImageLimit
	// ExclusionUnsignedHistory marks models that cannot accept the request's
	// unsigned tool-call history. Never lifted by a force.
	ExclusionUnsignedHistory
)

// Exclusions carries a request's hard model exclusions: the union set every
// consumer membership-tests via Excludes, plus per-kind markers used only for
// diagnostics attribution and explicit-force re-admission eligibility. Every
// marker set stays within the union. The zero value excludes nothing, and
// every mutating method returns a new value instead of touching the
// receiver's maps, so requests sharing an Exclusions value never observe each
// other's mutations.
type Exclusions struct {
	union           map[string]struct{}
	safety          map[string]struct{}
	contextWindow   map[string]struct{}
	imageLimit      map[string]struct{}
	unsignedHistory map[string]struct{}
}

// RequestedExclusions returns exclusions whose union is models, tagged as
// never-force-lifted requested exclusions. Nil yields the zero value.
func RequestedExclusions(models map[string]struct{}) Exclusions {
	return Exclusions{}.ExcludeSet(models, ExclusionRequested)
}

// ExcludeModel adds model to the union tagged kind. ExclusionRequested adds
// union membership only; every other kind additionally records the marker
// that attributes the exclusion and gates force re-admission.
func (e Exclusions) ExcludeModel(model string, kind ExclusionKind) Exclusions {
	if e.tagged(model, kind) {
		return e
	}
	out := e
	out.union = withModel(e.union, model)
	if kind != ExclusionRequested {
		out.setKind(kind, withModel(e.kindSet(kind), model))
	}
	return out
}

// ExcludeSet adds every model to the union tagged kind, copying each map at
// most once. Nil or empty models returns the receiver unchanged.
func (e Exclusions) ExcludeSet(models map[string]struct{}, kind ExclusionKind) Exclusions {
	if len(models) == 0 {
		return e
	}
	out := e
	if !e.containsAll(models) {
		out.union = withModels(e.union, models)
	}
	if kind != ExclusionRequested && !e.taggedAll(models, kind) {
		out.setKind(kind, withModels(e.kindSet(kind), models))
	}
	return out
}

// RemoveModels fully re-admits models: dropped from the union and every kind
// set. Used when the router re-admits models whose estimate-based exclusion
// was provisional, so no attribution marker may survive (widest-window
// admission on total overflow).
func (e Exclusions) RemoveModels(models []string) Exclusions {
	if len(models) == 0 || !e.containsAny(models) {
		return e
	}
	out := e
	out.union = withoutModelsList(e.union, models)
	out.safety = withoutModelsList(e.safety, models)
	out.contextWindow = withoutModelsList(e.contextWindow, models)
	out.imageLimit = withoutModelsList(e.imageLimit, models)
	out.unsignedHistory = withoutModelsList(e.unsignedHistory, models)
	return out
}

// Readmit lifts model's exclusion as a live gate: removed from the union and
// the safety set, while the context-window, image-limit and unsigned-history
// markers are preserved so diagnostics keep attributing the original
// exclusion. Used when a router-side re-check clears the exclusion (context
// fit-check) or an explicit force delegates an estimate-only exclusion to the
// provider.
func (e Exclusions) Readmit(model string) Exclusions {
	if !e.Excludes(model) {
		return e
	}
	out := e
	out.union = withoutModel(e.union, model)
	out.safety = withoutModel(e.safety, model)
	return out
}

// ReadmitUnionOnly removes model from the union while leaving every kind
// marker — including safety — in place. Used by re-admissions that must not
// clear a concurrent physical constraint: the allowlist lift for a forced
// model, and the image-count lift whose marker the caller needs to zero the
// request's image count.
func (e Exclusions) ReadmitUnionOnly(model string) Exclusions {
	if !e.Excludes(model) {
		return e
	}
	out := e
	out.union = withoutModel(e.union, model)
	return out
}

// ReadmitForcedContextEstimate re-admits a model excluded only by the
// context-window byte estimate for an explicit user force: the estimate is
// not a token count, so the provider's exact count decides. Removes the model
// from the union and the safety set — an estimate-only exclusion must not
// keep blocking usage-bypass — while preserving the context-window marker for
// diagnostics. Reports whether a lift happened.
func (e Exclusions) ReadmitForcedContextEstimate(model string) (Exclusions, bool) {
	if !e.ContextWindowExcludes(model) {
		return e, false
	}
	return e.Readmit(model), true
}

// ReadmitForcedImageLimit re-admits a model excluded only by the image-count
// caps for an explicit user force: the provider receives the pinned model and
// decides with its own limits. Removes the model from the union only —
// image-limited models carry no safety marker, so a concurrent safety
// exclusion (the maxed-out loop guard) keeps blocking usage-bypass. Reports
// whether the model was image-limit excluded, which is also the caller's
// signal to zero the request's image count.
func (e Exclusions) ReadmitForcedImageLimit(model string) (Exclusions, bool) {
	if !e.ImageLimitExcludes(model) {
		return e, false
	}
	return e.ReadmitUnionOnly(model), true
}

// ReadmitForForce lifts every exclusion kind an explicit user force outranks:
// the context-window estimate and the image-count cap. Requested and
// unsigned-history exclusions are never lifted. The caller zeroes the
// request's image count itself when ImageLimitExcludes reports the image kind.
func (e Exclusions) ReadmitForForce(model string) Exclusions {
	out, _ := e.ReadmitForcedContextEstimate(model)
	out, _ = out.ReadmitForcedImageLimit(model)
	return out
}

// WithoutContextWindow drops every context-window exclusion from the union,
// for callers retrying a resolution that must ignore the byte-estimate
// pre-filter and let a direct fit-check or the provider decide. Other kind
// markers are preserved.
func (e Exclusions) WithoutContextWindow() Exclusions {
	if len(e.contextWindow) == 0 {
		return e
	}
	out := e
	out.union = withoutModelsList(e.union, setKeys(e.contextWindow))
	out.contextWindow = nil
	return out
}

// Excludes reports whether model is hard-excluded from the request. This is
// the membership test every router and policy consumer uses; the per-kind
// accessors exist only for attribution and force-decision logic.
func (e Exclusions) Excludes(model string) bool {
	_, ok := e.union[model]
	return ok
}

// IsEmpty reports whether nothing is excluded.
func (e Exclusions) IsEmpty() bool {
	return len(e.union) == 0
}

// Union exposes the union set for consumers that iterate the exclusions or
// hand them to map-shaped interfaces (hard-pin resolvers, tier restriction).
// The returned map is read-only by convention; mutate only through the
// Exclude*/Readmit* methods.
func (e Exclusions) Union() map[string]struct{} {
	return e.union
}

// SafetyExcludes reports whether model carries the safety marker.
func (e Exclusions) SafetyExcludes(model string) bool {
	_, ok := e.safety[model]
	return ok
}

// ContextWindowExcludes reports whether model carries the context-window
// marker.
func (e Exclusions) ContextWindowExcludes(model string) bool {
	_, ok := e.contextWindow[model]
	return ok
}

// ImageLimitExcludes reports whether model carries the image-limit marker.
func (e Exclusions) ImageLimitExcludes(model string) bool {
	_, ok := e.imageLimit[model]
	return ok
}

// UnsignedHistoryExcludes reports whether model carries the unsigned-history
// marker.
func (e Exclusions) UnsignedHistoryExcludes(model string) bool {
	_, ok := e.unsignedHistory[model]
	return ok
}

// HasContextWindowExclusions reports whether any model carries the
// context-window marker.
func (e Exclusions) HasContextWindowExclusions() bool {
	return len(e.contextWindow) > 0
}

func (e Exclusions) kindSet(kind ExclusionKind) map[string]struct{} {
	switch kind {
	case ExclusionSafety:
		return e.safety
	case ExclusionContextWindow:
		return e.contextWindow
	case ExclusionImageLimit:
		return e.imageLimit
	case ExclusionUnsignedHistory:
		return e.unsignedHistory
	default:
		return nil
	}
}

func (e *Exclusions) setKind(kind ExclusionKind, set map[string]struct{}) {
	switch kind {
	case ExclusionSafety:
		e.safety = set
	case ExclusionContextWindow:
		e.contextWindow = set
	case ExclusionImageLimit:
		e.imageLimit = set
	case ExclusionUnsignedHistory:
		e.unsignedHistory = set
	}
}

func (e Exclusions) tagged(model string, kind ExclusionKind) bool {
	if kind == ExclusionRequested {
		return e.Excludes(model)
	}
	_, ok := e.kindSet(kind)[model]
	return ok
}

func (e Exclusions) containsAll(models map[string]struct{}) bool {
	for model := range models {
		if _, ok := e.union[model]; !ok {
			return false
		}
	}
	return true
}

func (e Exclusions) taggedAll(models map[string]struct{}, kind ExclusionKind) bool {
	set := e.kindSet(kind)
	for model := range models {
		if _, ok := set[model]; !ok {
			return false
		}
	}
	return true
}

func (e Exclusions) containsAny(models []string) bool {
	for _, model := range models {
		if _, ok := e.union[model]; ok {
			return true
		}
	}
	return false
}

// WithModel returns a fresh map with model added; the input map is never
// mutated, so values sharing it stay isolated.
func WithModel(set map[string]struct{}, model string) map[string]struct{} {
	return withModel(set, model)
}

func withModel(set map[string]struct{}, model string) map[string]struct{} {
	out := make(map[string]struct{}, len(set)+1)
	for k := range set {
		out[k] = struct{}{}
	}
	out[model] = struct{}{}
	return out
}

func withModels(set map[string]struct{}, models map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(set)+len(models))
	for k := range set {
		out[k] = struct{}{}
	}
	for k := range models {
		out[k] = struct{}{}
	}
	return out
}

// withoutModel returns set unchanged when model is absent, so no-op
// re-admissions never reallocate.
func withoutModel(set map[string]struct{}, model string) map[string]struct{} {
	if _, ok := set[model]; !ok {
		return set
	}
	out := make(map[string]struct{}, len(set)-1)
	for k := range set {
		if k != model {
			out[k] = struct{}{}
		}
	}
	return out
}

func withoutModelsList(set map[string]struct{}, models []string) map[string]struct{} {
	dropped := false
	for _, model := range models {
		if _, ok := set[model]; ok {
			dropped = true
			break
		}
	}
	if !dropped {
		return set
	}
	out := make(map[string]struct{}, len(set))
	for k := range set {
		out[k] = struct{}{}
	}
	for _, model := range models {
		delete(out, model)
	}
	return out
}

func setKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}
