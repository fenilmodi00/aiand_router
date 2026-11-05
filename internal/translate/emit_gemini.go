package translate

import (
	"strings"

	"aiand-os/router/internal/router"
)

// usesGemini3Contract includes Argon, which retains Gemini 3 thinking levels
// and requires thought signatures on function calls.
func usesGemini3Contract(model string) bool {
	return strings.HasPrefix(model, "gemini-3") || model == router.ModelGemini4Argon
}
