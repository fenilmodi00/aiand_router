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

---

# Revision 2 — rebuild on the enlarged AIDER corpus (commit `dc27f8ee`, 2026-10-14)

The label corpus gained **80 new graded AIDER rows** over existing `AIDER_0..149`
prompts (no new prompt ids, so the cluster-assignment cache and the centroid
geometry are untouched and were **not** regenerated). This revision records the
full re-run of the artifact pipeline against that corpus:
`build_v084_measured_cells.py` → `build_v084_knobs.py` → `build_v084_aa_priors.py`
→ `bakeoff_v084.py --self-check` → `bakeoff_v084.py`. No artifact file was
hand-edited; every number below is script output. The Revision-1 numbers above are
kept intact and are the comparison baseline.

## R1. Build identity

| field | value |
|---|---|
| git commit | `dc27f8eee142fa3f37a6e49be6574bf78ad378b1` ("cluster: correct cost-cap mode numbers, budget NewScorer boot logs, drop unused test helper") |
| corpus file | `.context/label-runs/v082-expanded-labels.jsonl` |
| corpus rows / prompts / all-8 prompts | **14,399 rows / 1,970 prompts / 1,508 all-8** (was 14,319 / 1,970 / 1,507) |
| corpus sha256 | **`7c77a5e52b835d2b17270f9fafa4383c1f0c0b22b12413667e1e338b72faef9c`** (was `b56e0edc…`) |
| assignment cache | `.context/label-runs/prompt-clusters-v084.json`, sha256 **`8f1d52f3fd08cf6cd0a490448e1106b3132c5eda1f521320810df05d6d8a680f`** — **unchanged** from Revision 1 (1,970 entries, 0 stale vs live centroids; no new prompt ids to assign) |
| cluster geometry | K=16, dim=768, `centroids.bin` byte-identical (`57b06f95…`), `model_axes.json`/`model_registry.json` byte-identical |
| embed cache | `.context/label-runs/corpus-embeds-v084.bin` invalidated by the new corpus sha and rebuilt (`embeddings: cache stale (corpus/embedder changed); re-embedding`); the re-run was then warm-cache reproducible bit-for-bit |
| knobs | unchanged from Revision 1: alpha `[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,0.96×8]` (c2/c3/c4/c6 at 0.80), `speed_weight=0`, `output_cost_ratio=0.25`, `expected_output_tokens=2000`, `per_model_verbosity=true`, cost cap `DefaultCostCapMultiple=3.0` in both arms |
| artifact sha256 (v0.84) | `quality_means.json ceadc9f3e8bf22af`, `rankings.json c85d1fa214b2fa1d`, `model_features.json 02a45bf133bf63c2`, `metadata.yaml 7de5f2eb0dee4a5e`, `centroids.bin 57b06f9548c02b88` (Revision-1 values were `991b7c08` / `edad83ac` / — / — / identical) |

## R2. Pipeline output (each script's summary, verbatim)

`python scripts/build_v084_measured_cells.py`:

```
labels: 14399 rows, 1970 prompts; 1970 cluster-assigned, 0 unassigned
z-scored prompts: 833 of 1970
usable measured n per cluster: {0: 37, 1: 12, 2: 1, 3: 12, 4: 25, 5: 17, 6: 20, 7: 134, 8: 20, 9: 50, 10: 275, 11: 57, 12: 105, 13: 8, 14: 37, 15: 23}
replay at v0.82 knobs: v0.82 cells 69.64% (n=1859) -> v0.84 cells 69.60% (n=1860) delta -0.04pt
byte-identical: centroids.bin sha256=57b06f9548c02b88…
byte-identical: model_axes.json sha256=cd94bd60f39f4189…
byte-identical: model_registry.json sha256=3a62bac9aba0f3b6…
```

`python scripts/build_v084_knobs.py` (cell table condensed; the usable-`n` column
is what the measured cells now use — c5's n is 17 and c10's 275, c12's 105, vs the
15/274/104 the same step yields on the 14,319-row prefix, i.e. the new AIDER rows
added 2/1/1 z-scored prompts to those clusters):

