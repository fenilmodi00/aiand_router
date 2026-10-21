package inference

// ResolvedPlan is read-only execution authorization produced by the policy
// resolver. Concrete plans keep construction fields private.
type ResolvedPlan interface {
	Purpose() Purpose
	DispatchClass() DispatchClass
	PolicyID() PolicyID
	RegistryRevision() string
	PolicyRevision() PolicyRevision
	SelectionStrategy() SelectionStrategy
	SelectedTarget() Target
	AlternativeTargets() []Target
	HardConstraints() []Constraint
	SoftPreferences() []SoftPreference
	Budget() BudgetSpec
	Provenance() PlanProvenance
}
