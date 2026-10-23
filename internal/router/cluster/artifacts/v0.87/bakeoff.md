# v0.86 — the coding lane on v0.84's measured cells

Candidate: `internal/router/cluster/artifacts/v0.86`
Parent: **v0.84** (geometry byte-identical; quality cells measured, plus a
range-relative operator offset on the coding lane)
Builder: `scripts/build_v086_coding_lane.py`

## 1. What this bundle is

v0.86 = v0.84's measured quality cells, plus a **range-relative cell offset**
on `moonshotai/kimi-k3` in the coding-heavy clusters `c5`, `c8`, `c10`, `c12`,
`c14`. Nothing else changes: `centroids.bin`, `model_axes.json`,
`model_registry.json` are byte-identical to v0.84, and every cell outside the
five offset clusters keeps its measured value.

The measured cells survive under `meta.operator_override.measured_cells_preserved`
in `quality_means.json`, with the rationale and the reversal instructions
beside them, so the bias stays attributable and reversible.

## 2. Why v0.84 alone was not enough

v0.84 is the cheapest bundle on the labelled corpus, but that accuracy edge is
**in-sample**: its cells were fitted to
`.context/label-runs/v082-expanded-labels.jsonl`. The corpus composition
(determined by reading the `dataset` field of every row):

| dataset | rows |
|---|---|
| BFCLv4 (+ BFCLv4H) | 4,634 |
| MMLU-Pro | 2,101 |
| LiveCodeBench | 1,705 |
| LCBv5 | 1,525 |
| AIDER | 150 prompts |
| OpenTDB / QANTA / PubMedQA / NarrativeQA / GeoBench / MedMCQA / Ethics | ~1,700 |

Per model, the AIDER coverage of the corpus is: `deepseek-v4-flash` 150,
`deepseek-v4-pro` 150, `motif-3` 150, `deepseek-v4.1-flash` 132,
`qwen3.8-27b` 71, `glm-5.3-flash` 49, `glm-5.3` 33, **`kimi-k3` 10**.

So the strongest measured lane is effectively unmeasured in the cells that
decide coding turns. Measured over the cells themselves, `kimi-k3` is elected
by **0 of 16** clusters in v0.84 (v0.83 elects it in 3), `motif-3` in **0 of
16** (v0.83: 3), and the served mix collapses to 4 of 8 models.

**No configuration reaches it.** A cost cap, an `alpha`, a
`ROUTER_CLUSTER_COST_CAP_MODE`, or a `PreferredModels` bonus all operate on the
blend, and the blend starts from the cell. Measured directly:

- widening the cap from 3.0 to 10.0 or removing it changes v0.84's mix not at
  all (the cap is not what excludes the lane);
- `alpha` 0.90 / 0.96 / 0.99 / 1.0 changes v0.84's mix not at all;
- a `PreferredModels` bonus on `kimi-k3` (rank 0, 0.15 blend units) and on
  `motif-3` changes v0.84's mix not at all.

The cell is the input. Fixing it needs a new bundle.

## 3. The held-out measurement

`aider-fair-lanes-grades.jsonl` (5 lanes) and `aider-glm-effort-graded.jsonl`
(2 GLM lanes) were produced **after** the corpus the v0.84 cells were fitted
to, so they are held out with respect to those cells.

Comparisons are made on the **fixed subset**: the AIDER prompts where every
graded lane has a grade (89 of 150). A config that routes to a lane with
sparse coverage would otherwise be scored on a different, easier prompt set
— `kimi-k3` has grades for only 101 of 150 AIDER cells, so a kimi-heavy config
loses exactly the prompts kimi could not answer, which inflates its apparent
accuracy. Fixing the subset removes that confound.

## 4. Result, measured through the production scorer

The comparison runs through the real scorer (`NewScorer` + `Route`), not a
Python port of it. The port disagreed with the Go implementation twice during
this work — once on the artifact loader and once on a double-applied cell
offset — so the port was discarded and the harness
(`internal/router/cluster/corpus_replay_harness_test.go`) was written against
the production code path.

