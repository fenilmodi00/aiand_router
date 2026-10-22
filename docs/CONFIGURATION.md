# Configuration reference

Deployment configuration uses environment variables ([12-factor](https://12factor.net/config));
organization settings are stored in Postgres.
This page is the exhaustive reference; the [README](../README.md) has the
60-second quickstart.

## Table of contents

- [Provider API keys](#provider-api-keys)
- [Postgres](#postgres)
- [Server](#server)
- [Managed serving (`ROUTER_SERVING_*`)](#managed-serving-router_serving_)
- [Routing](#routing)
- [Provider and model exclusions](#provider-and-model-exclusions)
- [Policy sidecars](#policy-sidecars)
- [Task-domain classifier](#task-domain-classifier)
- [BYOK encryption](#byok-encryption)
- [Telemetry (OpenTelemetry)](#telemetry-opentelemetry)
- [Cluster-routing artifacts](#cluster-routing-artifacts)

## Task-domain classifier

`ROUTER_TASK_DOMAIN_BINDINGS_PATH` optionally points to a digest-verified local
inventory of Qwen task classifier releases, HTTPS endpoints, bearer-secret env
names and scoring evidence files. Unset means no task runtime is loaded; managed
candidate admission, not this variable alone, enables task weighting. The worker
requires the task-profile migration and writable router Postgres. See the
[task-domain service guide](../sidecars/task-domain/README.md) for release format,
admission, inference limits, caching/failure semantics and staged-model validation.

## Provider API keys

The router dispatches to exactly one upstream, ai&'s OpenAI-compatible Chat
Completions surface, registered as the provider `aiand`. Anthropic, OpenAI, and
Gemini ingress requests are translated onto it. The provider is always
registered; `AIAND_API_KEY` is what makes it usable with deployment-level
credentials.

| Variable              | Default                                                   | Effect |
| --------------------- | --------------------------------------------------------- | ------ |
| `AIAND_API_KEY`   | *(none)*                                                  | Enables deployment-level use of the ai& provider through its OpenAI-compatible Chat Completions API. Without it the provider is registered for BYOK keys only. |
| `AIAND_BASE_URL`  | `https://api.aiand.com/v1`                                | Override for the ai& endpoint. |

**Native web search.** ai& implements function tools, not Anthropic's native
server tools, so Claude Code's WebSearch turn comes back as `tool type
'web_search_20250305' is not supported for this model`. The router has no
execution path for native web search: it detects the turn and strips the server
tools so the request can still be served, but the search itself is not run on
the tenant's behalf. Such turns stay on normal routing to the configured
upstream.

**BYOK (per-installation keys).** Instead of (or in addition to) the env vars
above, each installation can supply its own ai& key via the dashboard. Those
are stored in Postgres and used only for that installation's traffic. See
[BYOK encryption](#byok-encryption).

A BYOK key may carry its own endpoint, which overrides the deployment's base
URL for that installation's requests. Set it in **Settings → Provider API keys
→ Endpoint URL (optional)**, or through the admin API:

```bash
# /admin/v1 mutations take the dashboard cookie, not an rk_ bearer.
curl -sS -c jar -X POST https://<router>/admin/v1/auth/login \
  -H 'content-type: application/json' -d '{"password":"<admin password>"}'

curl -sS -b jar -X POST https://<router>/admin/v1/provider-keys \
  -H 'content-type: application/json' \
  -d '{"provider":"aiand","key":"<token>","base_url":"https://gateway.example.com/api"}'
```

The value must be an absolute `http(s)` URL; anything else is rejected with
`400`. A trailing slash is stripped, and the router appends its own API path
(`/chat/completions`), so give the base only. Omit the field to keep the
deployment endpoint.

A key may also carry a model alias map for endpoints that publish the catalog's
models under their own names:

```bash
curl -sS -b jar -X POST https://<router>/admin/v1/provider-keys \
  -H 'content-type: application/json' \
  -d '{"provider":"aiand","key":"<token>","base_url":"https://gateway.example.com/api",
       "model_aliases":{"z-ai/glm-5.2":"zai-org/glm-5.2"}}'
```

Keys are catalog model IDs (an ID outside the deployed catalog is rejected with
`400`) and values are what goes on the wire to that endpoint. Only the outbound
model name changes: routing, pricing, and analytics stay keyed on the catalog
ID. Omit the field to send catalog IDs unchanged.

The map is editable in **Settings → Provider API keys → Edit aliases**, or on
its own endpoint, which replaces the whole map and leaves the stored secret
alone — so retargeting model names doesn't need the credential re-entered:

```bash
curl -sS -b jar -X PUT https://<router>/admin/v1/provider-keys/<key id>/model-aliases \
  -H 'content-type: application/json' \
  -d '{"model_aliases":{"z-ai/glm-5.2":"zai-org/glm-5.2"}}'
```

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

These variables apply to managed workers that admit and serve requests directly.
The public hostname selects the stable fleet (Default, Boost and Max); a separate
internal hostname selects the isolated internal fleet. Self-hosted deployments
leave them unset.
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
| `ROUTER_SERVING_REGISTRY_URI` | Worker | Terraform and deploy script (worker) | Falls back to `WEAVE_REGISTRY_URI`, then `gs://weave_ml/weave_registry`; set it explicitly. |
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

A nonempty `ROUTER_SERVING_TARGET` enables managed admission. Boot validates the
worker identity and all immutable configuration references before mounting
inference. Partially stamped managed revisions fail closed. Admission decisions
use a 10,000-entry per-process LRU with a 30-second TTL; installation broadcasts
invalidate that installation and release broadcasts invalidate the whole cache.
Misses use the authoritative Postgres transaction and durable conversation lock.
Client serving headers are stripped. Worker validation additionally requires
`ROUTER_INTERNAL_SERVICE_TOKEN`; classifier validation retains Cloud Run IAM.
Inject `ROUTER_INTERNAL_SERVICE_TOKEN` into every worker fleet and the
`policyctl serving` runner so exact-revision worker validation can authenticate.
The `policyctl serving` runner and worker fleets must also share `PUBSUB_PROJECT_ID`
and `PUBSUB_TOPIC_ROUTER_POLICY_INVALIDATION` so activation invalidations reach
the admission caches using the same Pub/Sub project and topic.

## Routing

| Variable                          | Default                      | Purpose |
| --------------------------------- | ---------------------------- | ------- |
| `ROUTER_DEFAULT_STRATEGY`         | `cluster`                    | Strategy used when an installation has no persisted strategy. Change only after the policy rollout gate passes. The router refuses to boot, and `/readyz` fails, when this strategy has no router configured (e.g. `hmm_embedding` without `ROUTER_POLICY_ENVIRONMENT`). Managed serving workers (`ROUTER_DEPLOYMENT_MODE=managed` with `ROUTER_SERVING_TARGET`) additionally refuse to boot unless it is set to one of the policy strategies they register (`hmm`, `hmm_embedding`); the legacy `router` service and self-hosted deployments keep the `cluster` default. |
| `ROUTER_CLUSTER_VERSION`          | `latest`                     | Cluster artifact version to serve. `latest` is the sentinel that follows the `artifacts/latest` pointer; set a concrete version (e.g. `v0.27`) to pin one. |
| `ROUTER_CLUSTER_EMBED_TIMEOUT_MS` | `200`                        | Per-request ONNX embed timeout. Increase for slower hosts. |
| `ROUTER_EMBED_ONLY_USER_MESSAGE`  | `true`                       | Feed only user-role text to the embedder. Set `false` to embed the full concatenated turn. |
| `ROUTER_SESSION_PIN_ENABLED`      | `true`                       | Pin a session to its first-routed model so multi-turn conversations stay coherent. |
| `ROUTER_HARD_PIN_MODEL`           | *(none)*                     | Force every request to a specific model, bypassing the cluster scorer. Debugging only. Without this explicit override, titles and probes with auto/empty models are scored independently, while provider/quota probes with concrete models preserve their requested target; none anchors an automatic conversation pin. |
| `ROUTER_HARD_PIN_PROVIDER`        | *(none)*                     | Pair with `ROUTER_HARD_PIN_MODEL`. |
| `ROUTER_HARD_PIN_EXPLORE`         | `true`                       | Pin Claude Code Task-tool sub-agent turns to `ROUTER_HARD_PIN_MODEL`/`ROUTER_HARD_PIN_PROVIDER` (or the cheapest deployed model, if those are unset). Set `false` to route sub-agents through the scorer like any other turn. Ignored under the HMM strategy, whose classifier selects sub-agent turns like any other turn. |
| `ROUTER_SUBAGENT_MODEL`           | *(none)*                     | Route Claude Code Task-tool sub-agent turns to a distinct model, independent of `ROUTER_HARD_PIN_MODEL` — e.g. a BYOK catalog model while the main loop keeps using whatever the scorer picks. Requires `ROUTER_SUBAGENT_PROVIDER`; either alone is ignored. Takes effect regardless of `ROUTER_HARD_PIN_EXPLORE` and under every strategy, including HMM. |
| `ROUTER_SUBAGENT_PROVIDER`        | *(none)*                     | Pair with `ROUTER_SUBAGENT_MODEL`. |
| `ROUTER_TRANSLATION_COMPATIBILITY_MODE` | `shadow` | Translation representability rollout: `off` disables broad filtering, `shadow` records candidate exclusions without changing routes, and `enforce` makes declared semantic requirements hard routing constraints. Native search remains enforced in every mode because another provider family cannot preserve its tool/result blocks; advertised-only search tools are scoped out separately. Other native-only safety paths (such as unsupported Responses tool unions and native Gemini ingress) remain protected unless mode is `off`. |
| `ROUTER_SCOPED_SEARCH_REQUIREMENT` | `true` | Scopes the citations/search native-capability requirement to sessions that actually used a web-search tool this turn or recently, instead of every turn that merely advertises one. Advertised-only turns return to normal policy routing. |
| `ROUTER_SEARCH_REQUIREMENT_DECAY_TURNS` | `3` | With `ROUTER_SCOPED_SEARCH_REQUIREMENT`, how many routed turns after the last actual search-tool use keep the requirement before it decays. |
| `ROUTER_HANDOVER_PROVIDER`        | `aiand`                      | Provider whose registered client runs handover and compaction summaries. Must have a catalog binding for `ROUTER_HANDOVER_MODEL` and `ROUTER_COMPACTION_MODEL`; otherwise boot fails. |
| `ROUTER_HANDOVER_MODEL`           | `z-ai/glm-5.3-flash`         | Model for switch-turn handover summaries. Must be one of the `handover_summary` policy's fixed catalog models in [`POLICY_INFERENCE.md`](POLICY_INFERENCE.md); any other value fails boot rather than substituting a default. |
| `ROUTER_COMPACTION_MODEL`         | `z-ai/glm-5.3`               | Model used for compaction when no eligible session model is available. The configured model must bind on `ROUTER_HANDOVER_PROVIDER` and belong to the compaction policies' reviewed catalog set in [`POLICY_INFERENCE.md`](POLICY_INFERENCE.md); otherwise boot fails. Explicit `ROUTER_HARD_PIN_MODEL` overrides remain exact. |
| `ROUTER_COMPACTION_TIMEOUT_MS`    | `90000`                      | Hard timeout for one compaction-handover summary call (separate from `ROUTER_HANDOVER_TIMEOUT_MS`; a Sonnet-class summary of a near-full window is slow). On timeout the full history is kept. |
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

Exclusions keep traffic away from a provider or model. The router dispatches to
a single provider, `aiand`, so `ROUTER_EXCLUDED_PROVIDERS` is a switch for
"talk to no upstream at all" — useful to drain a deployment without taking it
down. Model exclusions are the everyday control: they take individual catalog
models out of rotation while the rest keep serving.

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

Exclusions are authoritative, not a preference. An excluded model is
subtracted from the request's eligible set before anything routes, so the
scorer, the turn-type hard pins, session pins, and the hand-over and
compaction model choices all stay off it. An excluded provider is subtracted
the same way — including when the caller holds their own BYOK key for it.

That extends to explicit forcing. `/force-model` and the `x-weave-force-model`
header are refused when the model is excluded: the command answers with the
reason and leaves routing (and any prior pin) alone, and the header fails the
request with HTTP 400. A live session whose forced pin is later excluded fails
the same way rather than quietly reverting to automatic routing — clear it
with `/unforce-model`. The same holds a level up: exclusions that empty a
forced routing cluster fail the request too (see [Forcing a model or a routing
cluster](#forcing-a-model-or-a-routing-cluster)).

Excluding every model a request could have picked leaves requests with nowhere
to go (HTTP 503 from the scorer), so exclude deliberately.

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

`/force-parent <model>[:effort]` (Codex: `$force-parent`) uses the same model
resolution and restrictions, but pins only the invoking thread. It replaces a
conversation-wide force, so children sharing its client-session ID resume normal
automatic routing. `/force-model` restores conversation-wide pinning, and
`/unforce-model` clears either mode. Parent scope follows the router's thread
digest, which includes the first user message: after compaction rewrites that
message, reapply `/force-parent` in the parent. An explicit deployment or request
header override still applies independently to children.

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
| `x-weave-force-model` | Pins the session to one model, exactly as `/force-model` does — same exact-match rule. Accepts a canonical catalog ID, a bare name, or an alias (`opus`, `gpt`, `qwen-max`, …) plus an optional `:level` effort suffix (`opus:high`). Separator spelling doesn't matter (`claude-opus-5.5`, `opus 5.5`, and `claude-opus-5-5` are one name), the `claude-` prefix and Claude Code's `[1m]` tag are optional, and the effort may follow a space or dash (`astra high`). A spelling that two models share is rejected rather than guessed. A value naming no catalog model is HTTP 400; for the typed command, a close typo (`opsu`) gets a "did you mean" hint but is never pinned. |
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
privacy context, and telemetry. The HMM strategy's roster ownership and the
rollback story for Go-owned deterministic selection are documented in
[HMM deterministic selection in Go](HMM_GO_SELECTION.md).

| Variable                           | Default | Purpose |
| ---------------------------------- | ------- | ------- |
| `ROUTER_POLICY_SIDECARS`           | *(none)* | JSON object mapping a new strategy ID to its sidecar origin, for example `{"quality-v2":"https://quality-v2.internal"}`. IDs must match `[a-z][a-z0-9_-]{0,63}`. At most 16 may be configured. `cluster`, `rl`, `hmm`, and `bandit` are reserved. |
| `ROUTER_POLICY_SIDECAR_AUTH`       | *(none)* | JSON object mapping configured generic strategy IDs to `none` or `google-id-token`, for example `{"quality-v2":"google-id-token"}`. Google ID-token mode uses the exact sidecar origin as token audience and fails router startup when application default credentials cannot build the client. |
| `ROUTER_POLICY_SIDECAR_TIMEOUT_MS` | `3000`  | Total timeout for each generic policy decision, including transient retries. Also bounds startup capability discovery. |
| `ROUTER_HMM_SIDECAR_TIMEOUT_MS`    | `3000`  | Total HMM decision timeout. |
| `ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS` | 60% of the decision timeout | Bounds a single HMM attempt so one stalled sidecar instance cannot spend the whole decision budget before the retries run. Set it equal to `ROUTER_HMM_SIDECAR_TIMEOUT_MS`, or to `0`, to let one attempt use the full budget. |
| `ROUTER_HMM_SIDECAR_AUTH`          | `none`  | Authentication for the HMM sidecar. Use `google-id-token` for managed Cloud Run; the exact sidecar origin is used as the token audience. |
| `ROUTER_POLICY_PIN_ENABLED`        | `false` | Registers the `x-weave-policy-pin: <policy_artifact_sha256>@<roster_sha256>` header on `/v1/messages`, `/v1/chat/completions`, `/v1/route` and `/v1/route/preview`. Off (default) the middleware is not registered, so the header has no effect and the telemetry columns `policy_pin_requested` / `policy_pin_honoured` stay NULL. On, the header value is only parsed for installations with `policy_header_overrides_enabled`: for every other installation the value (valid or not) is never inspected, the response is identical to a request without the header, and telemetry records `requested=true, honoured=false`. For an authorized installation a malformed value is HTTP 400 (`policy_pin_malformed`); a well-formed pin is honoured on policy-scored turns, which are scored fresh by the HMM sidecar with exactly that artifact (`HMM_PACKAGE_REGISTRY`) and select the arm from exactly that roster — session-sticky pins, `/force-model`, usage-bypass, blind-experiment passthrough and planner stays are not consulted. Auto/empty-model probes are independently scored without conversation state, including under an authorized policy pin. Non-scored utility exceptions retain their own path: probes with concrete models preserve the requested target unless explicitly overridden, compaction keeps its safeguard, and explicitly deployment-pinned titles keep their override. These exceptions may succeed with `policy_pin_honoured=false`. On policy-scored turns, if the artifact or roster is not loaded, or any path would serve a model the pinned policy did not pick, the turn fails HTTP 503 with typed reason `policy_pin_unavailable` and the telemetry row is written with `policy_pin_honoured=false` — an authorized pin never yields a 2xx with `policy_pin_honoured=false` on these turns, and the router never falls through to the current artifact or roster. |
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
  quantization). Default for every bundle through v0.66; the flat legacy
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

## Response context snapshot

Successful proxied responses report a per-request context snapshot in headers:

| Header | Meaning |
| --- | --- |
| `x-router-context-window` | Effective served model/provider window, in integer tokens; unchanged from the existing Pi contract. |
| `x-router-context-estimate-tokens` | Conservative whole-request overflow estimate, in integer tokens, using the same request envelope as capacity filtering. Includes image estimates; not a tokenizer count or client compaction percentage. |
| `x-router-context-output-reserve-tokens` | Output reserve used in capacity filtering: the greater of 8,000 tokens and the request's output limit. This part of the window is not input headroom. |
| `x-router-context-estimate-kind` | `approximate` in version 1. |
| `x-router-context-version` | `1` for the companion estimate contract. |

The estimate describes input to this Router request. The window describes the binding that served it, including provider failover. It does not describe the client's private context budget or the maximum window of the eligible pool. A conservative estimate can exceed the served window when the Router admits the widest candidates for the provider to decide actual fit. Do not derive client usage percentages or warnings from it. Omitted, unknown-version, or invalid values are unavailable, never zero. Clients must retain existing model displays when companions are absent. Native CLI integrations need no CORS changes.

Anthropic Messages, OpenAI Chat Completions/Responses through the HTTP adapter, Gemini, and semantic-cache replies carry this contract. Cache replies compute estimates from the current request rather than replaying stored headers. Non-HTTP transports that cannot preserve headers must omit the display.

The authenticated `GET /v1/sessions/:session_id/cost` response optionally includes `context_snapshot` for the latest conversation request (main loop, tool result, or client compaction) in that installation/session. It contains version, estimate kind/tokens, served window, output reserve, requested/served models, request ID, and UTC request/completion times. Snapshots expire five minutes after completion; a missing, malformed, failed latest request, or older Router omits the field. Title generation, probes, classifiers, recaps, and subagent dispatches do not replace the conversation snapshot. The existing read-key authentication and rate limits apply, and responses use `Cache-Control: no-store`. No eligible-model pool, private thresholds, credentials, or prompt content is returned. Telemetry is asynchronous, so a fetch can lag the completed turn. Semantic-cache hits have live headers but do not create an upstream telemetry snapshot; the session endpoint may still describe an earlier request.

Migration `0126` adds a nullable snapshot to existing request telemetry so client hooks can read it across Router replicas. Apply the migration before running the new binary; older binaries leave it null. This metadata bridge is required because lifecycle hooks cannot access inference response headers. It creates no new context-history endpoint or stored routine.
