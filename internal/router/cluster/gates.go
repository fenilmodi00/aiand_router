package cluster

import "strings"

// GateConfig routes requests whose primary cluster falls in PrimaryClusters
// to the fixed Model, overriding the blend argmax. A gate never routes to a
// model outside the request's eligible pool, so a disabled or excluded gate
// model degrades to plain argmax.
type GateConfig struct {
	Enabled         bool
	PrimaryClusters []int
	Model           string
}

// DifficultyGateConfig is a GateConfig plus the hard-signal thresholds: a
// request gated by primary cluster is only escalated when its prompt is long
// enough on its own (HardMinChars), carries code-register markers at half
// that bar (HardCodeMinChars), or carries instruction-register markers above
// the short-prompt floor (HardInstructionMinChars).
type DifficultyGateConfig struct {
	Enabled                 bool
	PrimaryClusters         []int
	Model                   string
	HardMinChars            int
	HardCodeMinChars        int
	HardInstructionMinChars int
}

// Gate names emitted in RoutingMetadata.Gate.
const (
	GateTrivial    = "trivial"
	GateDifficulty = "difficulty"
)

// applyGates resolves the selection-policy gate for a scored request.
// Trivial is checked first; difficulty wins if both could apply (their
// cluster sets are disjoint by configuration). Returns ("", "") when no gate
// fires, meaning plain argmax stands.
func applyGates(trivial GateConfig, difficulty DifficultyGateConfig, primaryCluster, promptChars int, embeddedText string, eligible []string) (model, gate string) {
	if trivial.Enabled &&
		gateMatchesCluster(trivial.PrimaryClusters, primaryCluster) &&
		gateModelEligible(trivial.Model, eligible) {
		return trivial.Model, GateTrivial
	}
	if difficulty.Enabled &&
		gateMatchesCluster(difficulty.PrimaryClusters, primaryCluster) &&
		gateModelEligible(difficulty.Model, eligible) &&
		hardSignal(promptChars, embeddedText, difficulty.HardMinChars, difficulty.HardCodeMinChars, difficulty.HardInstructionMinChars) {
		return difficulty.Model, GateDifficulty
	}
	return "", ""
}

// gateMatchesCluster reports whether c is one of the configured clusters.
func gateMatchesCluster(clusters []int, c int) bool {
	for _, k := range clusters {
		if k == c {
			return true
		}
	}
	return false
}

// gateModelEligible reports whether the gate's target is in the request's
// eligible pool (already provider-resolved and exclusion-filtered), so a gate
// can never route to an unavailable model.
func gateModelEligible(model string, eligible []string) bool {
	if model == "" {
		return false
	}
	for _, m := range eligible {
		if m == model {
			return true
		}
	}
	return false
}

// hardSignal reports whether the request looks hard enough to escalate:
// prompt long enough on its own, code-register markers present at half the
// full-length bar (code-dense short prompts are rarely trivial), or
// instruction-register markers above the short-prompt floor (short hard
// task phrasings: imperative dev verbs, defect and reliability vocabulary,
// dev-workflow nouns, identifier-like tokens). embeddedText is the
// tail-truncated text the scorer embedded; promptChars is the full prompt
// length, since truncation would otherwise cap the signal below every useful
// threshold. A zero threshold disables its path.
func hardSignal(promptChars int, embeddedText string, minChars, codeMinChars, instructionMinChars int) bool {
	if minChars > 0 && promptChars >= minChars {
		return true
	}
	if codeMinChars > 0 && promptChars >= codeMinChars && hasCodeRegister(embeddedText) {
		return true
	}
	return instructionMinChars > 0 && promptChars >= instructionMinChars && hasInstructionRegister(embeddedText)
}

// codeRegisterMarkers are substrings that mark code-register prompt text:
// fenced code blocks, stack traces / runtime panics, and tool-use JSON
// shapes. Order-insensitive; any hit counts.
var codeRegisterMarkers = []string{
	"```",                              // fenced code block
	"Traceback (most recent call last", // Python stack trace
	"panic: ",                          // Go panic
	"goroutine ",                       // Go stack dump
	"\tat ",                            // JVM/JS stack frame
	"Exception in thread",              // Java stack trace
	"runtime error: ",                  // Go runtime panic message
	"SIGSEGV",                          // crash marker
	"\"tool_use\"",                     // Anthropic tool-use JSON block
	"\"tool_calls\"",                   // OpenAI tool-call JSON shape
	"\"function_call\"",                // legacy OpenAI tool-call shape
}

