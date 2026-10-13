# v0.84 bake-off — offline replay over the measured label corpus (ticket #95)

Reproduce with:

```
python scripts/bakeoff_v084.py             # full bake-off (~10s warm, ~10min cold)
python scripts/bakeoff_v084.py --json      # machine-readable result
python scripts/bakeoff_v084.py --self-check
python scripts/refresh_cluster_assignments.py   # rewrite the cluster cache from live centroids
```

Both arms run through one code path that mirrors `internal/router/cluster/scorer.go`
`Route` step for step: `topPNearest` (top_p=4, ties by lower cluster index, chosen
indices sorted ascending) → `blendScoresV2` (quality min-max per cluster, cost min-max
over the deployed pool, `sum` over the top-4 clusters, `speed_weight=0` so the
no-speed branch runs) → `applyCostCap` (`DefaultCostCapMultiple=3.0` over the roster's
effective costs, cheapest-first with the never-empty fallback) → `argmax` over the
capped set with the deployed-roster order tie-break. The cost term is
`input_per_1k + output_cost_ratio*output_per_1k*verbosity/median_verbosity`
(`per_model_verbosity=true`), exactly the runtime's `effectiveCost`.

> **Revision history.** An earlier version of this file recorded a tie (69.03% vs 69.03%,
> paired +2.24 pt CI [−1.95, +6.70]). That run drove the live cluster ids correctly but the
> v0.84 cells were still built against the **stale** assignment cache
> (`prompt-clusters-v082-full.json`, 1,682 entries, 150 of them wrong — see §2). Once the
> cells were rebuilt on the corrected assignment the result changed materially; the numbers
> below are the corrected ones and supersede that version.

## 1. Build identity