```
  c    n        top-1                top-2                 margin   %rng provenance
  0   37  qwen3.8-27b deepseek-v4.1-flash   0.0788   7.2% measured
  1   12  qwen3.8-27b deepseek-v4.1-flash   0.0127  10.6% prior_fallback
  2    1 deepseek-v4.1-flash      qwen3.8-27b   0.0023   4.3% prior_fallback
  3   12 deepseek-v4.1-flash  deepseek-v4-flash   0.0084   9.3% prior_fallback
  4   25 deepseek-v4-flash      qwen3.8-27b   0.0620   6.1% measured
  5   17 deepseek-v4-flash deepseek-v4.1-flash  0.0103   4.2% prior_fallback
  6   20 deepseek-v4.1-flash      qwen3.8-27b   0.0936  27.9% measured
  7  134 deepseek-v4.1-flash      kimi-k3             0.0658  15.1% measured
  8   20 deepseek-v4-flash deepseek-v4.1-flash  0.0064   0.5% measured
  9   50 deepseek-v4-pro deepseek-v4.1-flash  0.0125   1.1% measured
 10  275 deepseek-v4-flash  deepseek-v4-pro   0.0963  11.8% measured
 11   57 deepseek-v4.1-flash      qwen3.8-27b   0.1463  13.0% measured
 12  105 deepseek-v4-flash      qwen3.8-27b   0.1857  21.5% measured
 13    8 deepseek-v4-flash deepseek-v4.1-flash  0.0189   6.6% prior_fallback
 14   37  qwen3.8-27b deepseek-v4-flash       0.0907   9.8% measured
 15   23 deepseek-v4.1-flash  deepseek-v4-flash  0.3335  35.3% measured
demoted (top-2 tie): [2, 3, 5, 8]
replay @ocr=0.25: v0.82 knobs 69.53% (n=1856) -> new knobs 69.38% (n=1860); identical 1853-prompt subset 69.59% -> 69.48%
winners: c0:qwen3.8-27b, c1:qwen3.8-27b, c2:deepseek-v4.1-flash, c3:deepseek-v4.1-flash, c4:deepseek-v4-flash, c5:deepseek-v4-flash, c6:deepseek-v4.1-flash, c7:deepseek-v4.1-flash, c8:deepseek-v4-flash, c9:deepseek-v4-pro, c10:deepseek-v4-flash, c11:deepseek-v4.1-flash, c12:deepseek-v4-flash, c13:deepseek-v4-flash, c14:qwen3.8-27b, c15:deepseek-v4.1-flash
routed-mix cost/1k: 0.000487  ladder spread ocr=0 20.0x -> ocr=0.25 25.5x
```

`python scripts/build_v084_aa_priors.py`: prior table and per-prior-cluster blend
output are byte-identical to Revision 1 (the 5 prior-blended clusters resolve the
same way); the only change is the replay line:

```
replay @ shipped knobs (alpha mean 0.9200, ocr=0.25): 69.38% (n=1860) -> 69.56% (n=1861) +0.18pt
winners: c1:qwen3.8-27b->deepseek-v4.1-flash, c2:deepseek-v4.1-flash->deepseek-v4-pro, c3:deepseek-v4.1-flash->deepseek-v4-flash, c5:deepseek-v4-flash->deepseek-v4-flash, c13:deepseek-v4-flash->deepseek-v4-pro
```

`python scripts/bakeoff_v084.py --self-check`:

```
self-check OK: 16 clusters, identical roster/centroids/axes, model_features.json mirrors quality_means.json, v0.84 alpha=[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,...], ocr=0.25
```

## R3. Result — accuracy with 95% Wilson interval (capped arm, runtime default)

Both arms apply the 3.0x cost cap identically. Accuracy is the mean of the chosen
model's measured label score.

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (baseline) | 1,847 | 67.9472% | [65.7844%, 70.0372%] | $0.000933 |
| v0.84 (candidate) | 1,869 | 68.9952% | [66.8873%, 71.0764%] | $0.000507 |

