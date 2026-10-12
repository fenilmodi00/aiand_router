# weave-bench — reproducible Codex-harness benchmarks for the router

Standalone Python tooling that reruns the router's published Codex comparisons
on **SWE-Atlas Codebase QnA** and **Terminal-Bench 4.0**: Codex CLI through the
router's `/beta` lane versus the same Codex CLI through the router with the
model pinned (`x-weave-force-model`, the headless `/force-model`).

Every arm runs through the router — the router is the only upstream. The
comparison is between per-request model selection and a pinned model.

Everything that determines a published number is pinned in code
(`weave_bench/benchmarks.py`, `weave_bench/manifests/`, `weave_bench/arms.py`)
and surfaced by `--dry-run`. Nothing here talks to WorkWeave-internal systems:
the router is whatever URL you configure, task definitions are fetched from
their official sources, and router-billed cost comes from the
public [analytics export](../docs/ANALYTICS_EXPORT.md).

Claude Code harnesses are out of scope for now (Codex only).

## What is in the box

| Piece | Where | Notes |
|---|---|---|
| Harbor agent `BetaCodex` | `weave_bench/agent/beta_codex.py`, `beta_turn.py` | Harbor's stock Codex agent plus: a Codex `config.toml` pointing at the router, and — for the `/beta` arm — a `/beta` first turn followed by `codex exec resume --last` so the whole trial runs on the beta lane. |
| Harbor launcher | `harbor_command.py`, `launch.py`, `cli.py` | Renders the exact `harbor run …` argv per arm; `--dry-run` prints it without spending. |
| Report | `trials.py`, `analytics.py`, `stats.py`, `report.py`, `markdown.py` | Pass rate + Wilson CI, task-level bootstrap CI, paired Δ, wins/ties/losses, exact sign test, McNemar, pass@k, $/trial (router-billed, with the catalog list price beside it), served-model mix, token/cache totals, error categories, agent-time median/p90. Markdown in the `RESULTS_*.md` layout plus `report.json`. |
| Prices | `prices.generated.json` | Generated from the router's `internal/router/catalog` by `make generate` (`cmd/genprices`). What the served tokens would cost at catalog list price, so the router's subscription/discount effect is visible. |

## Install