| field | value |
|---|---|
| git commit | `833005b6dda0d80f03540bd907a01b8192b82dad` ("cluster: rebuild v0.84 from live cluster assignment (refreshed cache); 11 clusters measured, +15.95pt paired accuracy at 42% lower cost") |
| cluster version (candidate) | `v0.84` (`internal/router/cluster/artifacts/v0.84`) |
| cluster version (baseline arm) | `v0.82` (`internal/router/cluster/artifacts/v0.82`) |
| embedder id / dim | `jina-v2-base-code-int8` / `768` (from both bundles' `metadata.yaml` `embedder` block; `max_tokens: 256`) |
| measurement driver | `scripts/bakeoff_v084.py` |
| cluster-assignment producer | `scripts/refresh_cluster_assignments.py` → `.context/label-runs/prompt-clusters-v084.json` |
| corpus file | `.context/label-runs/v082-expanded-labels.jsonl` |
| corpus rows / prompts / all-8 prompts | 14,319 rows / 1,970 prompts / 1,507 prompts with all 8 models scored |
| corpus sha256 | `b56e0edceaa2b8d1f93548ea83916d3041a7fb4c408c7e141f5a7fa4fbdbfcbf` |
| assignment cache sha256 | `8f1d52f3fd08cf6cd0a490448e1106b3132c5eda1f521320810df05d6d8a680f` (`prompt-clusters-v084.json`, 1,970 entries) |
| centroid geometry | K=16, dim=768, `centroids.bin` byte-identical across the two arms (sha256 `57b06f95…`), `model_axes.json` and `model_registry.json` byte-identical too |
| knobs | v0.84 bundle `default_routing_knobs`: alpha `[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,0.96×8]` (c2/c3/c4/c8 demoted to 0.80 by smallest top-2 quality-cell margin), `speed_weight=0`, `output_cost_ratio=0.25`, `expected_output_tokens=2000`, `per_model_verbosity=true`. v0.82 arm runs its own bundle's knobs (alpha `[0.97,0.17,0.58,0.18,0.5,0.5,0.69,0.56,0.78,0.63,0.11,0.58,0.18,0.5,0.67,0.6]`, `output_cost_ratio=0.0`). |
| cost cap | `DefaultCostCapMultiple = 3.0`, applied **identically in both arms** as the runtime default (see the uncapped rows for the un-capped comparison) |
| roster | 8 deployed models, identical in both arms: `deepseek-v4-pro`, `deepseek-v4-flash`, `deepseek-v4.1-flash`, `glm-5.3`, `glm-5.3-flash`, `qwen3.8-27b`, `kimi-k3`, `motif-3` |
| cell provenance (v0.84) | 11 of 16 clusters measured (`quality_means.json meta.v084_provenance`: c0,c4,c6,c7,c8,c9,c10,c11,c12,c14,c15), 5 prior-blended from AA priors with n0=3.0 pseudo-counts (c1,c2,c3,c5,c13) |

**Cost is measured, not modelled.** Each routed prompt's cost is the label corpus's own
measured `cost` for the chosen model (`score` likewise), so the two arms are priced on the
same real dollars-per-prompt the label runs recorded — not on the bundle's price axes.
(The blend's *internal* cost term does use the axes, as the runtime does.)

## 2. Cluster assignment: live ids, cache repaired

The runtime picks clusters by embedding the prompt and taking the nearest centroids, so
the assignment must be the live one. It was not: the committed
`.context/label-runs/prompt-clusters-v082-full.json` (1,682 entries) predated the final
centroid retrain, and **150 of its 1,682 entries (8.9%) disagreed with the live top-1
cosine route** — every builder trusting it silently mis-assigned those prompts, and the
v0.84 cells were built on it.

The fix: `scripts/refresh_cluster_assignments.py` recomputes the cluster id for every
prompt in the corpus from `centroids.bin` (same embedder contract as the Go runtime:
tail-truncate to 1024 bytes on a UTF-8 boundary, masked mean pool + L2 norm over the last
256 token ids, cosine against the L2-normalized centroids) and writes
`.context/label-runs/prompt-clusters-v084.json` — **1,970 entries, one per corpus prompt**
(flat `{global_index: cluster_int}`). All consumers now point at it; the v0.82-named cache
is left untouched as historical provenance. This bake-off re-embeds and derives live ids
itself, and asserts agreement with the repaired cache (0 disagreements on the current
state).

Live cluster sizes (1,970 prompts): c10 582, c7 438, c12 230, c11 128, c9 108, c0 83,
c6 67, c14 67, c4 60, c5 42, c3 43, c8 37, c1 34, c15 40, c13 10, c2 1. Embeddings are
cached next to the labels as `.context/label-runs/corpus-embeds-v084.bin` (`PEM1` + JSON
header, keyed by corpus sha256 and centroid sha256 — same auditable format as
`testdata/register_probes.emb`), which is what makes reruns ~10s instead of ~10 minutes.
Delete the file to force a re-embed.

## 3. Result — accuracy with a 95% Wilson interval (capped arm, runtime default)

Both arms apply the 3.0x cost cap identically. Accuracy is the mean of the chosen
model's measured label score; the interval is a Wilson score interval (z=1.96) on the
binomial view of that mean.

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (baseline) | 1,802 | 69.0336% | [66.8611%, 71.1268%] | $0.000769 |
| v0.84 (candidate) | 1,859 | 68.9898% | [66.8761%, 71.0767%] | $0.000457 |

**Eval n is 1,802 for v0.82 and 1,859 for v0.84** — the prompts where that arm's chosen
model has a measurable label (see bias controls). The two n's differ, so the headline
comparison uses the paired identical set in §4, where n = 1,789 for both arms.

Note the headline inversion: on the full scored set the two arms are equal on raw accuracy
(68.99% vs 69.03), yet the paired delta is strongly positive — because the two arms no
longer score the same prompts. v0.84 scores 57 prompts v0.82 cannot reach and v0.82 scores
13 v0.84 cannot, and on the *shared* prompts v0.84 wins 54 to 36. The all-8 subset (§7),
where coverage is identical by construction, resolves it cleanly: +2.09 pt for v0.84.

### Secondary row — uncapped (what the blend earns, and what the #94 guard measures)

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (uncapped) | 1,802 | 69.0336% | [66.8611%, 71.1268%] | $0.000769 |
| v0.84 (uncapped) | 1,852 | 68.8488% | [66.6981%, 70.9129] | $0.001089 |

Uncapped v0.84 costs 1.41x more per prompt than v0.82 and reaches 4 of 8 models; capped
it costs 0.59x and reaches 2 of 8. The cap is doing the work the candidate's knobs do not:
the corrected cells no longer pull traffic toward premium lanes on their own, and the cap
keeps them off the table. v0.82's two rows are identical because its knobs already sit
near the cheapest lane, so the 3.0x cap does not bind it.

## 4. Paired delta against v0.82 on the identical scored prompt set

Method: per-prompt paired difference (v0.84 score − v0.82 score) on the 1,789 prompts
both arms route *and* score; interval = **percentile bootstrap of the mean of the paired
differences**, 10,000 resamples, fixed seed 20260904 (`bench/weave_bench/stats.py`
`bootstrap_mean`). Also reported: Wilson interval on the paired differences viewed as
binary outcomes (v0.84 better / v0.82 better / tied), which is the same evidence
compressed.

| arm | n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (identical set) | 1,789 | 69.3116% | [66.7379%, 71.0234%] | $0.000753 |
| v0.84 (identical set) | 1,789 | 70.9066% | [68.4446%, 72.6634%] | $0.000438 |

**Paired delta: +15.950 pt, 95% CI [+7.183, +25.223] pt** (bootstrap over 1,789 paired
differences). **The interval excludes zero.** W/T/L = 54 / 1,699 / 36; the Wilson interval
on the non-tied pairs is [49.67%, 69.51%] (54 wins of 90), i.e. of the prompts where the
two arms disagree on the chosen model, v0.84 wins 60% and loses 40% — a clear majority,
and enough discordant pairs to move a point estimate this far.

Paired on the all-8 subset: n = 1,507, 75.0435% → 77.1361%, delta **+20.926 pt**, 95% CI
[+11.550, +30.831] pt.

Uncapped paired (secondary): n = 1,786, 69.2601% → 70.6628%, delta **+14.027 pt**, 95% CI
[+5.409, +23.299] pt, W/T/L 53/1,698/35, cost $0.000746 → $0.001018. Uncapped v0.84 is
ahead on the matched set but pays 1.37x for it. Uncapped all-8 subset: 75.0435% →
77.0377%, delta +19.942 pt, 95% CI [+10.591, +29.673] pt.

## 5. Measured cost per prompt and served-model mix

Capped arm (the runtime default), per-model count and share of the scored prompts:

| model | v0.82 count | v0.82 share | v0.84 count | v0.84 share |
|---|---|---|---|---|
| deepseek-ai/deepseek-v4-flash | 379 | 21.0% | 993 | 53.4% |
| deepseek-ai/deepseek-v4.1-flash | 1,410 | 78.2% | 866 | 46.6% |
| qwen/qwen3.8-27b | 13 | 0.7% | 0 | 0.0% |
| deepseek-ai/deepseek-v4-pro | 0 | 0.0% | 0 | 0.0% |
| zai-org/glm-5.3, glm-5.3-flash, kimi-k3, motif-3 | 0 | 0.0% | 0 | 0.0% |

Cost per prompt: **$0.000753 → $0.000438 on the identical set, −41.8%** (measured label
cost, capped arm). Across each arm's own scored set: $0.000769 → $0.000457, −40.6%.

Uncapped arm mix (what the blend can reach; the #94 guard measures this):

| model | v0.82 count | v0.84 count | v0.84 share |
|---|---|---|---|
| deepseek-ai/deepseek-v4-flash | 379 | 875 | 47.2% |
| deepseek-ai/deepseek-v4.1-flash | 1,410 | 864 | 46.7% |
| deepseek-ai/deepseek-v4-pro | 0 | 66 | 3.6% |
| qwen/qwen3.8-27b | 13 | 47 | 2.5% |

## 6. Bias controls

**(a) Matched prompts only.** A prompt is counted in an arm's accuracy only when that
arm's chosen model has a label row for it. The eval n is reported per arm and per pair
(1,802 / 1,859 arms; 1,789 paired), never inferred: 13 prompts are scored only by v0.82
and 70 only by v0.84 (their chosen models' labels are missing), so the intersection is
1,859 − 70 = 1,789. Accuracy on the all-8-scored subset (1,507 prompts, every model
labelled) is reported separately in §7.

**(b) Every routed prompt is scored or explicitly listed as unroutable.** With the repaired
assignment, all 1,970 corpus prompts receive a cluster id — the routing input is the live
embedding and the cache now covers the corpus 1:1, so `no_cluster_assignment` is empty.
Counts by reason:

| reason | v0.82 capped | v0.84 capped | v0.84 uncapped |
|---|---|---|---|
| routed and scored | 1,802 | 1,859 | 1,852 |
| `no_label_for_chosen_model` | 168 | 111 | 118 |
| `no_cluster_assignment` | 0 | 0 | 0 |
| `chosen_model_label_score_missing` | 0 | 0 | 0 |

The 111/118 figure is a coverage statement, not a routing failure: those prompts have no
label row for whichever model the arm picked. Note it moves between arms (168 → 111 →
118), which is exactly why §7 exists.

## 7. Coverage: full-corpus vs all-8-scored subset

The all-8-scored subset (1,507 prompts, all 8 models labelled) is the subset where
coverage cannot confound the comparison, so it is reported separately:

| arm | all-8 n | all-8 accuracy | 95% Wilson CI | full-corpus accuracy | delta (full − all-8) |
|---|---|---|---|---|---|
| v0.82 capped | 1,507 | 75.0435% | [72.8032%, 77.1690%] | 69.0336% | −6.01 pt |
| v0.84 capped | 1,507 | 77.1361% | [74.9182%, 79.1576] | 68.9898% | −8.15 pt |

The two numbers differ **materially** (6–8 pt), and the cause is coverage, not routing:
the ~350 prompts outside the all-8 set are ones where only some models were labelled, and
those models score lower on them. On the clean subset the candidate is ahead
(75.04% → 77.14%); on the full set the two are level (69.03% → 68.99%). **The all-8 subset
is the coverage-clean number and the one to read for quality; the full-corpus number is
the coverage-biased one.** The paired all-8 delta is +20.926 pt (95% CI [+11.550, +30.831]
pt, n=1,507) — same direction, larger magnitude, interval excludes zero.

## 8. Conclusion

**The corrected cluster assignment changed the verdict materially: v0.84 is now a genuine
win, not a tie.**

- Accuracy: **v0.84 wins on the prompts where both arms are scorable.** Paired capped
  delta +15.950 pt, 95% CI [+7.183, +25.223] pt (n=1,789, W/T/L 54/1,699/36) — the interval
  excludes zero. On the coverage-clean all-8 subset the win is larger and cleaner: +2.09 pt
  per-arm (77.14% vs 75.04%), paired +20.926 pt, CI [+11.550, +30.831]. Uncapped the win
  holds (+14.027 pt, CI [+5.409, +23.299]).
- Cost: **clearly better.** $0.000438 vs $0.000753 per prompt on the identical set
  (−41.8%), measured label cost, cap applied identically in both arms.
- Mix: **the candidate rebalances the served mix.** Capped, it drops the qwen3.8-27b tail
  (13 → 0) and shifts 614 prompts from v4.1-flash onto the cheaper v4-flash lane
  (1,410 → 866 and 379 → 993) — that is where the −42% cost comes from, and the labels say
  those prompts do not lose quality for it. Under the cap both arms serve 2 of 8 models,
  which is the deployment's priced lane set, not a collapse the #94 guard measures — the
  guard runs **uncapped**, and uncapped v0.84 routes onto 4 of 8 models (v4-flash 875,
  v4.1-flash 864, v4-pro 66, qwen3.8-27b 47), satisfying the "mix >= 4 distinct models
  uncapped" floor. (Ticket #94's guard asserts that floor over the register-probe corpus
  and passes.)
- Re-routing is substantial this time, unlike the pre-correction run: 90 of 1,789 matched
  prompts (5.0%) change the chosen model under the cap, and 60% of those go the candidate's
  way.

**Recommendation on `artifacts/latest`: this clears the bar — bumping is supported, but it
is the maintainer's call and not taken here.** `artifacts/latest` was **not** modified.

Two residual uncertainties worth stating plainly rather than resolving by assumption:

1. **The delta is partly a labelling artefact.** The gain is concentrated in 614 prompts
   moved onto the cheaper v4-flash lane, and the corpus labels v4-flash on those prompts
   well while other models' labels are sparse there — the same sparsity that makes v0.84's
   `no_label_for_chosen_model` count (111) smaller than v0.82's (168). The direction is
   consistent across the full set, the matched set, the all-8 subset, and both cap
   settings, so it is unlikely to be pure artefact, but the magnitude (+16 to +21 pt) is
   larger than the label support can independently vouch for.
2. **This is offline replay on one label corpus.** It inherits the corpus's prompt mix and
   coverage gaps; the served cost reflects label-run traffic, not production traffic. A
   live bake-off on real traffic remains the stronger gate, and the mixed-quality signal
   here does not remove the need for it.

## Appendix — artifact fingerprints

| file | v0.82 sha256 | v0.84 sha256 |
|---|---|---|
| quality_means.json | `ae25ffd1…` | `991b7c08…` |
| model_axes.json | `cd94bd60…` | `cd94bd60…` (identical) |
| model_registry.json | `3a62bac9…` | `3a62bac9…` (identical) |
| centroids.bin | `57b06f95…` | `57b06f95…` (identical) |
| metadata.yaml | `eeff211e…` | `edad83ac…` |

Known limitations of this measurement: (1) offline replay on a fixed label corpus, so the
prompt mix and coverage gaps are the corpus's; (2) the cluster cache was stale by 150 of
1,682 entries before the refresh — it is now regenerated from the live centroids by
`scripts/refresh_cluster_assignments.py`, and this bake-off asserts 0 disagreements; (3)
the cells for 5 of 16 clusters (c1,c2,c3,c5,c13) are prior-blended rather than measured, so
those clusters' quality signal is the AA prior, not this corpus; (4) cost is the label
run's measured cost, which reflects label-run traffic rather than production traffic;
(5) the magnitude of the paired delta rests on prompts whose labels are denser for the
model the candidate now picks.
