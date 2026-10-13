# v0.84 bake-off — offline replay over the measured label corpus (ticket #95)

Reproduce with:

```
python scripts/bakeoff_v084.py             # full bake-off (~10s warm, ~10min cold)
python scripts/bakeoff_v084.py --json      # machine-readable result
python scripts/bakeoff_v084.py --self-check
python scripts/bakeoff_v084.py --verify-clusters   # re-embed, audit the stale cache
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

## 1. Build identity

| field | value |
|---|---|
| git commit | `5ba4878169964080b0297055deaf2cf1d141cf85` ("cluster: rebuild v0.84 from enlarged label corpus (49 graded rows); priors keep 7 thin clusters, mix guard scoped to the premium rung") |
| cluster version (candidate) | `v0.84` (`internal/router/cluster/artifacts/v0.84`) |
| cluster version (baseline arm) | `v0.82` (`internal/router/cluster/artifacts/v0.82`) |
| embedder id / dim | `jina-v2-base-code-int8` / `768` (from both bundles' `metadata.yaml` `embedder` block; `max_tokens: 256`) |
| measurement driver | `scripts/bakeoff_v084.py` |
| corpus file | `.context/label-runs/v082-expanded-labels.jsonl` |
| corpus rows / prompts / all-8 prompts | 14,319 rows / 1,970 prompts / 1,507 prompts with all 8 models scored |
| corpus sha256 | `b56e0edceaa2b8d1f93548ea83916d3041a7fb4c408c7e141f5a7fa4fbdbfcbf` |
| centroid geometry | K=16, dim=768, `centroids.bin` byte-identical across the two arms (sha256 `57b06f95…`), `model_axes.json` and `model_registry.json` byte-identical too |
| knobs | v0.84 bundle `default_routing_knobs`: alpha `[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,0.96×8]` (c2/c3/c4/c6 demoted to 0.80), `speed_weight=0`, `output_cost_ratio=0.25`, `expected_output_tokens=2000`, `per_model_verbosity=true`. v0.82 arm runs its own bundle's knobs (alpha `[0.97,0.17,0.58,0.18,0.5,0.5,0.69,0.56,0.78,0.63,0.11,0.58,0.18,0.5,0.67,0.6]`, `output_cost_ratio=0.0`). |
| cost cap | `DefaultCostCapMultiple = 3.0`, applied **identically in both arms** as the runtime default (see the uncapped rows for the un-capped comparison) |
| roster | 8 deployed models, identical in both arms: `deepseek-v4-pro`, `deepseek-v4-flash`, `deepseek-v4.1-flash`, `glm-5.3`, `glm-5.3-flash`, `qwen3.8-27b`, `kimi-k3`, `motif-3` |
| cell provenance (v0.84) | 9 of 16 clusters measured (`quality_means.json meta.v084_provenance`: c0,c7,c8,c9,c10,c11,c12,c14,c15), 7 prior-blended from AA priors with n0=3.0 pseudo-counts (c1,c2,c3,c4,c5,c6,c13) |

**Cost is measured, not modelled.** Each routed prompt's cost is the label corpus's own
measured `cost` for the chosen model (`score` likewise), so the two arms are priced on the
same real dollars-per-prompt the label runs recorded — not on the bundle's price axes.
(The blend's *internal* cost term does use the axes, as the runtime does.)

## 2. Cluster assignment: live, not the stale cache

The runtime picks clusters by embedding the prompt and taking the nearest centroids, so
the bake-off re-embeds all 1,970 prompts with the committed `jina-v2-base-code-int8`
ONNX assets (same masked-mean-pool + L2-norm contract as the Go embedder) and derives
each prompt's cluster id from `centroids.bin` live. The committed
`.context/label-runs/prompt-clusters-v082-full.json` was **audited, not trusted**: it
predates the final centroid retrain and **150 of its 1,682 entries (8.9%) disagree with
the live top-1 cosine route** (`--verify-clusters` prints the list). Live cluster sizes:
c10 582, c7 438, c12 230, c11 128, c9 108, c0 83, c6 67, c14 67, c4 60, c5 42, c3 43,
c15 40, c1 34, c8 37, c13 10, c2 1. Both arms consume the identical live ids, so the
pairing is unaffected by the staleness. Embeddings are cached next to the labels as
`.context/label-runs/corpus-embeds-v084.bin` (`PEM1` + JSON header, keyed by corpus
sha256 and centroid sha256 — same auditable format as `testdata/register_probes.emb`),
which is what makes reruns ~10s instead of ~10 minutes. Delete the file to force a
re-embed.

## 3. Result — accuracy with a 95% Wilson interval (capped arm, runtime default)

Both arms apply the 3.0x cost cap identically. Accuracy is the mean of the chosen
model's measured label score; the interval is a Wilson score interval (z=1.96) on the
binomial view of that mean.

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (baseline) | 1,802 | 69.0336% | [66.8611%, 71.1268%] | $0.000769 |
| v0.84 (candidate) | 1,818 | 69.0319% | [66.8683%, 71.1153%] | $0.000637 |

**Eval n is 1,802 for v0.82 and 1,818 for v0.84** — the prompts where that arm's chosen
model has a measurable label (see bias controls). The two n's differ by 16, so the
headline comparison uses the paired identical set in §4, where n = 1,797 for both.

### Secondary row — uncapped (what the blend earns, and what the #94 guard measures)

| arm | eval n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (uncapped) | 1,802 | 69.0336% | [66.8611%, 71.1268%] | $0.000769 |
| v0.84 (uncapped) | 1,679 | 70.4965% | [68.2923%, 72.6503%] | $0.005897 |

The cap is what confines v0.84 to the cheap lanes: uncapped it reaches 4 models and pays
7.7x more per prompt. On this corpus the cap binds v0.84 but not v0.82 (its knobs already
sit near the cheapest lane), which is why the two v0.82 rows are identical.

## 4. Paired delta against v0.82 on the identical scored prompt set

Method: per-prompt paired difference (v0.84 score − v0.82 score) on the 1,797 prompts
both arms route *and* score; interval = **percentile bootstrap of the mean of the paired
differences**, 10,000 resamples, fixed seed 20260904 (`bench/weave_bench/stats.py`
`bootstrap_mean`). Also reported: Wilson interval on the paired differences viewed as
binary outcomes (v0.84 better / v0.82 better / tied), which is the same evidence
compressed.

| arm | n | accuracy | 95% Wilson CI | cost/prompt |
|---|---|---|---|---|
| v0.82 (identical set) | 1,797 | 69.1700% | [66.6007%, 70.8818%] | $0.000760 |
| v0.84 (identical set) | 1,797 | 69.3935% | [66.8270%, 71.0997%] | $0.000635 |

**Paired delta: +2.235 pt, 95% CI [−1.946, +6.698] pt** (bootstrap over 1,797 paired
differences). W/T/L = 13 / 1,775 / 9. The Wilson interval on the non-tied pairs is
[38.73%, 76.74%] (13 wins of 22), i.e. the same conclusion: **the accuracy difference is
not statistically established**. The interval is wide because the two arms choose the
same model on 98.8% of matched prompts (only 22 discordant pairs out of 1,797), so there
is very little paired signal in either direction. That is a property of this artifact
pair — the candidate re-routes ~1.2% of the corpus under the cap — not a defect of the
estimator.

Uncapped paired (secondary): n = 1,648, 71.7176% → 71.2739%, delta **−4.437 pt**, 95% CI
[−16.390, +7.317] pt, W/T/L 61/1,523/64. Uncapped v0.84 buys +1.46 pt of raw accuracy
for 7.8x the cost, and on the matched set it is behind.

## 5. Measured cost per prompt and served-model mix

Capped arm (the runtime default), per-model count and share of the scored prompts:

| model | v0.82 count | v0.82 share | v0.84 count | v0.84 share |
|---|---|---|---|---|
| deepseek-ai/deepseek-v4.1-flash | 1,410 | 78.2% | 1,358 | 74.7% |
| deepseek-ai/deepseek-v4-flash | 379 | 21.0% | 460 | 25.3% |
| qwen/qwen3.8-27b | 13 | 0.7% | 0 | 0.0% |
| deepseek-ai/deepseek-v4-pro | 0 | 0.0% | 0 | 0.0% |
| zai-org/glm-5.3, glm-5.3-flash, kimi-k3, motif-3 | 0 | 0.0% | 0 | 0.0% |

Cost per prompt: **$0.000769 → $0.000637, −17.1%** (measured label cost, capped arm).

Uncapped arm mix (what the blend can reach; the #94 guard measures this):

| model | v0.82 count | v0.84 count |
|---|---|---|
| deepseek-ai/deepseek-v4.1-flash | 1,410 | 897 |
| qwen/qwen3.8-27b | 13 | 664 |
| deepseek-ai/deepseek-v4-flash | 379 | 82 |
| deepseek-ai/deepseek-v4-pro | 0 | 36 |

## 6. Bias controls

**(a) Matched prompts only.** A prompt is counted in an arm's accuracy only when that
arm's chosen model has a label row for it. The eval n is reported per arm and per pair
(1,802 / 1,818 arms; 1,797 paired), never inferred: 5 prompts are scored only by v0.82
and 21 only by v0.84 (their chosen models' labels are missing), so the intersection is
1,818 − 21 = 1,797. Accuracy on the all-8-scored subset (1,507 prompts, every model
labelled) is reported separately in §7.

**(b) Every routed prompt is scored or explicitly listed as unroutable.** Of the 1,970
corpus prompts: 288 have no cluster assignment (their `global_index` is absent from the
label-run corpus entirely — the routing input is the live embedding, and the corpus itself
does not cover them), and the remainder are routed. Counts by reason:

| reason | v0.82 | v0.84 |
|---|---|---|
| routed and scored | 1,802 | 1,818 |
| `no_label_for_chosen_model` | 168 | 152 |
| `no_cluster_assignment` | 0 (live embedding always assigns) | 0 |
| `chosen_model_label_score_missing` | 0 | 0 |

Under the uncapped arm the same accounting gives v0.84 1,679 scored with 291
`no_label_for_chosen_model` (v0.84 reaches `deepseek-v4-pro`/`qwen3.8-27b`, whose labels
are sparse on the extra prompts), v0.82 unchanged at 1,802/168.

## 7. Coverage: full-corpus vs all-8-scored subset

The all-8-scored subset (1,507 prompts, all 8 models labelled) is the subset where
coverage cannot confound the comparison, so it is reported separately:

| arm | all-8 n | all-8 accuracy | 95% Wilson CI | full-corpus accuracy | delta (full − all-8) |
|---|---|---|---|---|---|
| v0.82 capped | 1,507 | 75.0435% | [72.8032%, 77.1690%] | 69.0336% | −6.01 pt |
| v0.84 capped | 1,507 | 75.3741% | [73.1439%, 77.4902%] | 69.0319% | −6.34 pt |

The two numbers differ **materially** (≈6 pt), and the cause is coverage, not routing:
the 295 extra prompts in the full number include many where only cheap models were
labelled, and those models score lower on them. The paired all-8 delta is +3.306 pt
(95% CI [−1.451, +8.605] pt, n=1,507) — the same direction and the same
not-significant verdict as the full-corpus pair. Which is which: **the full-corpus figure
is the coverage-biased one; the all-8 subset is the clean one.**

## 8. Conclusion

**The bar is met on cost and mix, not on accuracy.**

- Accuracy: **tie, not a win.** Capped, v0.84 69.0319% vs v0.82 69.0336% on nearly the
  same prompt set; the paired delta is +2.24 pt with a CI spanning zero, and the discordant
  pairs are only 22 of 1,797. Uncapped, v0.84 gains +1.46 pt of raw accuracy but costs
  7.7x more per prompt and loses on the matched set. The accuracy claim is **not
  statistically established** in either direction.
- Cost: **clearly better.** $0.000637 vs $0.000769 per prompt (−17.1%), measured label
  cost, cap applied identically in both arms.
- Mix: **v0.84 spreads the traffic the cap allows.** It drops the qwen3.8-27b tail (13 →
  0) and shifts 81 prompts from v4.1-flash onto the cheaper v4-flash lane (1,410 → 1,358
  and 379 → 460), which is exactly the cost saving: the candidate's cells and knobs move
  volume toward the cheaper of the two cap-surviving lanes. Under the cap both arms serve
  2–3 of 8 models, which is the deployment's priced lane set, not a collapse the #94 guard
  measures — the guard runs **uncapped**, and uncapped v0.84 routes onto 4 of 8 models
  (v4.1-flash 897, qwen3.8-27b 664, v4-flash 82, v4-pro 36), satisfying the
  "mix >= 4 distinct models uncapped" floor. (Ticket #94's guard asserts that floor over
  the register-probe corpus and passes.)
- The re-routing is deliberately small: v0.84 changes the chosen model on only ~1.2% of
  matched prompts under the cap (22 of 1,797), so the gains here are in mix spread and
  cost per prompt, not in wholesale re-routing.

**Recommendation on `artifacts/latest`: do NOT bump yet.**

Rationale: v0.84 is strictly cheaper at equal measured accuracy, so it is a rational
candidate, but this bake-off cannot separate it from v0.82 on quality — the paired
interval spans zero in both the capped and uncapped arms. Promotion on a tie is a judgement
call that the earlier tickets explicitly left to the live bake-off gate, and the live gate
is not runnable offline. What would change the verdict:

1. Re-run with more discordant pairs — the tie-band exploration in `ROUTER_EXPLORE_*`
   (#93) reaches 5 of 8 models at band 0.10, which would produce the volume needed to
   resolve a ~2 pt difference.
2. A live bake-off on real traffic, where the served-cost and quality measurements come
   from the deployment rather than the label corpus.
3. If the operator's priority is cost per prompt at unchanged quality, bumping is
   defensible today on that single axis — say so explicitly in the promotion PR if that is
   the intent, because this report does not establish it on accuracy.

`artifacts/latest` was **not** modified (`git status` confirms only
`scripts/bakeoff_v084.py` and the new cache are uncommitted additions).

## Appendix — artifact fingerprints

| file | v0.82 sha256 | v0.84 sha256 |
|---|---|---|
| quality_means.json | `ae25ffd1…` | `8f297a44…` |
| model_axes.json | `cd94bd60…` | `cd94bd60…` (identical) |
| model_registry.json | `3a62bac9…` | `3a62bac9…` (identical) |
| centroids.bin | `57b06f95…` | `57b06f95…` (identical) |
| metadata.yaml | `eeff211e…` | `3c09baa0…` |

Known limitations of this measurement: (1) it is an offline replay on a fixed label
corpus, so it inherits the corpus's prompt mix and its coverage gaps — 288 corpus prompts
could not be routed at all and 152–291 more had no label for the chosen model; (2) the
cluster assignment differs from the committed cache on 150 prompts because the cache
predates the centroid retrain — the live ids are used and the disagreement is quantified
above; (3) the paired interval is dominated by the 98.8% same-model rate, so it cannot
resolve small quality differences; (4) cost is the label run's measured cost, which
reflects the label-run traffic mix rather than production traffic.
