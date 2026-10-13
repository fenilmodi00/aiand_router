# Configuration reference

Deployment configuration uses environment variables ([12-factor](https://12factor.net/config));
organization settings are stored in Postgres.
This page is the exhaustive reference; the [README](../README.md) has the
60-second quickstart.

## Table of contents

- [Provider API keys](#provider-api-keys)
  - [AIand-only deployment](#aiand-only-deployment)
- [Postgres](#postgres)
- [Server](#server)
- [Managed serving (`ROUTER_SERVING_*`)](#managed-serving-router_serving_)
- [Routing](#routing)
- [Provider and model exclusions](#provider-and-model-exclusions)
- [Policy sidecars](#policy-sidecars)
- [BYOK encryption](#byok-encryption)
- [Telemetry (OpenTelemetry)](#telemetry-opentelemetry)
- [Cluster-routing artifacts](#cluster-routing-artifacts)

## Provider API keys

AIand is the only registered upstream: the router builds exactly one provider
client, from `AIAND_API_KEY`, and every route lands on its curated roster.

| Variable         | Default                    | Effect |
| ---------------- | -------------------------- | ------ |
| `AIAND_API_KEY`  | *(none)*                   | Enables the AIand provider (api.aiand.com), serving its open-weights catalog through an OpenAI-compatible API. |
| `AIAND_BASE_URL` | `https://api.aiand.com/v1` | Override for the AIand endpoint. |

**Historical provider keys — none are read at boot in the AIand-only build:**
`OPENROUTER_API_KEY`, `ROUTER_MODEL_ID_MAP`, `MINIMAX_API_KEY`,
`MINIMAX_REGION`, `MINIMAX_BASE_URL`, `GOOGLE_BASE_URL`,
`ANTHROPIC_GATEWAY_BASE_URL`, `ANTHROPIC_GATEWAY_TOKEN`,
`OPENAI_GATEWAY_BASE_URL`, `OPENAI_GATEWAY_TOKEN`, `WAFER_API_KEY`,
`WAFER_BASE_URL`, `ROUTER_SUBSCRIPTION_POOLS_ENABLED`,
`WEAVE_CODEX_OAUTH_ISSUER`, `WEAVE_ANTHROPIC_OAUTH_AUTHORIZE`,
`WEAVE_ANTHROPIC_OAUTH_TOKEN`. `GOOGLE_API_KEY`'s Gemini-model-provider role is
gone as well; the variable is still read by the HMM embedding sidecar (see
[Self-hosted frozen HMM sidecar](#self-hosted-frozen-hmm-sidecar)).

**BYOK (per-installation keys).** A deployment that leaves `AIAND_API_KEY`
unset can let each installation supply its own AIand key through the dashboard
(**Settings → Provider API keys**); those are stored in Postgres and used only
for that installation's traffic, and the API refuses a dashboard key for a
provider already configured by env var. See [BYOK encryption](#byok-encryption).
`aiand` is the only registered provider, so a key stored under any other
provider name is never dispatched.

Each key may carry its own endpoint — overriding the deployment's base URL for
that provider on that installation's requests — and a model-alias map. Set both
in the dashboard or through the admin API:

```bash
# /admin/v1 mutations take the dashboard cookie, not an rk_ bearer.
curl -sS -c jar -X POST https://<router>/admin/v1/auth/login \
  -H 'content-type: application/json' -d '{"password":"<admin password>"}'

curl -sS -b jar -X POST https://<router>/admin/v1/provider-keys \
  -H 'content-type: application/json' \
  -d '{"provider":"aiand","key":"<key>","base_url":"https://aiand.internal/v1",
       "model_aliases":{"zai-org/glm-5.3":"internal.glm-5.3"}}'
```

The base URL must be an absolute `http(s)` URL; anything else is rejected with
`400`. A trailing slash is stripped and the client appends its own API path
(`/chat/completions`), so give the base only. Omit the field to keep the
deployment endpoint.

Model-alias keys are catalog model IDs (an ID outside the deployed catalog is
rejected with `400`) and values are what goes on the wire to that endpoint. Only
the outbound model name changes: routing, pricing, and analytics stay keyed on
the catalog ID. The map is editable in **Settings → Provider API keys → Edit
aliases**, or on its own endpoint, which replaces the whole map and leaves the
stored secret alone:

```bash
curl -sS -b jar -X PUT https://<router>/admin/v1/provider-keys/<key id>/model-aliases \
  -H 'content-type: application/json' \
  -d '{"model_aliases":{"zai-org/glm-5.3":"internal.glm-5.3"}}'
```

A key can also name a header its endpoint wants the caller's identity in
(`identity_header` plus `identity_header_format`), and carry the client's own
correlation headers across the hop (`forwarded_client_headers`,
`baggage_header`):

```bash
curl -sS -b jar -X POST https://<router>/admin/v1/provider-keys \
  -H 'content-type: application/json' \
  -d '{"provider":"aiand","key":"<key>",
       "identity_header":"X-Caller-Identity","identity_header_format":"json",
       "forwarded_client_headers":["X-Client-Trace","X-Claude-Code-Session-Id"],
       "baggage_header":"X-Client-Baggage"}'
```

`identity_header_format` is `email` (the bare address) or `json` (a
percent-encoded property bag: `user_email`, `user_name`, `session_id`,
`client_app`, empty fields omitted). The header is set on the upstream request
after the client's own headers, so a caller can't attribute their turns to
someone else by sending it themselves, and nothing is sent when the request
carries no identity. `forwarded_client_headers` are copied verbatim from the
inbound request (up to 16, blanks and duplicates dropped). `baggage_header` is
re-emitted with `"on-behalf-of": "<X-Weave-User-Email>"` and a boolean
`"passthrough"` field added — other keys are preserved, and client-supplied
values for those fields are replaced. Naming a request-critical header
(`Authorization`, `x-api-key`, `Host`, `Content-Type`, `Content-Length`,
`Accept`) is rejected with `400`. Omit all three fields to forward nothing.

### AIand-only deployment

The router is AIand-only: boot registers exactly one provider client (AIAND),
so every automatic route lands on the curated 8-model AIand roster (glm-5.3,
glm-5.3-flash, kimi-k3, deepseek-v4-pro, v4-flash, v4.1-flash, qwen3.8-27b,
motif-3). Enforcement is layered: registration admits only AIAND, and the
cluster bundle's model registry (`artifacts/latest`, currently `v0.80`) bounds
the candidate pool. A vendor BYOK header cannot widen it either — the registry
gates the pool before provider eligibility is consulted.

Recommended settings:

| Variable | Value | Why |
|---|---|---|
| `AIAND_API_KEY` | your key | the only provider key needed |
| `ROUTER_DEFAULT_BASELINE_MODEL` | `zai-org/glm-5.3` | savings math compares against the roster's frontier anchor instead of an Anthropic price the deploy cannot serve |
| `ROUTER_HARD_PIN_MODEL` | `deepseek-ai/deepseek-v4-flash` | compaction/explore utility turns stay on AIand's fast lane (the roster's cheap model) |
| `ROUTER_EXCLUDED_PROVIDERS` | comma list of provider names to exclude | optional deployment-wide hard pin that also blocks per-request vendor BYOK widening; with AIand the only registered provider there is nothing else to exclude |

### Removed in the AIand-only build

Every gateway and federated-credential flow was cut with the AIand-only split,
and the walkthroughs that documented them were removed with it. They cannot
succeed now: no registered provider declares a base-URL override, so
`auth.UpsertExternalAPIKey` rejects `auth_type` `keypair_jwt`,
`wif`, and `azure_entra` with a `400` ("no registered provider accepts …").

- **Anthropic- and OpenAI-compatible gateways** (`anthropic_gateway`,
  `openai_gateway`, and the `ANTHROPIC_GATEWAY_*` / `OPENAI_GATEWAY_*` env
  vars) — no gateway provider is registered, so no client dispatches to one.
- **Snowflake Cortex native web search** (`ROUTER_CORTEX_WEB_SEARCH`,
  `SNOWFLAKE_AGENT_ROLE`, `SNOWFLAKE_AGENT_HOST_SUFFIX`,
  `SNOWFLAKE_AGENT_TIMEOUT_MS`) — the executor only ran for a gateway key, and
  these variables are no longer read.
- **Key-pair auth** (`auth_type: "keypair_jwt"`) — rejected at write time.
- **Workload identity federation** (`auth_type: "wif"`) — rejected at write
  time. `ROUTER_WIF_PROVIDER`, `ROUTER_WIF_AUDIENCE`, and
  `ROUTER_WIF_OIDC_TOKEN_FILE` are still parsed at boot, but no key can carry a
  `wif` credential.
- **Microsoft Entra client credentials** (`auth_type: "azure_entra"`) —
  rejected at write time.

`bearer` — the default when `auth_type` is omitted — is the only usable auth
type.

In `selfhosted` mode BYOK is always active (it's the only credentialing path).
In `managed` mode it is opt-in per installation: the control plane sets
`byok_enabled` on the installation row, and until it does, the auth middleware
strips BYOK keys so a stored key can't spend against a deployment that bills
prepaid credits. Once enabled, a BYOK turn debits no inference cost (the
customer paid their own provider). A platform fee can be charged on top,
recorded as a separate `byok_fee` ledger row: set `BYOK_FEE_RATE` to a
fraction of upstream cost (e.g. `0.05` for 5%). The default is `0` — no
fee, and no `byok_fee` row is written.

## Postgres

Set `DATABASE_URL` directly, or compose it from the individual vars:

| Variable                   | Default                           | Purpose |
| -------------------------- | --------------------------------- | ------- |
| `DATABASE_URL`             | *(none)*                          | Full connection string (takes precedence). |
| `POSTGRES_USER`            | *(required if no `DATABASE_URL`)* | Username. |
| `POSTGRES_PASSWORD`        | *(required if no `DATABASE_URL`)* | Password. |
| `POSTGRES_DB`              | *(required if no `DATABASE_URL`)* | Database name. |
| `POSTGRES_HOST`            | *(required if no `DATABASE_URL`)* | Hostname. |
| `POSTGRES_PORT`            | `5432`                            | Port. |
| `POSTGRES_SSLMODE`         | `require`                         | TLS mode. Use `disable` for local Docker. |
| `POSTGRES_CONNECTION_NAME` | *(none)*                          | Cloud SQL Auth Proxy instance connection name. |

## Server

| Variable                 | Default      | Purpose |
| ------------------------ | ------------ | ------- |
| `PORT`                   | `8080`       | HTTP listen port. |
| `ROUTER_DEPLOYMENT_MODE` | `selfhosted` | `selfhosted` mounts `/ui/*` and `/admin/v1/*`. `managed` skips both (for SaaS deployments with a separate admin UI). |
| `ROUTER_ADMIN_PASSWORD`  | *(none)*     | Dashboard password. When unset, inference stays available but dashboard login and management endpoints return `admin_login_disabled`. |
| `ROUTER_RESTRICT_UPSTREAM_EGRESS` | follows `ROUTER_DEPLOYMENT_MODE` | When true, provider adapters refuse to dial an upstream that resolves outside the public internet (loopback, private, link-local, CGNAT). Defaults to true in `managed` mode and false in `selfhosted`, where pointing a provider at an in-cluster or loopback gateway is normal. While on, provider adapters also ignore `HTTP_PROXY`/`HTTPS_PROXY`, since a proxied connection makes the destination unverifiable. |
| `ROUTER_MODEL_DISCOVERY_PRIVATE_ORIGINS` | *(none)* | Comma-separated exact origins allowed to use private addresses for model discovery (for example, `https://gateway.internal:8443`). Discovery always blocks non-public destinations otherwise and always ignores ambient proxies, regardless of `ROUTER_RESTRICT_UPSTREAM_EGRESS`. Entries may contain only scheme, host, and port; paths, wildcards, and CIDRs are rejected at startup. |

## Managed serving (`ROUTER_SERVING_*`)

These variables apply only to Weave's managed deployment, where a `router-gateway`
process admits a session and forwards it to a managed `router` worker revision that
is pinned to immutable registry artifacts. Self-hosted deployments leave them unset.
The control-plane semantics — candidates, selection sets, proposals, target state
and the activation flow — are documented in
[`SERVING_CONTROL.md`](SERVING_CONTROL.md); this table only records which binary
reads each variable, where its value comes from, and what happens when it is absent.

"Deploy script" means the WorkWeave release step
(`.github/scripts/router-prepare-revisions.sh`) that stamps a new worker revision
with the artifact references it must serve; "Terraform" means the service-level
environment in `terraform/modules/environment/router_managed_serving.tf`, which is
kept out of revision-specific stamping.

| Variable | Read by | Source | Absent or invalid |
| --- | --- | --- | --- |
| `ROUTER_SERVING_ASSERTION_KEY` | Gateway and worker | Terraform (Secret Manager reference) | Gateway: boot fails. Worker: unset or whitespace-only keeps the worker on its existing managed/self-hosted path, except in `managed` mode with any other `ROUTER_SERVING_*` variable set, where boot fails; a key shorter than 32 bytes fails boot rather than serving unsigned traffic. |
| `ROUTER_SERVING_ENVIRONMENT` | Gateway | Terraform | Boot fails. Must be `prod` or `staging`. |
| `ROUTER_SERVING_REGISTRY_URI` | Gateway and worker | Terraform (both services) and deploy script (worker) | Gateway: boot fails. Worker: falls back to `WEAVE_REGISTRY_URI`, then `gs://weave_ml/weave_registry`; set it explicitly. |
| `ROUTER_SERVING_TARGET` | Worker | Deploy script and Terraform | Boot fails. Must name a known target (`staging`, `prod/stable`, `prod/weave-internal`). |
| `ROUTER_SERVING_PROJECT` | Worker | Deploy script and Terraform | Boot fails. |
| `ROUTER_SERVING_REGION` | Worker | Deploy script and Terraform | Boot fails. |
| `ROUTER_SERVING_IMAGE_DIGEST` | Worker | Deploy script | Boot fails. Must be an exact image digest. |
| `ROUTER_SERVING_REVISION` | Worker | Deploy script | Falls back to Cloud Run's `K_REVISION`; boot fails when both are empty. |
| `ROUTER_SERVING_CONFIGURATION_URI` | Worker | Deploy script | Boot fails. Must be a credential-free `gs://` object inside the registry root. |
| `ROUTER_SERVING_CONFIGURATION_SHA256` | Worker | Deploy script | Boot fails. |
| `ROUTER_SERVING_CONFIGURATION_GENERATION` | Worker | Deploy script | Missing or non-numeric reads as `0`, which fails the positive-generation check at boot. |
| `ROUTER_SERVING_SELECTION_SET_URI` | Worker | Deploy script | Boot fails. |
| `ROUTER_SERVING_SELECTION_SET_SHA256` | Worker | Deploy script | Boot fails. |
| `ROUTER_SERVING_SELECTION_SET_GENERATION` | Worker | Deploy script | Same as the configuration generation. |

The three `ROUTER_SERVING_CONFIGURATION_*` and three
`ROUTER_SERVING_SELECTION_SET_*` variables are each read as one `ObjectRef`
(`{uri, sha256, generation}`), so a partially stamped triple is rejected rather
than resolved loosely.

With a nonempty `ROUTER_SERVING_ASSERTION_KEY`, managed worker boot is fail-closed:
the worker validates its attested identity (target, project, region, revision,
image digest, configuration reference) before mounting inference endpoints, and
a failure stops boot rather than degrading to an unattested path. If the worker
key is unset or whitespace-only, it skips managed-serving preparation and mounts
inference endpoints without serving-admission checks; in
`ROUTER_DEPLOYMENT_MODE=managed` that is allowed only on a revision with no other
`ROUTER_SERVING_*` variable, so a serving-stamped worker whose key injection was
dropped refuses to boot instead. The gateway validates its environment and signing
key before it opens the registry, and `/readyz` stays fail-closed afterwards.
Keep the signing key identical on gateway and workers of the same environment.

## Routing

| Variable                          | Default                      | Purpose |
| --------------------------------- | ---------------------------- | ------- |
| `ROUTER_DEFAULT_STRATEGY`         | `cluster`                    | Strategy used when an installation has no persisted strategy. Change only after the policy rollout gate passes. The router refuses to boot, and `/readyz` fails, when this strategy has no router configured (e.g. `hmm_embedding` without `ROUTER_POLICY_ENVIRONMENT`). Managed serving workers (`ROUTER_DEPLOYMENT_MODE=managed` with `ROUTER_SERVING_ASSERTION_KEY`) additionally refuse to boot unless it is set to one of the policy strategies they register (`hmm`, `hmm_embedding`); the legacy `router` service, self-hosted deployments and the gateway keep the `cluster` default. |
| `ROUTER_CLUSTER_VERSION`          | *(reads `artifacts/latest`)* | Pin a specific cluster artifact version (e.g. `v0.79`). |
| `ROUTER_CLUSTER_EMBED_TIMEOUT_MS` | `200`                        | Per-request ONNX embed timeout. Increase for slower hosts. |
| `ROUTER_CLUSTER_COST_CAP_MULTIPLE` | `3.0`                      | Deployment cost cap: argmax only considers models whose effective cost (`input_per_1k + output_cost_ratio * output_per_1k * verbosity_factor`) is within this multiple of the cheapest deployed lane. Set to `0` to disable and restore the pure quality/cost blend. Measured over the full labelled corpus (`.context/label-runs/v082-expanded-labels.jsonl`, 1,503 all-8-scored prompts): cheapest-lane-only 67.24% accuracy @ $0.00034/prompt, `3.0` (default) 71.03% @ $0.00058/prompt, cap disabled 74.36% @ $0.00512/prompt. A cap that would empty the candidate set falls back to the full set, so a turn is never failed over pricing. |
| `ROUTER_EMBED_ONLY_USER_MESSAGE`  | `true`                       | Feed only user-role text to the embedder. Set `false` to embed the full concatenated turn. |
| `ROUTER_STICKY_DECISION_TTL_MS`   | `0` (disabled)               | Reuse a routing decision per API key for this many ms. |
| `ROUTER_SESSION_PIN_ENABLED`      | `true`                       | Pin a session to its first-routed model so multi-turn conversations stay coherent. |
| `ROUTER_EXPLORE_ENABLED`          | `false`                      | Quality-tie-band exploration: instead of always serving the scorer's argmax, sample uniformly among the servable models whose blend score is within `ROUTER_EXPLORE_EPSILON` of it, logging the sampling probability (`Propensity`) and chosen score for off-policy estimation. Non-prod / shadow expected — never flip fleet-wide without a bake-off; ignored unless the router is the cluster scorer. |
| `ROUTER_EXPLORE_EPSILON`          | `0.05`                       | Band half-width for `ROUTER_EXPLORE_ENABLED`. Models clearly below the band are never gambled on, so a wider band only trades expected accuracy for more exploration coverage — for the measured trade-off at 0.00 / 0.05 / 0.10 run `scripts/measure_tie_band.py` (re-run it after any cluster-artifact rebuild; it pins its table to artifact sha256 fingerprints). |
| `ROUTER_HARD_PIN_MODEL`           | *(none)*                     | Force every request to a specific model, bypassing the cluster scorer. Debugging only. |
| `ROUTER_HARD_PIN_PROVIDER`        | *(none)*                     | Pair with `ROUTER_HARD_PIN_MODEL`. |
| `ROUTER_HARD_PIN_EXPLORE`         | `true`                       | Pin Claude Code Task-tool sub-agent turns to `ROUTER_HARD_PIN_MODEL`/`ROUTER_HARD_PIN_PROVIDER` (or the cheapest deployed model, if those are unset). Set `false` to route sub-agents through the scorer like any other turn. Ignored under the HMM strategy, whose classifier selects sub-agent turns like any other turn. |
| `ROUTER_SUBAGENT_MODEL`           | *(none)*                     | Route Claude Code Task-tool sub-agent turns to a distinct model, independent of `ROUTER_HARD_PIN_MODEL` — e.g. a local/self-hosted OpenAI-compatible model (point `AIAND_BASE_URL` at your server) while the main loop keeps using Anthropic/whatever the scorer picks. Requires `ROUTER_SUBAGENT_PROVIDER`; either alone is ignored. Takes effect regardless of `ROUTER_HARD_PIN_EXPLORE` and under every strategy, including HMM. |
| `ROUTER_SUBAGENT_PROVIDER`        | *(none)*                     | Pair with `ROUTER_SUBAGENT_MODEL`. |
| `ROUTER_TRANSLATION_COMPATIBILITY_MODE` | `shadow` | Translation representability rollout: `off` disables broad filtering, `shadow` records candidate exclusions without changing routes, and `enforce` makes declared semantic requirements hard routing constraints. Native search remains enforced in every mode because another provider family cannot preserve its tool/result blocks; advertised-only search tools are scoped out separately. Other native-only safety paths (such as unsupported Responses tool unions and native Gemini ingress) remain protected unless mode is `off`. |
| `ROUTER_SCOPED_SEARCH_REQUIREMENT` | `true` | Scopes the citations/search native-capability requirement to sessions that actually used a web-search tool this turn or recently, instead of every turn that merely advertises one. Advertised-only turns return to normal policy routing. |
| `ROUTER_SEARCH_REQUIREMENT_DECAY_TURNS` | `3` | With `ROUTER_SCOPED_SEARCH_REQUIREMENT`, how many routed turns after the last actual search-tool use keep the requirement before it decays. |
| `ROUTER_HANDOVER_PROVIDER`        | `aiand`                      | Provider whose registered client runs handover and compaction summaries. Must have a catalog binding for `ROUTER_HANDOVER_MODEL` and `ROUTER_COMPACTION_MODEL`; otherwise boot fails. |
| `ROUTER_HANDOVER_MODEL`           | `zai-org/glm-5.3-flash`      | Model for switch-turn handover summaries. Must be one of the `handover_summary` policy's fixed catalog models in [`POLICY_INFERENCE.md`](POLICY_INFERENCE.md); any other value fails boot rather than substituting a default. |
| `ROUTER_COMPACTION_MODEL`         | `deepseek-ai/deepseek-v4-pro`| Fallback Anthropic family for compaction when no eligible session model is available. Selection upgrades to the newest eligible catalog version in that family, including session-derived choices. The configured model must bind on `ROUTER_HANDOVER_PROVIDER` and belong to the compaction policies' reviewed catalog set in [`POLICY_INFERENCE.md`](POLICY_INFERENCE.md); otherwise boot fails. Claude Code's client-side compaction still requires direct Anthropic; Codex checkpoint compaction can reuse other providers. Explicit `ROUTER_HARD_PIN_MODEL` overrides remain exact. |
| `ROUTER_COMPACTION_TIMEOUT_MS`    | `90000`                      | Hard timeout for one compaction-handover summary call (separate from `ROUTER_HANDOVER_TIMEOUT_MS`; a Sonnet-class summary of a near-full window is slow). On timeout the full history is kept. |
| `ROUTER_ESCALATION_JUDGE_ENABLED` | `false` | Enables the optional Weave-funded Switchyard LLM judge, which runs on the AIand binding for `zai-org/glm-5.3-flash`. |
| `ROUTER_ESCALATION_JUDGE_ACTIVE_ENABLED` | `false` | Allows `switchyard_llm_v1` to be selected as the active classifier. Keep false during shadow evaluation; requires `ROUTER_ESCALATION_JUDGE_ENABLED=true`. |
| `ROUTER_ONNX_ASSETS_DIR`          | `/opt/router/assets`         | Directory containing `model.onnx` + `tokenizer.json`. |
| `ROUTER_ONNX_LIBRARY_DIR`         | *(system default)*           | Path to `libonnxruntime` (e.g. `/opt/homebrew/lib` on Apple Silicon). |

If the cluster scorer can't run (missing model, embed timeout, etc.), the
router returns HTTP 503 — it does *not* silently fall back to a default
model. Failures are loud by design.

### Authoritative upgrade evidence policy

Authoritative HMM turns evaluate a typed evidence policy for expensive fresh
models. The deployment default is `score` (today's 0.85 confidence floor).
Set `ROUTER_AUTHORITATIVE_UPGRADE_POLICY=evidence` (or the installation override
`authoritative_upgrade_policy`) to serve that policy. `off` serves the fresh
HMM pick without the 0.85 floor.

| Variable | Default | Purpose |
| --- | --- | --- |
| `ROUTER_AUTHORITATIVE_UPGRADE_POLICY` | `score` | `score`, `evidence`, or `off`. Installation override wins. |
| `ROUTER_AUTHORITATIVE_UPGRADE_HOLDOUT_PCT` | `0` | Session-sticky share that stays on `score` while evidence is on. 0-100. |
| `ROUTER_AUTHORITATIVE_UPGRADE_VOTES` | `3` | Consecutive same-group expensive votes required before evidence switches. `0` disables vote hysteresis. |
| `ROUTER_AUTHORITATIVE_UPGRADE_SHADOW_MARGIN` | unset | Top-1 minus top-2 classifier margin threshold in `[0,1]`. Unset falls back to the score gate for margin-needed cases. |
| `ROUTER_AUTHORITATIVE_UPGRADE_SHADOW_STALE_AFTER` | unset | Pin-age cutoff as a Go duration. Unset or `0s` disables the stale-pin rule. |
| `ROUTER_AUTHORITATIVE_UPGRADE_GATE` | `true` | Existing 0.85 floor kill switch, used when policy is `score` or evidence falls back to existing policy. |

Invalid policy, holdout, margin, or duration values fail startup. Force/hard
pins, utility turns, deadline fallbacks, and active escalation floors still win.
A content-free `authoritative upgrade evidence` log records mode, applied,
holdout, outcome, reason, margin, votes, and served model.

## Provider and model exclusions

Exclusions keep traffic away from a provider or model — the control to reach
for when an installation must stay off a particular provider or model.

| Variable                     | Default  | Purpose |
| ---------------------------- | -------- | ------- |
| `ROUTER_EXCLUDED_PROVIDERS`  | *(none)* | Comma-separated provider names no request may be routed to. Pins the list deployment-wide: per-installation edits are refused (403) while it is set. |
| `ROUTER_EXCLUDED_MODELS`     | *(none)* | Comma-separated model IDs no request may be routed to, same deployment-wide pinning. |

Without either env var the lists come from the installation, editable in the
dashboard or through `PUT /admin/v1/excluded-providers` and
`PUT /admin/v1/excluded-models`.

From a terminal, `npx @weave-os/router models --claude` lists every deployed
model with its on/off state and `models enable` / `models disable` edit it,
reading the endpoint and key from the Claude Code install already on disk.
Claude Code gets the same thing as `/router-models` (alias `/models`). While
either env var is set the CLI surfaces the 403 verbatim rather than pretending
the edit landed. See [install/README.md](../install/README.md#choosing-which-models-the-router-may-pick).

Exclusions are authoritative, not a preference. An excluded provider is
subtracted from the request's eligible set before anything routes, so the
scorer, the turn-type hard pins, session pins, and cross-binding failover all
stay off it — including when the caller holds their own BYOK key for it.

That extends to explicit forcing. `/force-model` and the `x-weave-force-model`
header are refused when every provider that could serve the model is excluded:
the command answers with the reason and leaves routing (and any prior pin)
alone, and the header fails the request with HTTP 400. A model with one
permitted binding left is forced normally and served through that binding. A
live session whose forced pin is later excluded fails the same way rather than
quietly reverting to automatic routing — clear it with `/unforce-model`. The
same holds a level up: exclusions that empty a forced routing cluster fail the
request too (see [Forcing a model or a routing
cluster](#forcing-a-model-or-a-routing-cluster)).

Excluding every provider that serves the models you route to leaves requests
with nowhere to go (HTTP 503 from the scorer), so exclude deliberately.

## Forcing a model or a routing cluster

`/force-model <model>` (aliases `/model` and `/fm`) pins the client session to one model. The
pin applies to parent and child agent threads that share the same client-session
identity, regardless of their first prompt or active routing strategy. Clients
that send no session identity can only be pinned at the current thread scope.
Codex handles its own `/model` locally and never sends the command itself; on
an opted-in install (`X-Weave-Codex-Native-Model-Pin: 1`) the router instead
keys off the `<model_switch>` developer fragment Codex records when the user
switches models mid-session, and pins the request's model from then on. The
model a session launched with is a baseline and routes automatically. The
installed `$force-model` / `$fm` skill remains the persistent-pin path.
An explicit force-model choice also takes precedence over an installation's
passthrough policy, including users without an assignment in assigned mode.
Those policies disable automatic model selection, not the caller's explicit
choice. Without a force-model choice, they preserve the request's model.
Installation model/provider restrictions still apply to the forced model.
The name is matched **exactly** — it must be a canonical catalog ID
(`qwen/qwen3.8-max`), that model's bare name without the vendor prefix
(`qwen3.8-max`), or an alias (`opus`, `qwen-max`), optionally with a `:level`
effort suffix (`opus:high`). There is no prefix, substring, or nearest-match
fallback: a name the router doesn't recognize is refused, never approximated.

That strictness is the point. Approximate matching served a model the caller
never named — `/fm qwen 3.8` resolved through the bare `qwen` alias to
`qwen/qwen3-coder` and acked as if the pin took. The whole rest of the command
line is now read as the model name, so that input is rejected instead. To pin
and prompt in one turn, put the prompt on the **next line**:

```
/force-model qwen/qwen3.8-max
now fix the failing test
```

Two request headers let a headless caller (eval harness, CI, any client whose
UI eats slash commands) override routing. Both fail the request rather than
routing on, so a typo can't look like it took effect.

| Header | Effect |
| ------ | ------ |
| `x-weave-force-model` | Pins the session to one model, exactly as `/force-model` does — same exact-match rule. Accepts a canonical catalog ID, a bare name, or an alias (`opus`, `gpt`, `qwen-max`, …) plus an optional `:level` effort suffix (`opus:high`). A value naming no catalog model is HTTP 400. |
| `x-weave-force-cluster` | Constrains serving to one of the policy sidecar's routing clusters, leaving the choice *within* it to the policy. |

`x-weave-force-cluster` takes an opaque label — the router holds no list of
valid ones. The live cluster vocabulary belongs to the deployed policy artifact
and changes when it does, so the only authority is the roster the sidecar
reports on that very request; a hardcoded list would silently go stale on the
next roster bump. Consequences:

- A label absent from the live roster is HTTP 400, whether it's a typo or a
  cluster the current artifact retired. Both are equally unservable.
- A label that *is* in the roster but has no eligible model for this request
  (everything in it excluded, over-window, or filtered out on capability) is
  also HTTP 400 — including when a per-key cluster model list empties it.
- The header only works on the `hmm` / `hmm_embedding` strategies. The default
  `cluster` strategy scores anonymous centroids with no named groups, so there
  is nothing to constrain to and the request is HTTP 400 rather than a silent
  no-op.
- A sidecar too old to report its clusters also 400s. The constraint can't be
  proven against a roster the router can't see, and serving anyway would ignore
  the force.

Unlike `x-weave-force-model` the cluster header writes no session pin: every
turn carrying it is constrained on its own merits. Which models make up a
cluster stays control-plane config (the dashboard's per-API-key "Cluster model
lists" panel) — the header only says *which* cluster this turn must come from,
and any list configured for that cluster still orders the arm that serves.

## Policy sidecars

Out-of-process policy routers use the versioned contract in
[Policy router harness](POLICY_ROUTER_HARNESS.md). The router remains the
authority for candidate eligibility, provider binding, dispatch, retries,
privacy context, and telemetry. `ROUTER_HMM_ROSTER_PATH` and the rollback story
for Go-owned deterministic selection are documented in
[HMM deterministic selection in Go](HMM_GO_SELECTION.md).

| Variable                           | Default | Purpose |
| ---------------------------------- | ------- | ------- |
| `ROUTER_POLICY_SIDECARS`           | *(none)* | JSON object mapping a new strategy ID to its sidecar origin, for example `{"quality-v2":"https://quality-v2.internal"}`. IDs must match `[a-z][a-z0-9_-]{0,63}`. At most 16 may be configured. `cluster`, `rl`, `hmm`, and `bandit` are reserved. |
| `ROUTER_POLICY_SIDECAR_AUTH`       | *(none)* | JSON object mapping configured generic strategy IDs to `none` or `google-id-token`, for example `{"quality-v2":"google-id-token"}`. Google ID-token mode uses the exact sidecar origin as token audience and fails router startup when application default credentials cannot build the client. |
| `ROUTER_POLICY_SIDECAR_TIMEOUT_MS` | `3000`  | Total timeout for each generic policy decision, including transient retries. Also bounds startup capability discovery. |
| `ROUTER_HMM_SIDECAR_URL`           | *(none)* | Legacy built-in HMM registration. Prefer the generic map for new strategies. |
| `ROUTER_HMM_SIDECAR_TIMEOUT_MS`    | `3000`  | Total HMM decision timeout. |
| `ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS` | 60% of the decision timeout | Bounds a single HMM attempt so one stalled sidecar instance cannot spend the whole decision budget before the retries run. Set it equal to `ROUTER_HMM_SIDECAR_TIMEOUT_MS`, or to `0`, to let one attempt use the full budget. |
| `ROUTER_HMM_SIDECAR_AUTH`          | `none`  | Authentication for the HMM sidecar. Use `google-id-token` for managed Cloud Run; the exact sidecar origin is used as the token audience. |
| `ROUTER_HMM_ROSTER_PATH`           | *(none; required with `ROUTER_HMM_SIDECAR_URL`)* | Path to a generated declarative roster JSON (`hmm_router_cluster_roster_v6`). The roster is loaded and validated against the model catalog at startup (boot fails on any invalid arm) and drives the router's authoritative deterministic within-cluster arm selection: the sidecar's classifier label/confidence is kept, its arm is not. Explicit force-cluster and per-key cluster overrides still take precedence when they actually constrain the pick; selection fails open to the sidecar's pick when no ranked group holds an eligible arm. Pin-sticky eligibility is neutralized on any Go pick so a session pin cannot veto it. Leaving it unset while an HMM sidecar is configured fails boot. |
| `ROUTER_HMM_ROSTER_PINNABLE_PATHS` | *(none)* | Comma-separated paths of additional roster JSON files loaded and validated at boot alongside `ROUTER_HMM_ROSTER_PATH`. Every loaded roster is indexed by the sha256 of its file bytes so an `x-weave-policy-pin` may select it; nothing is read from disk on a live request. Only meaningful with `ROUTER_POLICY_PIN_ENABLED=true`. |
| `ROUTER_POLICY_PIN_ENABLED`        | `false` | Registers the `x-weave-policy-pin: <policy_artifact_sha256>@<roster_sha256>` header on `/v1/messages`, `/v1/chat/completions`, `/v1/route` and `/v1/route/preview`. Off (default) the middleware is not registered, so the header has no effect and the telemetry columns `policy_pin_requested` / `policy_pin_honoured` stay NULL. On, the header value is only parsed for installations with `policy_header_overrides_enabled`: for every other installation the value (valid or not) is never inspected, the response is identical to a request without the header, and telemetry records `requested=true, honoured=false`. For an authorized installation a malformed value is HTTP 400 (`policy_pin_malformed`); a well-formed pin is honoured, which means the turn is always scored fresh by the HMM sidecar with exactly that artifact (`HMM_PACKAGE_REGISTRY`) and the arm is selected from exactly that roster — session-sticky pins, `/force-model`, usage-bypass, blind-experiment passthrough and planner stays are not consulted (utility hard pins such as probes/title-gen keep their fixed path). If the artifact or roster is not loaded, or any path would serve a model the pinned policy did not pick, the turn fails HTTP 503 with typed reason `policy_pin_unavailable` and the telemetry row is written with `policy_pin_honoured=false` — an authorized pin never yields a 2xx with `policy_pin_honoured=false`, and the router never falls through to the current artifact or roster. |
| `ROUTER_HMM_BETA_SIDECAR_URL`      | *(none)* | Second HMM sidecar serving the candidate package that sessions opt into with `/beta`. Unset leaves `/beta` answering "unavailable" and never touches stable routing. `ROUTER_HMM_BETA_SIDECAR_AUTH`, `ROUTER_HMM_BETA_SIDECAR_TIMEOUT_MS`, and `ROUTER_HMM_BETA_SIDECAR_ATTEMPT_TIMEOUT_MS` mirror the stable variables. |
| `ROUTER_HMM_BETA_ROSTER_PATH`      | *(none; required with `ROUTER_HMM_BETA_SIDECAR_URL`)* | Declarative roster the beta strategy's Go-side selection reads. It must be the roster embedded in the pinned beta package, which may carry a different cluster taxonomy from stable's. Unlike the stable pair, a missing or invalid beta roster disables beta with an error log instead of failing boot, so a broken candidate cannot take stable routing down. |
| `ROUTER_RL_SIDECAR_URL`            | *(none)* | Legacy built-in RL registration. Prefer the generic map for new strategies. |
| `ROUTER_RL_SIDECAR_TIMEOUT_MS`     | `3000`  | Total RL decision timeout. |
| `ROUTER_RL_SIDECAR_MODAL_KEY`      | *(none)* | Optional Modal proxy token id (`Modal-Key`) when the RL sidecar is a Modal ASGI app with `requires_proxy_auth`. |
| `ROUTER_RL_SIDECAR_MODAL_SECRET`   | *(none)* | Optional Modal proxy token secret (`Modal-Secret`); required when `ROUTER_RL_SIDECAR_MODAL_KEY` is set. |

`GET /capabilities` is queried at router startup. A failed probe does not
silently remove the strategy: serving stays registered and fails closed if
`POST /route` is unavailable, while optional outcome and feedback callbacks
remain disabled until the next successful restart. This keeps persisted
rollout state visible without pretending that a different strategy served.

Policy route requests retry network failures and HTTP 500, 502, 503, and 504
up to three attempts within the configured total timeout. Other failures are
not retried. An unavailable or invalid policy decision returns HTTP 503; it
never falls back to cluster or another policy.

### Self-hosted frozen HMM sidecar

The repository includes an optional companion container under
`sidecars/hmm/`. Start it with `make up-hmm`; the normal `make up` and
`make full-setup` paths remain cluster-only. HMM is not selected unless an
operator explicitly chooses the `hmm` strategy.

| Variable | Default | Purpose |
| --- | --- | --- |
| `HMM_PACKAGE_URL` | Published `hmm-model-v1` GitHub Release asset | HTTPS URL for the portable frozen package. |
| `HMM_PACKAGE_PATH` | *(none)* | Local package path when running the sidecar outside Compose. Set exactly one of path or URL. |
| `HMM_PACKAGE_SHA256` | Pinned release digest in the sidecar image | Required digest for URL downloads; optional but recommended with a local path. |
| `HMM_ARTIFACT_CACHE_DIR` | `/tmp/workweave-hmm-artifacts` | Atomic download/extraction cache. |
| `HMM_EMBEDDING_PROVIDER` | `google` | `google` or `openai-compatible`. |
| `GOOGLE_API_KEY` | *(none)* | Google Gemini API key for the exact embedding model named by the artifact. |
| `HMM_EMBEDDING_BASE_URL` | *(none)* | Base URL for an OpenAI-compatible `/embeddings` endpoint. |
| `HMM_EMBEDDING_API_KEY` | *(none)* | Optional bearer token for that endpoint. |
| `HMM_EMBEDDING_MODEL` | Artifact model ID | Model sent to an OpenAI-compatible endpoint. |

The published v1 package is tied to `google/gemini-embedding-2` at 3,072
dimensions. Those embedding values are direct classifier features and define
the HMM emission space, so another 3,072-dimensional model is not a substitute.
At startup the sidecar embeds a fixed probe and compares it to the reference
vector stored in the artifact. Readiness fails closed when the endpoint serves
an incompatible vector space. A fully local embedder is supported only with a
separately trained package that declares and probes that embedder.

The self-hosted sidecar is frozen: it keeps only a bounded in-memory embedding
cache, advertises no learning/outcome/feedback callbacks, and never persists
request or response content.

Selection precedence is:

1. An authorized internal `x-weave-router-strategy` request override.
2. The installation's persisted strategy.
3. `ROUTER_DEFAULT_STRATEGY`.

The request header is ignored unless the installation explicitly enables
policy-header overrides. `x-weave-router-debug` follows the same authorization
rule and cannot enable training. Shadow decisions are always non-dispatching,
non-debug, and non-learning.

## BYOK encryption

| Variable                      | Default   | Purpose |
| ----------------------------- | --------- | ------- |
| `EXTERNAL_KEY_ENCRYPTION_KEY` | *(unset)* | Tink AES-256-GCM keyset (JSON) that encrypts customer-supplied upstream provider keys at rest. |

**If unset, BYOK secrets are stored unencrypted** and the router logs a
`WARN` at startup. Set this in any deployment that handles real customer
secrets. Generate with:

```bash
tinkey create-keyset --key-template AES256_GCM --out-format json
```

A *malformed* keyset still fails closed (the router refuses to boot); only a
genuinely absent value triggers the unencrypted bypass.

## Telemetry (OpenTelemetry)

The router exports per-request trace spans to any OTLP-compatible collector.
Each proxied request emits two spans (`router.decision` and `router.upstream`)
with routing decisions, token usage, cost estimates, and latency. Export is
async/non-blocking; when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, OTel is
fully disabled at zero runtime cost. Everything the router records leaves the
process over OTLP only — there is no hardcoded analytics endpoint.

### High-fidelity content capture (`router.call` log records)

When `WV_CAPTURE_CONTENT` is set, the router additionally emits a `router.call`
OTLP **log record** per upstream call to `${OTEL_EXPORTER_OTLP_ENDPOINT}/v1/logs`.
Each record carries the same routing/decision metadata as the spans plus the
call outcome, and — depending on the mode — the request/response bodies. This
is the ML-ready event stream (one record per LLM call, full inputs and
outputs). It is **opt-in**: with `WV_CAPTURE_CONTENT` unset (the default) no
log records are emitted and behavior is unchanged.

| Variable             | Default | Purpose |
| -------------------- | ------- | ------- |
| `WV_CAPTURE_CONTENT` | `off`   | `off` = no log records; `hashed` = metadata + SHA-256 content hashes (no raw text); `full` = metadata + raw request/response bodies. |
| `WV_CAPTURE_MAX_BYTES` | `1048576` | Max buffered response bytes; larger responses are dropped and flagged `io.truncated=true` (the client still receives the full stream). |

Captured bodies are in the client's native wire format (Anthropic / OpenAI /
Gemini, matching the inbound surface). The `router.deployment_mode` resource
attribute (`selfhosted` / `managed`) is stamped on every export so a collector
can branch redaction or content-opt-out by deployment.

`WV_CAPTURE_CONTENT` is the deployment-wide **ceiling**. An installation can
tighten it below that (`GET`/`PUT /admin/v1/content-capture`, body
`{"mode": "off" | "hashed" | "full"}`; `{"mode": null}` clears the override),
and the effective mode for a request is the stricter of the two — so a tenant
on a `full` deployment can opt down to `hashed` or `off`, but an installation
asking for `full` under a `hashed` deployment still gets `hashed`.


| Variable                         | Default      | Purpose |
| -------------------------------- | ------------ | ------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`    | *(disabled)* | Collector base URL (e.g. `https://api.honeycomb.io`). Required to enable. |
| `OTEL_EXPORTER_OTLP_HEADERS`     | *(none)*     | Comma-separated `key=value` headers (e.g. auth tokens). |
| `OTEL_EXPORTER_OTLP_TIMEOUT`     | `10000`      | Per-export HTTP timeout in ms. |
| `OTEL_SERVICE_NAME`              | `router`     | `service.name` resource attribute. |
| `OTEL_RESOURCE_ATTRIBUTES`       | *(none)*     | Comma-separated `key=value` resource attributes. |
| `OTEL_BSP_MAX_QUEUE_SIZE`        | `1000`       | Span queue capacity. Spans drop when full. |
| `OTEL_BSP_MAX_EXPORT_BATCH_SIZE` | `50`         | Max spans per OTLP POST. |
| `OTEL_BSP_SCHEDULE_DELAY`        | `500`        | Partial-batch flush interval in ms. |
| `OTEL_EXPORT_WORKERS`            | `2`          | Export-goroutine count (spans and logs each get this many workers). |

The first five follow the [OTel SDK env spec](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/);
`OTEL_BSP_*` follows the [Batch Span Processor spec](https://opentelemetry.io/docs/specs/otel/trace/sdk/#batch-span-processor).
`OTEL_EXPORT_WORKERS` is a router-specific extension.

## Cluster-routing artifacts

Each embedder the cluster scorer can use needs two files at runtime —
`model.onnx` (INT8-quantized) and `tokenizer.json` — in its own subdirectory
of the assets root, keyed by embedder ID:

- `jina-v2-base-code-int8/` — from the public
  [`jinaai/jina-embeddings-v2-base-code`](https://huggingface.co/jinaai/jina-embeddings-v2-base-code)
  HuggingFace repo (Jina's own INT8 export; we don't maintain our own
  quantization). The default for every retained bundle; the flat legacy
  layout (`<root>/model.onnx`) still resolves for this embedder.
- `qwen3-embedding-0.6b-int8/` — produced by `scripts/export_qwen3_onnx.py`
  (Qwen3-Embedding-0.6B with last-token pooling baked into the graph) and
  uploaded to the public
  [`weave-eng/qwen3-embedding-0.6b-onnx-router`](https://huggingface.co/weave-eng/qwen3-embedding-0.6b-onnx-router)
  HF repo. Only needed when serving a bundle whose `metadata.yaml` declares
  this embedder; the runtime loads embedders lazily.

Neither is committed to git.

**Docker (default):** the Dockerfile downloads the files at image build time
into `/opt/router/assets/<embedder-id>/`. Both repos are public — no token
needed (the optional `hf_token` secret still works for rate-limit headroom);
set `HF_QWEN_REPO=` (empty) to skip the Qwen pull for Jina-only deploys.

**`make dev` (host-mode hot reload):** fetch the Jina files once into a local
directory and point `ROUTER_ONNX_ASSETS_DIR` at it:

```bash
mkdir -p assets/jina-v2-base-code-int8
BASE="https://huggingface.co/jinaai/jina-embeddings-v2-base-code/resolve/516f4baf13dec4ddddda8631e019b5737c8bc250"
curl -L "$BASE/onnx/model_quantized.onnx" -o assets/jina-v2-base-code-int8/model.onnx
curl -L "$BASE/tokenizer.json" -o assets/jina-v2-base-code-int8/tokenizer.json
echo "ROUTER_ONNX_ASSETS_DIR=$(pwd)/assets" >> .env.local
```

To also serve Qwen bundles locally, run `scripts/export_qwen3_onnx.py
--out-dir assets/qwen3-embedding-0.6b-int8` (or download the uploaded export
into that directory).

The pinned revisions (`HF_MODEL_REVISION`, `HF_QWEN_REVISION`) in the
Dockerfile keep local dev and the container build on the same weights. Bump
deliberately if you want a newer export.

The committed cluster artifacts (centroids, rankings, model registry,
metadata) live under `internal/router/cluster/artifacts/v<X.Y>/`. The
`artifacts/latest` pointer selects the default served version;
`ROUTER_CLUSTER_VERSION` overrides per-deployment.
