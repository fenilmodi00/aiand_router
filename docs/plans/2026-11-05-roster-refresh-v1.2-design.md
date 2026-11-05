# Roster refresh v1.2 — flash-1017 split, kimi-k3.1-pro onboarding

Date: 2026-11-05 · Status: approved (user confirmed recommendations) · Issue: follow-up to #99/#104

## Intent

Refresh the auto-routing roster to match the live upstream catalog and the
newest measured quality data: onboard `deepseek-ai/deepseek-v4.1-flash-1017`
as the auto-routed flash (old flash goes pin-only pending upstream
deprecation), onboard `moonshotai/kimi-k3.1-pro` on an AA-anchored prior, and
demote `moonshotai/kimi-k3` to pin-only. Serve it as cluster bundle **v1.2**.

## Revision (2026-11-05, coordinator-approved mid-build)

The kimi-k3.1-pro **lane elevation is cancelled**. Original decision 2
proposed an AA-anchored quality lift (base column cloned from glm-5.3, then
an additive overlay on the agentic/hard clusters {0,7,8,12,13} so k3.1-pro
wins those lanes). Verified against origin/main: upstream has never given any
kimi row a bundle entry built from published leaderboard scores; the day-0
rule (v0.70 metadata) is *a new model can at most TIE the best existing
coder, never extrapolate*; sonnet-5 won its lane only via a **measured**
SWE-bench Easy probe, not published AA scores. Published AA scores do not
move quality_means. **Revised kimi swap:** k3.1-pro enters the pool as
roster-listed-but-inert — quality column = kimi-k3's v1.1 column (its direct
predecessor, the same prior lineage muse-spark uses), `proxy: true`,
proxy_note naming kimi-k3 as predecessor, and the **whole operational block
(cost axis included) borrowed from kimi-k3** (borrowing the cost axis
wholesale keeps the blend's cost-normalization span identical to v1.1, so
routing is bit-identical and zero probe flips — the promotion gate). It wins
ZERO clusters. Catalog pricing keeps the real $2/$10 (billing source); the
bundle cost axis is a prior, not a price. Lane elevation is deferred until a
measured probe grade exists (#96 / re-anchor): at that point the cost span
changes and lanes may legitimately flip. Routing outcome: 1017 takes c1 from
v4.1-flash by same-column swap (same as the v0.75 opus-5 same-line-swap
precedent); everything else is unchanged.

## Decisions (user-locked)

1. **Flash split.** `deepseek/deepseek-v4.1-flash-1017` (TierLow) is the
   auto-routed flash; `deepseek/deepseek-v4.1-flash` drops to pin-only
   (untiered, force-reachable) — same treatment as `deepseek-v4-pro-0813`.
2. **Kimi swap (revised above).** `moonshotai/kimi-k3.1-pro` (TierHigh)
   added, auto-routed; `moonshotai/kimi-k3` drops to pin-only. Day-0 k3.1-pro
   is roster-listed-but-inert on the kimi-k3 predecessor prior (no lane
   elevation — see the Revision section); lane elevation deferred until
   measured grades exist.
3. **Conflict rule (revised above).** When AA and our measured cells disagree
   about a lane winner: measured-until-recalibrated (qwen keeps c6). Models
   with no measured row enter on their predecessor's prior and stay inert
   until a measured probe grade exists — the day-0 no-extrapolation rule; a
   published AA score alone never moves a quality cell (the sonnet-5 lane win
   came from a measured SWE-bench Easy probe, not its published index).
4. **muse-spark stays** in the pool on the kimi-k3 proxy prior, metadata marked
   "pending re-anchor" (its prior is now known-stale after kimi demotion; do
   not invent a lane for it — wait for AA grades or #96 measurement).

## Verified upstream facts (live `GET https://api.aiand.com/v1/models`, 2026-11-05)

- `deepseek-ai/deepseek-v4.1-flash-1017`: $0.30/$0.60/$0.02-cache per 1M,
  ctx 1,048,576, efforts none/low/high/xhigh/max default high,
  vision+document+tool_calling.
- `moonshotai/kimi-k3.1-pro`: $2.00/$10.00/$0.20-cache per 1M, ctx 1,048,576,
  efforts low/high/max default high, text+image input,
  chat+reasoning+vision+document+tool_calling.
- AA (artificialanalysis.ai, 2026-10-23): k3.1-pro Intelligence Index **55**
  (#4 overall, #1 open weights when published), Terminal-Bench 4.0 **67%**
  (best of any model, ahead of Claude Sonnet 5.5 at 64%), GDPval-AA 1846 Elo
  (3rd). Kimi-k3 = 44, glm-5.3 = 45, glm-5.3-flash = 42, qwen3.8-27b = 34,
  v4.1-flash = 39.

## Design

### Catalog (internal/router/catalog)

- New `ModelIDDeepSeekV41Flash1017 = "deepseek/deepseek-v4.1-flash-1017"`,
  TierLow, ctx 1_048_576, `effortsNoneHighXHighMax` (match upstream list
  none/low/high/xhigh/max — reuse or add slice per existing constants),
  DefaultEffort high, binding UpstreamID `deepseek-ai/deepseek-v4.1-flash-1017`,
  Price{0.30, 0.60, 0.02/0.30}. Same capabilities as old flash (multimodal —
  no ImageInputUnsupported).
- `deepseek/deepseek-v4.1-flash` row: remove Tier (→ TierUnknown), keep
  bindings/pricing, comment "pin-only: upstream deprecation pending; 1017
  release is the auto-routed flash" (mirrors the v4-pro-0813 comment style).
- New `ModelIDKimiK31Pro = "moonshotai/kimi-k3.1-pro"`, TierHigh,
  ctx 1_048_576, multimodal (no ImageInputUnsupported), efforts low/high/max
  default max, binding UpstreamID `moonshotai/kimi-k3.1-pro`,
  Price{2.00, 10.00, 0.20/2.00}.
- `moonshotai/kimi-k3` row: remove Tier, comment "pin-only: k3.1-pro supersedes
  it on the premium lane" — keep pricing/bindings.
- `ApprovedAutoRoutedModels` (roster.go): swap `deepseek/deepseek-v4.1-flash`
  → `deepseek/deepseek-v4.1-flash-1017` and `moonshotai/kimi-k3` →
  `moonshotai/kimi-k3.1-pro`; the other six members (v4-pro, glm-5.3,
  glm-5.3-flash, motif-3, qwen3.8-27b, muse-spark) stay. Size stays 8:
  two swaps, no net additions.
- Tests: tier-split re-derived (8 tiered / 5 pin-only / 13 total);
  `aiandUpstreamIDs` gains both new upstream IDs (old rows keep theirs);
  pricing tests for both new rows; roster and tier-split tests updated to
  the new sets; the #102 guard invariants re-run against the new registry.

### Bundle v1.2 (internal/router/cluster/artifacts/v1.2)

Derived from v1.1; `centroids.bin` and embedder contract byte-identical
(sha256-recorded). Registry changes:

- Remove `deepseek/deepseek-v4.1-flash`, add
  `deepseek/deepseek-v4.1-flash-1017` with `proxy: true`, proxy_note cloning
  the v4.1-flash column ("same v4.1 line, newer 1017 weights, identical
  price"; mirror the v1.0 v4.1-flash entry's day-0 clone note).
- Remove `moonshotai/kimi-k3`, add `moonshotai/kimi-k3.1-pro` with
  `proxy: true`, proxy_note documenting the **inert kimi-k3 predecessor
  prior** (per the Revision section: no lane elevation, no AA lift): quality
  column cloned from kimi-k3's v1.1 column (its direct predecessor — the
  same prior lineage muse-spark uses), and the **whole operational block
  (cost axis included) borrowed from kimi-k3 wholesale**. Catalog pricing
  stays the real $2/$10 (billing source); the bundle cost axis is a prior,
  not a price. Borrowing the cost axis wholesale keeps the blend's
  cost-normalization span identical to v1.1, so routing is bit-identical.
- `quality_means.json`: every cluster 0-15 gains rows for both new models —
  1017 = v4.1-flash's value; k3.1-pro = kimi-k3's value. Loader fail-fast
  guarantees no cluster is missed.
- `model_axes.json`/`model_features.json` gain entries for both — 1017's
  copied from v4.1-flash (0.0003/0.0006 per 1k), k3.1-pro's wholesale from
  kimi-k3 (tps/ttft/verbosity AND the 0.003/0.0125 cost prior, documented);
  muse-spark's entry unchanged; its `prior_status: "pending_re_anchor"` note
  lives in metadata.yaml (the JSON schema does not carry the field).
- `metadata.yaml`: version v1.2, parent v1.1, changelog prepended documenting
  both swaps, the AA anchors (recorded, not applied — with sources+dates),
  the borrows (including the cost-axis borrow and the real-vs-prior price
  distinction), and that centroid bytes are unchanged. Knobs unchanged
  (per-cluster).
- `rankings.json` parity artifact extended via the build formula (values
  byte-equal to v1.1 modulo the two column renames).
- `artifacts/latest` → `v1.2`.

### Guard rails (must hold)

- #102 invariants: registry ⊆ approved ⊆ tiered; old flash and kimi-k3 must
  be absent from registry; both still force-dispatchable (pin-only rows).
- **Zero probe flips vs v1.1 as served is the hard assertion** (revised — was
  "only AA-targeted lanes may flip"). Verified cross-process (old catalog vs
  new catalog, see Expected live outcome): rename-only diff, zero flips.
  1017 inherits c1 by the same-column swap; k3.1-pro wins nothing.
  routing-report approved-set assertion runs on v1.2.

### Expected live outcome

Routing is bit-identical to v1.1 **as served** (verified cross-process: v1.1
under the old catalog vs v1.2 under the new catalog, 180 register probes,
zero differences beyond the two model-ID renames — per-probe captures in
`.local/routing_v1_1_old_catalog.json` / `.local/routing_v1_2_new_catalog.json`).
kimi-k3.1-pro is roster-listed-but-inert (selectable via exclusions/pins,
wins no argmax lane); 1017 serves c1 exactly as v4.1-flash did. The deferred
decision point: when k3.1-pro is re-anchored with real measured grades + its
real $2/$10 prices, the bundle cost span changes and lanes may legitimately
flip then — gated on the #96 measurement campaign.

### Why the in-process `--baseline` diff is invalid for swap bundles

`routing-report --target v1.2 --baseline v1.1` inside one process compares
v1.1 under the NEW catalog: the pin-only demotion makes the scorer drop the
old flash + kimi-k3 columns (untiered ⇒ not eligible), so v1.1's blend
normalizes over 6 models while v1.2's runs over 8. Min-max spans differ, so
identical cell values produce different blend scores and ~41/180 probes
appear to flip — a baseline that never existed in production. The honest
gate is cross-process: v1.1 under the OLD catalog (its real serving state)
vs v1.2 under the new catalog; that diff is rename-only (zero flips).

## Out of scope

- No centroid/embedder/alpha changes; no re-training; #96 calibration
  campaign untouched; muse-spark re-anchor deferred until AA grades exist;
  no changes to the scorer/blend code.

## Verification plan

`go build`/`go vet` clean; full `go test ./...` green; routing-report on v1.2
(lane-flip diff vs v1.1 recorded); centroid sha256 equality v1.1↔v1.2; compose
bump to `ROUTER_CLUSTER_VERSION: v1.2`; live probe battery re-run focusing
agentic + easy + trivial lanes, cost capture per class; both new models
confirmed serving live inside the approved set.