// hasCodeRegister reports whether text carries any code-register marker.
func hasCodeRegister(text string) bool {
	for _, m := range codeRegisterMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// instructionPhrases are multi-word or hyphenated substring markers for the
// instruction register: imperative dev-verb phrases, defect nouns, and
// reliability/distributed-systems vocabulary. Matched case-insensitively on
// the embedded text; any hit counts.
var instructionPhrases = []string{
	// defect / failure nouns
	"memory leak",
	// reliability & distributed-systems concepts
	"exactly-once", "at-least-once", "circuit breaker", "graceful shutdown",
	"connection pool", "connection exhaustion", "optimistic concurrency",
	"thundering herd", "multi-tenant", "multi-tenancy", "per-tenant",
	// dev-workflow nouns
	"test suite", "unit tests", "integration tests", "env var",
	"breaking change", "feature flag", "hard-coded", "hardcoded",
}

// instructionWords are single-token markers (matched on word boundaries so
// "build" matches "the build is broken" but not "rebuild"). Classes:
// imperative dev verbs, defect nouns, reliability concepts, and dev-workflow
// /ops nouns. Kept deliberately generic — no probe-specific strings.
var instructionWords = []string{
	// imperative dev verbs
	"implement", "refactor", "redesign", "profile", "migrate", "optimize",
	"troubleshoot", "investigate", "trace",
	// defect / failure nouns
	"bug", "flaky", "regression", "regressed", "deadlock", "leak", "crash",
	// reliability & distributed-systems concepts
	"idempotent", "idempotency", "backpressure", "backoff", "throughput",
	"downtime", "distributed", "dedup", "resumable", "checkpoint",
	// dev-workflow / ops nouns
	"deploy", "staging", "prod", "production", "repo", "npm", "linter",
	"grep", "todo", "cron", "docker", "kubernetes", "coverage", "benchmark",
	"build", "migration", "middleware", "e2e", "ci",
}

// hasInstructionRegister reports whether text carries instruction-register
// markers: the phrasing of a concrete engineering task (imperative dev verbs,
// defect and reliability vocabulary, dev-workflow nouns, identifier-like
// tokens) rather than conversation, definitional knowledge questions, or
// copywriting. Callers apply the short-prompt length floor.
func hasInstructionRegister(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, m := range instructionPhrases {
		if strings.Contains(lower, m) {
			return true
		}
	}
	for _, w := range instructionWords {
		if containsWord(lower, w) {
			return true
		}
	}
	return hasIdentifierCase(text)
}

// containsWord reports whether word occurs in lower as a standalone token
// (both neighbors are non-alphanumeric). lower must already be lowercase.
func containsWord(lower, word string) bool {
	for i := 0; ; {
		j := strings.Index(lower[i:], word)
		if j < 0 {
			return false
		}
		i += j
		before := i == 0 || !isAlnumByte(lower[i-1])
		end := i + len(word)
		after := end == len(lower) || !isAlnumByte(lower[end])
		if before && after {
			return true
		}
		i++
	}
}

func isAlnumByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// hasIdentifierCase reports whether text contains identifier-like casing: a
// lower→upper transition inside one token (camelCase, e.g. "getUser") or a
// SCREAMING_SNAKE token (uppercase letters joined by underscores, e.g.
// "OPENAI_API_KEY"). Scanned on the original text; case folding would erase
// the signal.
func hasIdentifierCase(text string) bool {
	inToken := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case isAlnumByte(c):
			if inToken && i > 0 && c >= 'A' && c <= 'Z' && isAlnumByte(text[i-1]) && text[i-1] >= 'a' && text[i-1] <= 'z' {
				return true // camelCase hump
			}
			inToken = true
		case c == '_':
			if inToken {
				// Underscore only continues a token if what surrounds it is
				// SCREAMING_SNAKE-shaped; checked per token below.
				if isScreamingSnakeAround(text, i) {
					return true
				}
			}
			inToken = false
		default:
			inToken = false
		}
	}
	return false
}

// isScreamingSnakeAround reports whether the underscore at index i joins
// uppercase letters on both sides within the same token, i.e. text looks
// like "...A_A..." with alnum-or-underscore neighbors — the
// SCREAMING_SNAKE_CASE shape (rejects snake_case by requiring uppercase
// letters around the underscore).
func isScreamingSnakeAround(text string, i int) bool {
	prevUpper := i > 0 && text[i-1] >= 'A' && text[i-1] <= 'Z'
	nextUpper := i+1 < len(text) && text[i+1] >= 'A' && text[i+1] <= 'Z'
	return prevUpper && nextUpper
}

// nearestCluster returns the single centroid closest to vec by cosine
// similarity (dot product on L2-normed vectors), tie-breaking by lower index.
// topPNearest returns its result sorted ascending by cluster id, so the
// primary cluster cannot be read off topClusters[0].
func nearestCluster(vec []float32, c *Centroids) int {
	best, bestSim := 0, float32(-1e30)
	for k := 0; k < c.K; k++ {
		row := c.Row(k)
		var sim float32
		for i, v := range row {
			sim += v * vec[i]
		}
		if sim > bestSim {
			best, bestSim = k, sim
		}
	}
	return best
}