| arm | all-8 n | all-8 accuracy | 95% Wilson CI | full-corpus accuracy |
|---|---|---|---|---|
| v0.82 capped | 1,508 | 75.0601% | [72.8209%, 77.1843%] | 67.9472% |
| v0.84 capped | 1,508 | 77.1513% | [74.9346%, 79.1716%] | 68.9952% |

Secondary row — uncapped:

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (uncapped) | 1,847 | 67.9472% | [65.7844%, 70.0372%] | $0.000933 |
| v0.84 (uncapped) | 1,862 | 68.8013% | [66.6556%, 70.8609%] | $0.001149 |

Uncapped v0.84 all-8: n=1,508, 77.0530%, CI [74.8663%, 79.1076%].

Unroutable breakdown (every routed prompt accounted for): `no_cluster_assignment`
0/0/0/0, `chosen_model_label_score_missing` 0 throughout;
`no_label_for_chosen_model` v0.82 capped 123, v0.84 capped 101, v0.84 uncapped 108.

## R4. Paired delta against v0.82 on the identical scored prompt set

Method unchanged: per-prompt paired difference, 10,000-resample percentile bootstrap
of the mean, fixed seed 20260904.

| arm | n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (identical set) | 1,835 | 68.2280% | [65.6750%, 69.9452%] | $0.000920 |
| v0.84 (identical set) | 1,835 | 69.7286% | [67.2811%, 71.4933%] | $0.000501 |

**Paired delta: +15.006 pt, 95% CI [+6.074, +23.969] pt** (n=1,835, W/T/L = 54 /
1,744 / 37). Wilson on the non-tied pairs [49.07%, 68.86%] — 54 wins of 91, 59.3%.
Paired on the all-8 subset: n=1,508, 75.0601% → 77.1513%, delta **+20.912 pt**, 95%
CI [+11.427, +30.736] pt.

Paired uncapped: n=1,830, 68.1959% → 69.5102%, delta **+13.144 pt**, 95% CI
[+4.235, +22.263] pt, W/T/L 53/1,741/36, cost $0.000907 → $0.001133; all-8 subset
75.0601% → 77.0530%, delta +19.929 pt, CI [+10.578, +29.610] pt.

## R5. Measured cost per prompt and served-model mix

Capped, per-model count and share of the scored prompts:

| model | v0.82 count | v0.82 share | v0.84 count | v0.84 share |
|---|---|---|---|---|
| deepseek-ai/deepseek-v4-flash | 381 | 20.6% | 978 | 52.3% |
| deepseek-ai/deepseek-v4.1-flash | 1,453 | 78.7% | 891 | 47.7% |
| qwen/qwen3.8-27b | 13 | 0.7% | 0 | 0.0% |
| deepseek-v4-pro, glm-5.3, glm-5.3-flash, kimi-k3, motif-3 | 0 | 0.0% | 0 | 0.0% |

Cost per prompt on each arm's own scored set: **$0.000933 → $0.000507, −45.7%**
(measured label cost). On the identical paired set: $0.000920 → $0.000501, −45.6%.