| bundle | corpus accuracy | cost/prompt | held-out AIDER coding | models reached |
|---|---|---|---|---|
| **v0.86** | 73.88% | **$0.001363** | **42.86%** | 5 |
| v0.84 | 73.93% | $0.001004 | 35.90% | 4 |
| v0.83 | 69.98% | $0.003744 | 35.14% | 7 |
| v0.85 | 67.52% | $0.002937 | 22.76% | 8 |

Cost is honest: every routed prompt contributes a cost (the cell's measured
cost when labelled, else the model's median measured cost).

v0.86 matches v0.84 on the corpus, beats both shipped bundles on held-out
coding, and costs **36% of v0.83**.

## 5. Split-half validation

The offset magnitude and the cluster set were chosen by looking at the
held-out grades, so the reported gain is validated on a half that was not used
for the choice. The fixed subset is split by index parity: the offset is
chosen on the SELECT half and reported on the VALID half.

| candidate | half | n | paired delta | W/L |
|---|---|---|---|---|
| c5c8c10c12c14 @ 1.0 | SELECT | 42 | +7.14pt | 3/0 |
| c5c8c10c12c14 @ 1.0 | **VALID** | 39 | **+7.69pt** | 3/0 |
| c5c10c12 @ 1.25 | SELECT | 42 | +7.14pt | 3/0 |
| c5c10c12 @ 1.25 | VALID | 36 | +5.56pt | 2/0 |

The gain holds on the half that was not used for the choice.

## 6. Why these clusters, and why not c0 or c9

The register-probe composition per cluster (from
`TestClusterComposition`, which reports the same top-P neighbourhood Route
blends over) shows which clusters chit-chat lands in:

| cluster | probes in neighbourhood | chit-chat | coding |
|---|---|---|---|
| c0 | 84 | 16.7% | 63.1% |
| c1 | 29 | 20.7% | 65.5% |
| c2 | 14 | 78.6% | 21.4% |
| c4 | 41 | 70.7% | 14.6% |
| **c5** | 74 | 10.8% | 68.9% |
| c8 | 57 | 10.5% | 70.2% |
| c9 | 61 | 73.8% | 19.7% |
| c10 | 66 | 45.5% | 30.3% |
| c12 | 49 | 16.3% | 59.2% |
| c14 | 21 | 4.8% | 47.6% |

`c0` and `c9` are excluded: offsetting a chit-chat-dominated cluster routes
conversational and trivial_nl turns onto the premium lane. Measured through
the real scorer, `c0` in the set puts 4 of the 180 register probes on the
premium lane; `c9` puts 5 to 12. With the shipped set the count is **0**,
which is what the register mix guard requires.

## 7. The traps this bundle hit

Both are recorded because they are silent: the bundle loads, the tests pass
on the wrong numbers, and the override simply does nothing.

1. **`model_features.json` overrides `quality_means.json` at load time.**
   `artifacts.go` reads the features file after the quality cells and
   replaces them. v0.84 ships one, so patching `quality_means.json` alone
   produces a bundle whose override is **inert at runtime** — the blend never
   sees it. The builder patches both files with the same delta, because the
   two carry the same cells (`TestFeaturesMatchQualityMeans` holds them
   equal). v0.85 did not hit this: it was built from v0.83, which ships no
   features file.

2. **The offset span must be computed from the original row.** Deriving the
   span from an already-patched row re-derives it from a moved maximum and
   applies a larger delta than the one measured — which produced a bundle
   whose features file and quality file disagreed, and a mix that differed
   from the offline prediction. The builder computes one delta per
   (cluster, model) from the original cells and applies it to both files.

## 8. Reversal

Delete the `meta.operator_override` block in `quality_means.json` and the
corresponding additive offsets in both `quality_means.json` and
`model_features.json`, or rebuild from measured labels via
`scripts/build_v084_measured_cells.py` plus a fresh fair-label campaign that
covers the coding lanes.

## 9. Reproducing

```
go test ./internal/router/cluster/ -run TestCorpusReplay -v      # per-bundle replay
go test ./internal/router/cluster/ -run TestCandidateSweep -v    # offset x cluster set
go test ./internal/router/cluster/ -run TestClusterComposition -v # per-cluster register mix
go test ./internal/router/cluster/ -run TestCandidate -v          # the gates
```

The corpus and the held-out grades live under `.context/`, which is
gitignored; the harness skips when they are absent.
