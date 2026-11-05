package proxy

import (
	"context"
	"errors"
	"fmt"
)

// ErrForcedModelExcluded is returned when a caller forces a model the
// installation's exclusion policy forbids — exclusions win loudly so the
// caller isn't silently served a different model every turn.
var ErrForcedModelExcluded = errors.New("forced model is excluded")

// ForcedModelExcludedError carries the caller-facing reason a forced model
// was refused, so ClassifyDispatchError can name the model in the response.
type ForcedModelExcludedError struct {
	Model  string
	Reason string
}

// Error implements error.
func (e *ForcedModelExcludedError) Error() string { return e.Reason }

// Unwrap ties the typed error to ErrForcedModelExcluded for errors.Is.
func (e *ForcedModelExcludedError) Unwrap() error { return ErrForcedModelExcluded }

// forcedModelBinding resolves the provider a forced model should pin to
// under exclusion policy. reason is "" when servable; provider is returned
// as-is for passthrough IDs the catalog doesn't carry.
func (s *Service) forcedModelBinding(ctx context.Context, model, provider string) (binding, reason string) {
	// Checked directly, not via the exclusion set: a forced model may be
	// passthrough-only and so never enters the desugared exclusions.
	if !modelPermittedByAllowlist(ctx, model) {
		return "", fmt.Sprintf("%s is not on this organization's allowed-model list", model)
	}
	if _, drop := s.policyExcludedModels(ctx)[model]; drop {
		return "", fmt.Sprintf("%s is excluded on this installation", model)
	}
	// Provider exclusions no longer exist: with a single provider, excluding
	// it would serve nothing, so the requested provider is always servable
	// (passthrough IDs included).
	return provider, ""
}