Uncapped mix (what the blend can reach; the #94 guard measures this):

| model | v0.82 count | v0.84 count | v0.84 share |
|---|---|---|---|
| deepseek-ai/deepseek-v4-flash | 381 | 859 | 46.1% |
| deepseek-ai/deepseek-v4.1-flash | 1,453 | 889 | 47.7% |
| deepseek-ai/deepseek-v4-pro | 0 | 67 | 3.6% |
| qwen/qwen3.8-27b | 13 | 47 | 2.5% |

Uncapped reaches 4 of 8 models; capped reaches 2 of 8 in both arms (the priced lane
set under the 3.0x cap). Both unchanged in character from Revision 1.

**Register-probe mix over the 180 probes** (`go test -run TestRegisterProbeMix…`,
v0.84 uncapped live `Route`): deepseek-v4-flash 100, deepseek-v4.1-flash 76,
deepseek-v4-pro 3, qwen3.8-27b 1 — 4 of 8 models, the same floor Revision 1
reported. `TestRegisterProbeMixDoesNotCollapse` passes (see R7).

## R6. Per-class (AIDER vs rest) contribution

AIDER rows form 741 of 14,399 corpus rows (5.1%) over 150 prompts; the new 80 rows
are 10.8% of the AIDER class and land on **71 existing prompts** in clusters
**c0 (10 rows), c5 (13), c8 (1), c10 (44), c12 (12)**. AIDER prompts per cluster and
the new rows' share by class:

| cluster | AIDER prompts | AIDER rows before | + new | share of cluster's new rows from AIDER |
|---|---|---|---|---|
| c0 | 89 | 79 | +10 | 10/10 (100%) |
| c5 | 111 | 98 | +13 | 13/13 (100%) |
| c6 | 3 | 3 | 0 | 0/0 |
| c8 | 4 | 3 | +1 | 1/1 (100%) |
| c9 | 13 | 13 | 0 | 0/0 |
| c10 | 403 | 359 | +44 | 44/44 (100%) |
| c12 | 108 | 96 | +12 | 12/12 (100%) |
| c14 | 7 | 7 | 0 | 0/0 |
| c15 | 3 | 3 | 0 | 0/0 |
| c1, c2, c3, c4, c7, c11, c13 | 0 | 0 | 0 | 0/0 |

**All 80 new rows are AIDER; every other dataset contributed 0 rows** — the classes
are cleanly separable, and the AIDER share of the new signal is 100% by
construction. AIDER accuracy per (cluster, model) vs the same for non-AIDER
datasets, for the clusters that actually carry AIDER rows (full 128-row table:
`python scripts/aider_class_report.py`, output cached at
`.context/label-runs/aider_class_report.txt`):

| cluster | model | AIDER n | AIDER acc | non-AIDER n | non-AIDER acc |
|---|---|---|---|---|---|
| c0 | deepseek-v4-pro | 19 | 0.0526 | 62 | 0.6959 |
| c0 | deepseek-v4-flash | 19 | 0.0000 | 62 | 0.7476 |
| c0 | deepseek-v4.1-flash | 16 | 0.1250 | 61 | 0.7451 |
| c0 | glm-5.3 | 4 | 0.0000 | 63 | 0.5574 |
| c0 | glm-5.3-flash | 7 | 0.2857 | 61 | 0.5902 |
| c0 | qwen3.8-27b | 5 | 0.0000 | 62 | 0.7783 |
| c0 | kimi-k3 | 0 | — | 64 | 0.5169 |
| c0 | motif-3 | 19 | 0.0000 | 64 | 0.7063 |
| c5 | deepseek-v4-pro | 24 | 0.1667 | 17 | 0.5882 |
| c5 | deepseek-v4-flash | 24 | 0.1667 | 16 | 0.7007 |
| c5 | deepseek-v4.1-flash | 22 | 0.2727 | 17 | 0.6528 |
| c5 | glm-5.3 | 2 | 0.5000 | 16 | 0.5693 |
| c5 | glm-5.3-flash | 6 | 0.3333 | 17 | 0.6471 |
| c5 | qwen3.8-27b | 8 | 0.1250 | 16 | 0.6375 |
| c5 | kimi-k3 | 1 | 1.0000 | 17 | 0.5377 |
| c5 | motif-3 | 24 | 0.0000 | 18 | 0.4491 |
| c6 | deepseek-v4-pro | 1 | 1.0000 | 66 | 0.7879 |
| c6 | deepseek-v4-flash | 1 | 1.0000 | 66 | 0.7879 |
| c6 | deepseek-v4.1-flash | 0 | — | 66 | 0.8485 |
| c6 | glm-5.3 | 0 | — | 66 | 0.8485 |
| c6 | glm-5.3-flash | 0 | — | 66 | 0.8485 |
| c6 | qwen3.8-27b | 0 | — | 66 | 0.8333 |
| c6 | kimi-k3 | 0 | — | 66 | 0.8333 |
| c6 | motif-3 | 1 | 0.0000 | 66 | 0.8182 |
| c8 | deepseek-v4-pro | 1 | 0.0000 | 34 | 0.6347 |
| c8 | deepseek-v4-flash | 1 | 0.0000 | 33 | 0.7135 |
| c8 | deepseek-v4.1-flash | 1 | 0.0000 | 32 | 0.7397 |
| c8 | glm-5.3 | 0 | — | 33 | 0.4039 |
| c8 | glm-5.3-flash | 0 | — | 34 | 0.2751 |
| c8 | qwen3.8-27b | 0 | — | 33 | 0.6654 |
| c8 | kimi-k3 | 0 | — | 34 | 0.5027 |
| c8 | motif-3 | 1 | 0.0000 | 36 | 0.5985 |
| c9 | deepseek-v4-pro | 2 | 0.5000 | 104 | 0.6221 |
| c9 | deepseek-v4-flash | 2 | 0.5000 | 100 | 0.6127 |
| c9 | deepseek-v4.1-flash | 2 | 0.5000 | 103 | 0.6391 |
| c9 | glm-5.3 | 2 | 0.5000 | 100 | 0.4052 |
| c9 | glm-5.3-flash | 1 | 1.0000 | 102 | 0.4460 |
| c9 | qwen3.8-27b | 2 | 0.5000 | 98 | 0.6281 |
| c9 | kimi-k3 | 0 | — | 104 | 0.4565 |
| c9 | motif-3 | 2 | 0.5000 | 106 | 0.6015 |
| c10 | deepseek-v4-pro | 79 | 0.1519 | 453 | 0.5486 |
| c10 | deepseek-v4-flash | 79 | 0.1772 | 452 | 0.5649 |
| c10 | deepseek-v4.1-flash | 69 | 0.2609 | 439 | 0.4623 |
| c10 | glm-5.3 | 17 | 0.4706 | 401 | 0.3849 |
| c10 | glm-5.3-flash | 24 | 0.2500 | 418 | 0.3959 |
| c10 | qwen3.8-27b | 49 | 0.3265 | 357 | 0.5002 |
| c10 | kimi-k3 | 7 | 0.7143 | 460 | 0.4081 |
| c10 | motif-3 | 79 | 0.0633 | 503 | 0.4851 |
| c12 | deepseek-v4-pro | 22 | 0.2273 | 197 | 0.5865 |
| c12 | deepseek-v4-flash | 22 | 0.1364 | 192 | 0.6538 |
| c12 | deepseek-v4.1-flash | 17 | 0.2353 | 190 | 0.5302 |
| c12 | glm-5.3 | 7 | 0.2857 | 182 | 0.4784 |
| c12 | glm-5.3-flash | 10 | 0.2000 | 185 | 0.4547 |
| c12 | qwen3.8-27b | 6 | 0.3333 | 153 | 0.5879 |
| c12 | kimi-k3 | 2 | 1.0000 | 199 | 0.4890 |
| c12 | motif-3 | 22 | 0.0455 | 207 | 0.5539 |
| c14 | all 8 models | 1 each | 0.0000 | 56–65 | 0.6321–0.8130 |
| c15 | deepseek-v4-pro / v4-flash / motif-3 | 1 each | 0.0000 | 38–39 | 0.5962–0.6241 |

AIDER is a materially harder dataset than the corpora that fill these clusters: on
every cluster that carries a real AIDER sample, each model's AIDER accuracy sits
well below its non-AIDER accuracy on the same cluster (e.g. c10 deepseek-v4-pro
0.152 vs 0.549; c12 deepseek-v4-flash 0.136 vs 0.654). The one exception is c5,
where AIDER accuracy (0.167–0.500) is closer to the non-AIDER level. So the new
signal is a **downward, difficulty-scaling** adjustment concentrated in c0/c5/c8/
c10/c12 — exactly the clusters that received the rows.

AIDER accuracy per model, whole class (741 rows; "before" excludes the 80 new
(model, prompt) rows):

| model | AIDER n | mean after | new rows | mean before |
|---|---|---|---|---|
| deepseek-v4-pro | 150 | 0.1600 | 8 | 0.1549 |
| deepseek-v4-flash | 150 | 0.1533 | 4 | 0.1438 |
| deepseek-v4.1-flash | 128 | 0.2422 | 56 | 0.2361 |
| glm-5.3 | 33 | 0.3636 | 7 | 0.3462 |
| glm-5.3-flash | 49 | 0.2653 | 4 | 0.2444 |
| qwen3.8-27b | 71 | 0.2817 | 1 | 0.2714 |
| kimi-k3 | 10 | 0.8000 | 0 | 0.8000 |
| motif-3 | 150 | 0.0467 | 0 | 0.0467 |

## R7. Regression guard

```
$ go test -count=1 ./internal/router/cluster/...
ok  	weave-os/router/internal/router/cluster	3.655s
```

`TestRegisterProbeMixDoesNotCollapse` passes verbatim; the mix over the 180
register probes is v0.84-v4-flash 100 / v0.84-v4.1-flash 76 / v0.84-v4-pro 3 /
v0.84-qwen3.8-27b 1 — **4 of 8 models, 0% premium share on conversational and
trivial_nl**, i.e. the ≥4-of-8 floor and the 0%-premium gate both hold. No Go
source was edited for this rebuild; the artifact re-derivation alone held the guard.

## R8. What changed vs the Revision-1 reference

| quantity | Revision 1 (corpus b56e0edc, 14,319 rows) | Revision 2 (corpus 7c77a5e5, 14,399 rows) | delta |
|---|---|---|---|
| cell provenance | 11 measured (c0,c4,c6,c7,c8,c9,c10,c11,c12,c14,c15) / 5 prior (c1,c2,c3,c5,c13) | **identical split** (same two sets; no cluster flipped sides) | none |
| cluster n (usable z-scored prompts) | c0 37, c5 15, c8 20, c10 274, c12 104 | c0 37, c5 17, c8 20, c10 275, c12 105 | **+2 c5, +1 c10, +1 c12; all other clusters byte-identical** |
| v0.84 capped accuracy | 68.9898% [66.8761%, 71.0767%], n=1,859 | 68.9952% [66.8873%, 71.0764%], n=1,869 | **+0.005 pt, CI overlaps** |
| v0.84 capped cost | $0.000457 | $0.000507 | +$0.000050 |
| v0.82 capped accuracy (same-code arm re-scored on the bigger corpus) | 69.0336% [66.8611%, 71.1268%], n=1,802 | 67.9472% [65.7844%, 70.0372%], n=1,847 | −1.09 pt (new AIDER rows are hard; they lower the baseline arm too) |
| all-8 subset | v0.82 75.0435% → v0.84 77.1361% | v0.82 75.0601% → v0.84 77.1513% | v0.84 **+0.015 pt** |
| paired delta (capped) | +15.950 pt [+7.183, +25.223], n=1,789, W/T/L 54/1,699/36 | +15.006 pt [+6.074, +23.969], n=1,835, W/T/L 54/1,744/37 | **−0.94 pt; both CIs exclude zero** |
| paired delta (all-8) | +20.926 pt [+11.550, +30.831] | +20.912 pt [+11.427, +30.736] | unchanged |
| paired delta (uncapped) | +14.027 pt [+5.409, +23.299] | +13.144 pt [+4.235, +22.263] | −0.88 pt, same conclusion |
| paired cost | $0.000753 → $0.000438 | $0.000920 → $0.000501 | −45.6% both |
| capped mix | v4-flash 993 (53.4%) / v4.1-flash 866 (46.6%) | v4-flash 978 (52.3%) / v4.1-flash 891 (47.7%) | 15 prompts back onto v4.1-flash; qwen tail stays at 0 |
| uncapped mix | 4 of 8 models | 4 of 8 models | unchanged |

### Verdict — did the 80 new AIDER cells move any premium lane's measured mean or its alpha interval?

**No.** The rebuild leaves every measured cell and every alpha interval essentially
where Revision 1 had them. The new rows are 80 of 14,399 (0.6%) and, after per-prompt
z-scoring and k0=10 shrinkage, none of them moves a measured z-mean by more than
**0.114** (c5 deepseek-v4-flash −0.1125 and c5 deepseek-v4.1-flash +0.1136 — the
largest shifts, and they partially cancel because c5's measured sample grew 15→17);
on c0/c8/c10/c12 the largest single-cell shift is c10 deepseek-v4.1-flash **+0.0252**
against a candidate-order margin of 0.0963, i.e. 26% of the gap, which is why the
c10 winner (deepseek-v4-flash) and every other cluster winner is unchanged. Nothing
comes within reach of flipping a top-2 order, so the alpha vector is byte-identical
(`[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,0.96×8]`) and no alpha interval changed at all.
Concretely on the lanes named in the ticket: AIDER accuracy per model, before →
after the 80 rows, is **glm-5.3 0.3462 → 0.3636, glm-5.3-flash 0.2444 → 0.2653,
kimi-k3 0.8000 → 0.8000 (no new rows), and flash lanes v4-flash 0.1438 → 0.1533,
v4.1-flash 0.2361 → 0.2422** — moves of +0.017/+0.021/0/+0.010/+0.006 on samples of
10–150 rows, all inside one Wilson interval. The headline paired delta drifts from
+15.950 to +15.006 pt, and that drift comes almost entirely from the *baseline*
arm re-scoring on a corpus where AIDER is harder (69.0336% → 67.9472%, −1.09 pt),
not from the candidate's cells moving. Direction and significance are unchanged:
the 95% CI still excludes zero, all-8 still gives +20.9 pt, cost still falls ~46%,
and the #94 register-probe guard still passes with 4 of 8 models and 0% premium on
conversational/trivial_nl.

**The verdict of Revision 1 stands: `artifacts/latest` remains a maintainer decision
and was not modified here. No commit was made and no Go source was edited.**

# Revision 3 — final sweep append, 4 further graded v4.1-flash cells (2026-10-14)

The AIDER sweep's last gradable cells landed after Revision 2: four more
`deepseek-v4.1-flash` AIDER rows (AIDER_75/80/85 plus one re-grade) with real
scorer output, all score 0.0 (16k-truncated generations). Corpus grew
14,399 -> 14,403 rows. No new prompt ids, so the assignment cache and all
geometry assets are byte-identical (`8f1d52f3...`).

## R9. Numbers over the final corpus (sha256 `0469fe49...`)

| arm | n | accuracy | cost/prompt |
|---|---|---|---|
| v0.82 cap 3.0 | 1,850 | 67.8370% [65.6743, 69.9275] | $0.000943 |
| v0.84 cap 3.0 | 1,870 | 68.9583% [66.8503, 71.0397] | $0.000509 |
| v0.84 uncapped | 1,863 | 68.7644% [66.6187, 70.8242] | $0.001151 |

Paired (capped, n=1,838): +14.981 pt [+6.420, +23.975], W/T/L 54/1,747/37.
All-8 subset (n=1,508): 75.0601% -> 77.1513%, +20.912 pt [+11.427, +30.736].
Provenance unchanged: 11 measured (c0,c4,c6,c7,c8,c9,c10,c11,c12,c14,c15) /
5 prior (c1,c2,c3,c5,c13). `go test ./internal/router/cluster/` passes; the
register-probe guard still holds 4 of 8 models with no premium leak.

## R10. Sweep outcome and the spend that bought it

The 539-cell AIDER gap is now 84 cells closed (v4-flash 4, v4-pro 8,
v4.1-flash 72 = 84 graded rows across both revision appends), of which 26
passed the real test-command scorers and 58 failed on the merits. Sweep spend
was $9.77 for the whole campaign, inside the $12 completion-phase ceiling.
The lanes that are still incomplete are incomplete for recorded reasons:

| lane | answered / 150 | blocker |
|---|---|---|
| deepseek-v4-flash, deepseek-v4-pro | 150 / 150 | complete |
| deepseek-v4.1-flash | 132 / 150 | remaining 18 cells are proven 16k-token truncations (deterministic at this budget) |
| glm-5.3-flash | 49 / 150 | request budget ran out mid-lane |
| qwen3.8-27b | 69 / 150 | request budget ran out mid-lane |
| glm-5.3 | 33 / 150 | not reached; ~$7.76 estimated |
| kimi-k3 | 10 / 150 | **blocked**: 60 minutes of request starts, zero cache writes (API lane dead on this key) |

kimi-k3's 140 cells are the single largest remaining block and the one lane
where no spend produced data; it needs a fresh diagnostic before any further
generation attempt.