Requirements: Python ≥ 3.12, Docker (Harbor's default sandbox), `git`, and
[Harbor](https://github.com/laude-institute/harbor) 0.22.0 — pulled in by the
`harbor` extra. Codex itself is installed *inside* each sandbox by Harbor at
the pinned version; nothing touches your own `~/.codex`.

```bash
cd bench
export HARBOR_EXTRA=harbor  # run this block in one shell; use harbor-atlas for Atlas (Harbor 0.18.0)
uv sync --locked --extra "$HARBOR_EXTRA" --extra dev
cp bench.example.toml bench.toml         # git-ignored; edit URLs / env-var names
uv run --locked --extra "$HARBOR_EXTRA" weave-bench --help
```

Secrets are never written to disk: `bench.toml` names the environment
variables that hold them (defaults: `WEAVE_ROUTER_API_KEY`,
`WEAVE_ANALYTICS_KEY`, `AIAND_API_KEY` for the Atlas rubric judge). Every key
can be overridden as `WEAVE_BENCH_<SECTION>_<KEY>`, e.g.
`WEAVE_BENCH_ROUTER_BASE_URL`.

## 1. Verify what the router will serve

```bash
weave-bench probe
```

Prints the router build (`/health` → commit + cluster artifact version), the
sha256 of the stable HMM roster it is serving, and the router's reply to a
`/beta` turn. Exit status is 0 only if the reply contains `Beta enabled` — the
same acknowledgement the agent asserts at the start of every router trial (it
is saved per trial as `agent/codex-beta.txt` and counted in the report's
"Beta acks" column). **Record the probe output next to your results**: the
published numbers were produced against specific router builds and HMM
packages (see below); a different build is a different experiment.

## 2. Plan without spending

```bash
weave-bench run terminal-bench-4 --arms router,router-glm --smoke --dry-run
weave-bench run atlas-qna --dry-run                # full 124 tasks, router vs router-glm
```

`--dry-run` resolves the manifest (task count), pins (Harbor + Codex version),
per-arm Codex `config.toml`, which environment variables are read, and the full
`harbor run` argv. It performs no network calls. `--smoke` selects the pinned
≤3-task subset (`cad-model`; Atlas `task-6905333b74f22949d97ba998`).
`--tasks a,b` accepts explicit Harbor task names and refuses names
outside the pinned manifest.

## 3. Run

```bash
export WEAVE_ROUTER_API_KEY=rk_…
weave-bench run terminal-bench-4 --arms router,router-glm --smoke --run-id tb4-smoke-$(date +%Y%m%d)
weave-bench report terminal-bench-4 tb4-smoke-<date> --arms router,router-glm
```

Each arm is one Harbor job at `<jobs_dir>/<run-id>--<arm>/`; Harbor's own
`result.json`, per-trial `agent/codex.txt` (Codex `--json` event stream),
`agent/trajectory.json`, `verifier/` and `exception.txt` are what the report
reads. To continue an interrupted arm: `harbor jobs resume <jobs_dir>/<run-id>--<arm>`.

### Arms

Both arms reach the router at `[router] base_url` with the same Codex CLI; they
differ only in how the model is chosen.

| Arm | Upstream | Codex model | Effort | What it measures |
|---|---|---|---|---|
| `router` | router, `/beta` first turn | `zai-org/glm-5.3` (ignored; the beta policy picks per request) | high | the treatment |
| `router-glm` | router with `x-weave-force-model` | `zai-org/glm-5.3` (pinned) | high | the same traffic with routing disabled (headless `/force-model`) |

Router arms send `x-app: weave-eval-codex` and `x-weave-rollout-id: <run-id>` so
production telemetry (`client_app NOT LIKE 'weave-eval%'`) and force-pin alerts can
exclude the run, while the analytics export can still filter on rollout id. The router
strips the `weave-eval-` prefix before any harness-keyed behaviour runs, so an eval arm
is served exactly as `x-app: codex` would be (HMM harness roster, Codex `/models`,
compaction and footer policy); only the recorded `client_app` carries the prefix.

Both arms write a Codex `config.toml` (Harbor's `config` agent kwarg) because
extra request headers cannot reach the wire any other way; Harbor passes the
router key through its own `OPENAI_API_KEY` wiring, so the rendered file holds
no secret.

## 4. Report

```bash
export WEAVE_ANALYTICS_KEY=ra_…              # optional: router-billed cost + served-model mix
weave-bench report atlas-qna <run-id> [--arms router,router-glm] [--k 2]
```

Writes `<jobs_dir>/reports/<run-id>/report.md` (the table layout of the
published `RESULTS_*.md` docs) and `report.json`. Pairing is by
`<task>/<attempt>` with attempts numbered chronologically per task, so k=2
runs pair first-with-first and second-with-second.

Cost columns and where they come from:

| Column | Source | Notes |
|---|---|---|
| **router-billed** | analytics export `actual_input_cost_usd + actual_output_cost_usd`, rows joined to the trial's Codex thread id (`session_id`) | Router-computed at catalog rates with cache semantics — what the router bills. Needs a read-only `ra_` key with access to the same org that owns the `rk_` key. Rows appear after the export's ~60–90 s hold-back; the report waits for it. Shown beside **list**: the same served tokens repriced at `prices.generated.json`, so the gap is the router's subscription/discount effect. |

If no analytics key is set, the export is unreachable, or you pass
`--no-analytics`, router-billed fields stay blank — they are never estimated.
To reproduce a router-billed number after the fact you need either analytics
access to the org that ran it or its saved export: `--analytics-ndjson
PATH` replays a file written by an earlier report
(`reports/<run-id>/analytics-<window-start>-<window-end>.ndjson`; the window is
the trials' span, so a report re-run after `harbor jobs resume` fetches afresh)
or by any client of `GET /v1/analytics/routing-decisions`.

## Reproducing the published comparisons

All published runs: k=2 attempts, `--n-concurrent` as pinned (Atlas 24, TB4
12), the manifests below. Codex effort: Atlas pinned
`model_reasoning_effort=high` (Astra control `max`); the published TB4 launcher
set no effort in either arm, so both used Codex's default — this harness
sets `high` on every benchmark, so TB4 reruns are effort-pinned rather than
launcher-exact. The router arm ran against WorkWeave's staging router; the
HMM package it served is noted per run — your router's package is whatever
`weave-bench probe` reports.

The rows below are historical records: they were produced before the
AIand-only cut, and their control arms (direct OpenAI models, the OpenRouter
metarouter) no longer exist in this tool. Commands are recorded as the original
launchers' shape; rerun them with the surviving router arms (`router`,
`router-glm`) instead. Do not read the numbers as a current comparison.

| Comparison | Command | Published (router vs control) | Spend / wall clock |
|---|---|---|---|
| Atlas · router `/beta` vs Sol (recorded pre-AIand-only cut) | `weave-bench run atlas-qna --arms router,sol` | 47.6% vs 53.2% task-mean pass; $262 billed vs $445 list (pkg `6868da84`) | ≈ $940 incl. judge; 1.5 h wall at 24 concurrent |
| Atlas · router `/beta` vs Luna (recorded pre-AIand-only cut) | `--arms router,luna` | 60.1% vs 46.4%; $692 billed vs $127 list (pkg `468b99a9`) | ≈ $1,000 incl. judge; router arm 113 min, Luna 57 min |
| Atlas · router `/beta` vs OpenRouter auto-beta, max (recorded pre-AIand-only cut) | `--arms router,openrouter-beta` | 60.1% vs 60.9% — tie (Δ −0.8 pp, CI −8.2..+6.6, sign p=0.89); $692 vs $612 OpenRouter-billed | control 200 min wall in 4 batches of 31 tasks |
| Atlas · router `/beta` vs Astra, max (recorded pre-AIand-only cut) | `--arms router,astra` | 55.6% vs 57.7% (Δ −2.0 pp, CI −8.9..+4.8); $573 billed vs $1,250 list (pkg `17752a7d`) | Codex arms ≈ $1,820 + judge; median trial 6.0 vs 16.3 min |
| TB4 · router `/beta` vs Sol (recorded pre-AIand-only cut) | `weave-bench run terminal-bench-4 --arms router,sol` | 24.6% vs 30.3% trial pass, McNemar p=0.29; $399 billed vs $633 list (pkg `af0ef6ce`) | ≈ $1,030; 10 h wall for both arms |
| TB4 · router `/beta` vs Luna (recorded pre-AIand-only cut) | `--arms router,luna` | 25.8% vs 0.8%, McNemar p<0.0001; $651 billed vs $36 list (pkg `468b99a9`) | ≈ $690 |
| TB4 · router `/beta` vs OpenRouter auto-beta, xhigh (recorded pre-AIand-only cut) | `--arms router,openrouter-beta-xhigh` | 25.8% vs 19.0% (121/132 control trials graded), McNemar p=0.21; $651 vs $668 on graded trials | control $1,288 total incl. smoke + preemption losses |

Budget rule of thumb: a full Atlas or TB4 router arm is $36–$1,250 depending on
the models the router serves; the Atlas judge adds ≈ $75–110 per arm pair. Always `--smoke` first — the smoke tasks
exercise the full path (sandbox build, Codex install, `/beta` ack, verifier,
analytics join) for a few dollars.

### Pins

| | SWE-Atlas QnA | Terminal-Bench 4.0 |
|---|---|---|
| Tasks | 124 (`manifests/sweatlas_qna_tasks.json`) | 66 (`manifests/terminalbench4_tasks.json`) |
| Source | `scaleapi/SWE-Atlas` @ `49e4af3b`, `data/qa` (`weave-bench fetch atlas`) | Harbor registry `terminal-bench/terminal-bench@4.0.0`, content sha256 `39d9f44b…` |
| Harbor | 0.22.0 (published: 0.18.0 + a private patch equivalent to `BetaCodex`) | 0.22.0 |
| Codex CLI | 0.153.0 | 0.150.0 |
| Verifier | task rubrics judged by `deepseek-ai/deepseek-v4-pro` over the AIand-compatible endpoint (`EVAL_*` env, prompt ships in each task's `tests/`) | task tests |
| k / concurrency | 2 / 24 | 2 / 12 |

Override for ablations with `--n-attempts`, `--n-concurrent`,
`--codex-version`; the dry-run header shows the effective values and the
run id should say so.

## Known caveats

- **Upstream cyber-filter refusals.** Some TB4/Atlas tasks (security-flavoured
  repos) get `This request was flagged for possible cybersecurity risk` from
  the served upstream; Codex aborts and the trial scores 0. The report counts
  them under `cyber-filter-refusal`. Which models carry the filter depends on
  what the router serves, so the `/beta` arm can hit it where the force-pinned
  arm does not — read the paired Δ with and without them.
- **Harbor environment/setup failures.** Some TB4 images fail to build or
  cannot install Codex's prerequisites (`nodejs npm ripgrep`); these show as
  `harbor-environment-setup-failure` / `NonZeroAgentExitCodeError` with zero
  model spend, symmetrically across arms. Rerun the same run id to retry.
- **Atlas ran on Harbor 0.18.0.** The published Atlas numbers used a private
  Harbor 0.18 patch that added what `BetaCodex` + the `config` agent kwarg do
  in 0.22.0. Harbor's Codex install/exec shape changed between releases;
  `beta_turn.py` asserts the shape it rewrites and fails loudly otherwise.
- **Router build and HMM package.** `/beta` routes with whatever roster the
  router at `router.base_url` serves; the published runs name their package.
  Attach `weave-bench probe` output to any result.
- **Router-billed cost needs analytics access.** See §4. Without a `ra_` key
  for the org that ran the trials (or a saved NDJSON), router-billed cost is
  not reproducible. The published router-billed figures price cache writes at
  the catalog's 1.25× default; `aiand` rows report cache-inclusive
  `input_tokens`, which the report folds into fresh/read/write buckets.
- **Modal is optional and second.** Run locally (`environment = "docker"`)
  first; switch `[harbor] environment = "modal"` only after the docker path
  has produced a clean smoke. Modal preemption can lose whole batches — split
  long controls into `--tasks` batches with the same run id prefix.

## Development

```bash
cd bench
uv run --locked --extra dev ruff check . && uv run --locked --extra dev ruff format --check .
uv run --locked --extra dev pytest tests
cd .. && make generate && make check-docs        # regenerates prices.generated.json, checks links
```

Tests cover config parsing, the `/beta` command rewrite, analytics paging and
hold-back, the statistics, trial loading from Harbor's on-disk layout, report
aggregation and Markdown, and the CLI's dry-run/report paths — all offline.
