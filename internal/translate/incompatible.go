package translate

import "errors"

// ErrGeminiSchemaIncompatible marks a JSON Schema constraint that the Gemini
// function declaration schema cannot represent without changing its meaning.
var ErrGeminiSchemaIncompatible = errors.New("gemini schema is not representable")

// ErrGeminiToolDeclarationConflict marks duplicate function names with
// different declarations. Gemini would make their resolution ambiguous.
var ErrGeminiToolDeclarationConflict = errors.New("gemini function declarations conflict")

// ErrGeminiUnsignedToolHistory marks a Gemini 3.x continuation that would
// require deleting prior tool history to reach the upstream.
var ErrGeminiUnsignedToolHistory = errors.New("gemini tool history lacks thought signatures")

// IsIntrinsicallyIncompatible reports whether err marks a request the routed
// model provably cannot serve (unrepresentable tool schema, reasoning intent,
// or missing Gemini thought signatures on tool history) — as opposed to a
// transient upstream fault.
func IsIntrinsicallyIncompatible(err error) bool {
	return errors.Is(err, ErrGeminiSchemaIncompatible) ||
		errors.Is(err, ErrReasoningIncompatible) ||
		errors.Is(err, ErrGeminiUnsignedToolHistory)
}
