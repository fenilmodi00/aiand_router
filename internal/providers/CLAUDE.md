# internal/providers — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Provider `Client` interface + the canonical `Provider*` name constant + the concrete adapter for the one upstream this router dispatches to. Read [root CLAUDE.md](../../CLAUDE.md) first.

## Layout

- `provider.go` — `Client` interface, the `Provider*` constant, `APIKeyEnvVars` map (single source of truth for provider→env-var mapping).
- `openaicompat/` — the concrete adapter: a generic `providers.Client` for upstreams that speak OpenAI Chat Completions. ai& (`api.aiand.com/v1`) is the only upstream wired, and it uses this client.
- `httputil/` — shared transport + streaming helpers.

There is one upstream. `ai&` speaks OpenAI Chat Completions with bearer auth, so `openaicompat` is the only adapter and `FamilyOpenAICompat` is the only translation family.

## Request-scoped context

Provider adapters read per-request credentials (`Credentials`, `EffectiveBaseURL`, `ApplyModelAlias`), caller identity, and forwarded/baggage headers from the inner-ring `internal/requestcontext` package. Adapters must not import `internal/proxy`; `internal/architecture` fails the build if one does.

## Adding a second upstream

There is one upstream today (`ProviderAIAND`, the `ai&` OpenAI-compatible
Chat Completions surface). To wire a second upstream:

1. **Reuse [`openaicompat`](openaicompat)** if the upstream speaks OpenAI Chat Completions — add a sibling `*BaseURL` constant and register it in `cmd/router/main.go`. A new adapter directory is only warranted for a different wire format.
2. **Add a canonical name constant** to [`provider.go`](provider.go) next to `ProviderAIAND`, then register it in the two maps beside it: `APIKeyEnvVars` (its deployment-level env key) and `FamilyFor` (its `TranslationFamily`). The composition root reads `APIKeyEnvVars`, so the admin `/config` view can't drift from actual wiring. Skipping the `FamilyFor` case is the failure mode that silently 502s at request time (`ErrProviderNotConfigured`) instead of at boot — `families_test.go` pins the family of every provider in `AllProviders()` and `ValidateDispatchable` rejects a registered-but-unknown name.
3. **Check the non-family-based dispatch switches.** Cross-format dispatch in `internal/proxy/service.go` keys off `providers.FamilyFor` (the `TranslationFamily` enum), so a new provider reusing an existing family needs no edit there. Two switches still key off literal `Provider*` constants and are **not** driven by the family, so review whether the new provider needs its own case:
4. **Wire in `../../cmd/router/main.go`.** Only place that imports the provider package directly. The provider must be added to `providerMap` regardless of mode; `envKeyedProviders` (parallel set) tracks which providers have a deployment-level key configured.

## Ingress formats vs. the upstream

The clients speak Anthropic Messages and OpenAI Chat Completions / Responses.
`internal/api/{anthropic,openai}` and `internal/translate`'s request parsing
are **client-facing ingress** and translate those formats into the internal
envelope; `internal/translate`'s emitters then render the one upstream's
OpenAI-compat format. The client chooses the wire format, not the upstream.
## What is load-bearing

- **The training script is the only writer of `rankings.json`.** Hand-editing breaks the cluster geometry guarantee (`scorer.go`'s sorted-candidate ordering must match what training produced). Re-run `train_cluster_router.py` after touching `model_registry.json` + commit the regenerated artifact.
- **Cluster scorer is availability-aware at boot, not request time.** Filter happens in `NewScorer`; runtime argmax unchanged. Empty filtered set = hard boot error so misconfigured deploys fail loud.
- **`FamilyFor` must cover every `Provider*` constant.** A provider without a family entry 502s silently at request time instead of at boot.

## What to NOT do

- **Don't bypass the provider filter.** If you need to route to a provider whose key isn't registered (and isn't reachable via BYOK), register the provider — don't add a special-case path that ignores `availableProviders`.
- **Don't add bench-column averaging back to the training script.** 1:1 mapping is the point. Two entries that share a column copy the same score; they don't average across columns.
- **Don't add per-installation provider preference yet.** Deploy-time env config + cluster scorer's per-prompt argmax cover v1. Per-installation routing = follow-up PR with a DB migration on `model_router_installations`.
- **Don't leak provider-specific request or streaming-chunk types** across the package boundary.
