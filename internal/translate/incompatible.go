package translate

import "errors"

// IsIntrinsicallyIncompatible reports whether err marks a request the routed
// model provably cannot serve (unrepresentable tool schema, reasoning intent)
// — as opposed to a transient upstream fault.
func IsIntrinsicallyIncompatible(err error) bool {
	return errors.Is(err, ErrReasoningIncompatible)
}
