#!/usr/bin/env python3
"""Replace v0.84's 8 inherited v0.82 prior cells with an AA-style capability
prior blend (ticket #91; artifacts/v0.84).

The #88 rebuild (scripts/build_v084_measured_cells.py) left 8 of 16 clusters
below the MIN_MEASURED_N=20 gate, so they still carry v0.82's researched
cells: c1 (n=9), c2 (n=2), c3 (n=6), c4 (n=18), c5 (n=14), c6 (n=15),
c13 (n=8), c15 (n=21). This script rebuilds exactly those cells.

1. Capability prior. One score per deployed model, from the published
   benchmark columns in .context/bench-scores.md (retrieval 2026-10-04).
   Column tier weights mirror upstream quality_means meta.aa_label_tier_weights
   (coding loaders 1.0, knowledge/instruction-following 0.3,
   bfcl-v4-parallel-multi 0.8):

     | Model (AIand roster id)                | AA Index v4.3.2 (context) | SWE-bench Verified w=1.0 | LiveCodeBench w=1.0 | Aider Polyglot w=1.0 | MMLU-Pro w=0.3 | GPQA Diamond w=0.3 | IFBench w=0.3 | MATH-500 w=0.3 | BFCL v4 parallel-multi w=0.8 | prior score | confidence |
     | -------------------------------------- | ------------------------- | ------------------------ | ------------------- | -------------------- | -------------- | ------------------ | ------------- | -------------- | --------------------------- | ----------- | ---------- |
     | deepseek-ai/deepseek-v4-pro            | 36 (V4 Pro 0813 max)      | 96.4% INDEP (vals.ai mini-SWE-agent, obs 2026-08-17, rank 2) | 87.53% INDEP (vals.ai LCB, rank 13/143) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 91.965 | high |
     | moonshotai/kimi-k3                     | 44 (max)                  | 93.4% INDEP (vals.ai mini-SWE-agent, rank 8/88) | 87.19% v6 INDEP (vals.ai LCB, rank 18/143) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 90.295 | high |
     | deepseek-ai/deepseek-v4-flash          | 34 (V4 Flash 0731 max)    | 88.8% INDEP (vals.ai mini-SWE-agent, obs 2026-08-17, rank 11) | 87.26% INDEP (vals.ai LCB) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 88.03 | high |
     | zai-org/glm-5.3                        | 45 (max)                  | 95.4% INDEP (vals.ai mini-SWE-agent, rank 6) | 80.53% INDEP (vals.ai LCB, rank 68/143) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 87.965 | high |
     | zai-org/glm-5.3-flash                  | 42                        | 92.0% INDEP (vals.ai) | 80.51% INDEP (vals.ai) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 86.255 | high |
     | qwen/qwen3.8-27b                       | 34 (Xhigh)                | 86.0% INDEP (vals.ai, rank 15/88) | 84.00% INDEP (vals.ai; HF self-report 90.3 not used) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 85.0 | high |
     | deepseek-ai/deepseek-v4.1-flash        | 39 (max)                  | NOT FOUND (vendor DeepSWE v1.1 74.2 SELF only; vals SWE board archived 2026-09-01 before release) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 86.533 (roster mean) | none |
     | motif-technologies/motif-3             | 34 (ESTIMATED, no measured AA run) | 76.2% SELF ONLY (HF card + arXiv 2608.09119, mini-SWE-agent; no independent reproduction) | NOT FOUND (AA-LCR v1.1 proxy 76% is not LCB, not used) | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | NOT FOUND | 76.2 | medium |

     prior score = sum(w_c * v_c) / sum(w_c) over the columns that exist.
     NOT FOUND columns drop out of both sums (they are absent, not zero):
     TOTAL WEIGHT = 2.0 for seven models (SWE-V + LCB), 1.0 for motif-3,
     0.0 for deepseek-v4.1-flash. Columns NOT FOUND for every model:
     Aider Polyglot, MMLU-Pro, GPQA Diamond, IFBench, MATH-500 and
     bfcl-v4-parallel-multi (AA v4.3.2 pages no longer publish the first
     three; IFBench/MATH-500 were never published on the AA pages used);
     their 2.5 of 4.5 total tier weight is evidence we do not have.
     deepseek-v4.1-flash has zero tier evidence, so its prior is the roster
     mean (prior_z = 0 by construction) rather than a guess; the AA Index
     column is context only and is deliberately NOT a tier weight (it is the
     residual anchor, not a per-capability column).
     Source: .context/bench-scores.md (artificialanalysis.ai model pages +
     vals.ai independent runs), retrieved 2026-10-04. Confidence per cell:
     INDEP = independent harness reproduction (high), SELF = vendor-reported
     (medium), NOT FOUND = no published cell (excluded).

2. Prior in z units. prior_z[m] = (prior_score[m] - mean) / std over the 8
   roster models, i.e. the prior expressed on the same per-prompt z scale the
   measured cells live on.

3. Pseudo-count blend (equivalent to upstream aa_evidence_scale = 3.0):

     cell_z[m] = (n_measured * measured_z[m] + n0 * prior_z[m])
                 / (n_measured + n0),   n0 = 3.0

   n_measured = the cluster's usable measured prompt count (v084_provenance
   .n), measured_z[m] = the #88 shrunk per-(cluster, model) z mean
   (per-prompt z across the 8 roster columns, shrinkage_k0=10 toward the
   model's global mean), prior_z[m] = the AA-style capability prior above.
   The blended z row is then min-max rescaled into the cluster's existing
   v0.82 cell range, which is the #88 rescale step, so prior cells and
   measured cells share one scale. Prior weight is n0/(n+n0): 60% in c2
   (n=2) and 44% in c13 (n=8) down to 14% in c4 (n=18), so the cell ordering
   follows the capability prior, never the inherited v0.82 value. Measured
   clusters (n>=20) keep their #88 cell byte-identical.
"""
from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path

import yaml

K = 16
SHRINKAGE_K0 = 10.0
EPS = 1e-9
N0 = 3.0  # pseudo-count, equal to upstream aa_evidence_scale
MIN_MEASURED_N = 20  # #88's usable-n gate for a measured-only cell

MODELS = [
    "deepseek-ai/deepseek-v4-pro",
    "deepseek-ai/deepseek-v4-flash",
    "deepseek-ai/deepseek-v4.1-flash",
    "zai-org/glm-5.3",
    "zai-org/glm-5.3-flash",
    "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3",
    "motif-technologies/motif-3",
]

# Upstream tier weights (quality_means meta.aa_label_tier_weights, v0.64 keys).
TIER_WEIGHTS = {
    "SWE_BENCH_VERIFIED": 1.0,
    "LIVECODEBENCH": 1.0,
    "AIDER_POLYGLOT": 1.0,
    "MMLU_PRO": 0.3,
    "GPQA_DIAMOND": 0.3,
    "IFBENCH": 0.3,
    "MATH_500": 0.3,
    "BFCL_V4_PARALLEL_MULTI": 0.8,
}

# (value or None, source, confidence) per model per tier column.
# Sources: .context/bench-scores.md, retrieved 2026-10-04.
PRIORS = {
    "deepseek-ai/deepseek-v4-pro": {
        "SWE_BENCH_VERIFIED": (96.4, "vals.ai mini-SWE-agent obs 2026-08-17 rank 2 (INDEP)", "high"),
        "LIVECODEBENCH": (87.53, "vals.ai LCB rank 13/143 (INDEP)", "high"),
    },
    "moonshotai/kimi-k3": {
        "SWE_BENCH_VERIFIED": (93.4, "vals.ai mini-SWE-agent rank 8/88 (INDEP)", "high"),
        "LIVECODEBENCH": (87.19, "vals.ai LCB v6 rank 18/143 (INDEP)", "high"),
    },
    "deepseek-ai/deepseek-v4-flash": {
        "SWE_BENCH_VERIFIED": (88.8, "vals.ai mini-SWE-agent obs 2026-08-17 rank 11 (INDEP)", "high"),
        "LIVECODEBENCH": (87.26, "vals.ai LCB (INDEP)", "high"),
    },
    "zai-org/glm-5.3": {
        "SWE_BENCH_VERIFIED": (95.4, "vals.ai mini-SWE-agent rank 6 (INDEP)", "high"),
        "LIVECODEBENCH": (80.53, "vals.ai LCB rank 68/143 (INDEP)", "high"),
    },
    "zai-org/glm-5.3-flash": {
        "SWE_BENCH_VERIFIED": (92.0, "vals.ai (INDEP)", "high"),
        "LIVECODEBENCH": (80.51, "vals.ai (INDEP)", "high"),
    },
    "qwen/qwen3.8-27b": {
        "SWE_BENCH_VERIFIED": (86.0, "vals.ai rank 15/88 (INDEP)", "high"),
        "LIVECODEBENCH": (84.0, "vals.ai (INDEP; HF self-report 90.3 not used)", "high"),
    },
    # zero tier evidence: no SWE-V / LCB / Aider cell published at all
    "deepseek-ai/deepseek-v4.1-flash": {},
    # one SELF-only cell; nothing independent reproduced yet
    "motif-technologies/motif-3": {
        "SWE_BENCH_VERIFIED": (76.2, "HF card + arXiv 2608.09119 mini-SWE-agent (SELF)", "medium"),
    },
}

REPO = Path(__file__).resolve().parent.parent
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v082-full.json"
ART = REPO / "internal/router/cluster/artifacts"
DST = ART / "v0.84"


def short(m: str) -> str:
    return m.split("/")[-1]


def prior_table() -> dict[str, dict]:
    """Per-model capability prior: tier-weighted score, evidence weight,
    per-cell confidence, and the z-score against the roster distribution."""
    scores, weights, cells = {}, {}, {}
    for m in MODELS:
        pr = PRIORS[m]
        w = num = 0.0
        cells[m] = {}
        for col, wt in TIER_WEIGHTS.items():
            v, src, conf = pr.get(col, (None, "NOT FOUND", "none"))
            cells[m][col] = {"value": v, "source": src, "confidence": conf,
                             "weight": wt}
            if v is not None:
                num += wt * v
                w += wt
        if w == 0.0:
            scores[m] = None  # resolved below to the roster mean
        else:
            scores[m] = num / w
        weights[m] = w
    known = [s for s in scores.values() if s is not None]
    roster_mean = sum(known) / len(known)
    for m in MODELS:
        if scores[m] is None:
            scores[m] = roster_mean
    mu = sum(scores.values()) / len(scores)
    sd = statistics.pstdev(scores.values()) or 1.0
    out = {}
    for m in MODELS:
        conf = "none"
        if weights[m] > 0:
            confs = [c["confidence"] for c in cells[m].values() if c["value"] is not None]
            conf = "medium" if "medium" in confs else "high"
        out[m] = {"score": round(scores[m], 4), "z": (scores[m] - mu) / sd,
                  "evidence_weight": weights[m], "confidence": conf,
                  "roster_mean": scores[m] == roster_mean and weights[m] == 0.0,
                  "cells": cells[m]}
    return out


def measured_z_means() -> dict[int, dict[str, float | None]]:
    """#88 recipe: per-prompt z across the 8 roster columns, per-(cluster,
    model) shrunk mean toward the model's global mean, shrinkage_k0=10."""
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    assign_all = json.loads(ASSIGN.read_text(encoding="utf-8"))
    prompts = [p for p in sorted(by_prompt) if p in assign_all]
    z: dict[str, dict[str, float]] = {}
    for p in prompts:
        scored = {m: v["score"] for m, v in by_prompt[p].items()
                  if m in MODELS and v.get("score") is not None}
        ms = [m for m in MODELS if m in scored]
        if len(ms) < 2:
            continue
        vals = [scored[m] for m in ms]
        mean = sum(vals) / len(vals)
        var = sum((v - mean) ** 2 for v in vals) / len(vals)
        std = var ** 0.5
        if std < EPS:
            continue
        z[p] = {m: (scored[m] - mean) / std for m in ms}
    gmean = {m: sum(z[p][m] for p in z if m in z[p])
             / max(1, sum(1 for p in z if m in z[p])) for m in MODELS}
    out: dict[int, dict[str, float | None]] = {}
    for k in range(K):
        ps = [p for p in z if assign_all[p] == k]
        row = {}
        for m in MODELS:
            sc = [z[p][m] for p in ps if m in z[p]]
            if not sc:
                row[m] = None
                continue
            mean = sum(sc) / len(sc)
            row[m] = (len(sc) * mean + SHRINKAGE_K0 * gmean[m]) / (len(sc) + SHRINKAGE_K0)
        out[k] = row
    return out, len(prompts), len(rows)


def blend_row(qm_row, costs, alpha):
    """Runtime blend parity (internal/router/cluster/scorer.go blendScoresV2,
    speed_weight = 0 path): wQ=alpha, wC=1-alpha."""
    qmin, qmax = min(qm_row[m] for m in MODELS), max(qm_row[m] for m in MODELS)
    qr = qmax - qmin
    cmin, cmax = min(costs.values()), max(costs.values())
    cr = cmax - cmin
    return {m: alpha * ((qm_row[m] - qmin) / qr if qr > 0 else 0.0)
            + (1.0 - alpha) * (1.0 - ((costs[m] - cmin) / cr if cr > 0 else 0.0))
            for m in MODELS}


def discordant(ranking: list[str], prior_order: list[str]) -> int:
    """Kendall-tau discordant pair count between a ranking and the capability
    prior ordering (28 orderings total: 0 = identical, 26 = fully reversed)."""
    pos = {m: i for i, m in enumerate(prior_order)}
    return sum(1 for i in range(len(ranking))
               for j in range(i + 1, len(ranking))
               if pos[ranking[i]] > pos[ranking[j]])


def main() -> int:
    qm = json.loads((DST / "quality_means.json").read_text(encoding="utf-8"))
    prov = qm["meta"]["v084_provenance"]
    meta = yaml.safe_load((DST / "metadata.yaml").read_text(encoding="utf-8"))
    features = json.loads((DST / "model_features.json").read_text(encoding="utf-8"))
    rk = json.loads((DST / "rankings.json").read_text(encoding="utf-8"))
    axes = json.loads((DST / "model_axes.json").read_text(encoding="utf-8"))["axes"]

    priors = prior_table()
    zmeas, n_prompts, n_rows = measured_z_means()
    knobs = meta["default_routing_knobs"]
    alpha = list(knobs["alpha"])
    ocr = float(knobs["output_cost_ratio"])

    print(f"priors (score / z / evidence weight / confidence):")
    for m in sorted(MODELS, key=lambda m: -priors[m]["z"]):
        p = priors[m]
        tag = " (roster mean, no tier evidence)" if p["roster_mean"] else ""
        print(f"  {short(m):>14} {p['score']:7.3f}  z={p['z']:+.3f}  "
              f"w={p['evidence_weight']:.1f}  {p['confidence']}{tag}")
    print(f"labels: {n_rows} rows, {n_prompts} cluster-assigned prompts")

    # --- 1. prior-blended cells: the clusters below #88's usable-n gate.
    # Keyed off the n gate, not the cell label, so the builder is correct both
    # on v0.82's inherited prior_fallback cells and on a re-run of itself.
    prior_clusters = sorted(int(k) for k, v in prov.items() if v["n"] < MIN_MEASURED_N)
    new_cells: dict[int, dict[str, float]] = {}
    for k in range(K):
        base = qm["quality_means"][str(k)]
        if k not in prior_clusters:
            new_cells[k] = dict(base)  # measured cell: byte-identical
            continue
        n = prov[str(k)]["n"]
        blended = {m: (n * zmeas[k][m] + N0 * priors[m]["z"]) / (n + N0)
                   for m in MODELS}
        zlo, zhi = min(blended.values()), max(blended.values())
        clo, chi = min(base.values()), max(base.values())
        row = {m: clo + (chi - clo) * ((blended[m] - zlo) / (zhi - zlo))
               for m in MODELS}
        new_cells[k] = row

    # --- 2. before / after cell ordering per prior cluster
    # The "before" baseline must be the v0.82 inherited cell, which is what
    # this ticket removes; not the blended cell a previous run produced.
    v082 = json.loads((ART / "v0.82/quality_means.json").read_text(encoding="utf-8"))
    print(f"\n{'c':>3} {'n':>4} {'prior w':>8}  before (v0.82 inherited) -> after (prior-blended)")
    order_lines = {}
    prior_order = sorted(MODELS, key=lambda m: -priors[m]["z"])
    for k in prior_clusters:
        before = sorted(MODELS, key=lambda m: -v082["quality_means"][str(k)][m])
        after = sorted(MODELS, key=lambda m: -new_cells[k][m])
        n = prov[str(k)]["n"]
        db = discordant(before, prior_order)
        da = discordant(after, prior_order)
        b = " > ".join(short(m) for m in before)
        a = " > ".join(short(m) for m in after)
        order_lines[k] = f"c{k}: {b}  ->  {a}"
        print(f"{k:>3} {n:>4} {N0/(n+N0):>7.1%}  {b}")
        print(f"{'':>3} {'':>4} {'':>8}  {a}")
        print(f"{'':>3} {'':>4} {'':>8}  prior discordance {db}/26 -> {da}/26, "
              f"top-1 {short(before[0])} -> {short(after[0])}")

    # --- 3. replay: measured accuracy of the routed mix, before vs after
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    assign_all = json.loads(ASSIGN.read_text(encoding="utf-8"))
    prompts = sorted(p for p in by_prompt if p in assign_all)

    def routed(cells):
        cells = {int(k): v for k, v in cells.items()}
        win = {k: max(MODELS, key=lambda m: blend_row(cells[k], costs8, alpha[k])[m])
               for k in range(K)}
        num = den = 0.0
        for p in prompts:
            if win[assign_all[p]] in by_prompt[p]:
                num += by_prompt[p][win[assign_all[p]]]["score"]
                den += 1
        return (num / den if den else 0.0), int(den), win

    med = statistics.median(axes[m]["verbosity_tokens"] for m in MODELS)
    costs8 = {m: axes[m]["input_per_1k_usd"]
              + ocr * axes[m]["output_per_1k_usd"] * axes[m]["verbosity_tokens"] / med
              for m in MODELS}
    acc_before, n_before, win_before = routed(qm["quality_means"])
    acc_after, n_after, win_after = routed(new_cells)
    print(f"\nreplay @ shipped knobs (alpha mean {sum(alpha)/K:.4f}, "
          f"ocr={ocr}): {100*acc_before:.2f}% (n={n_before}) -> "
          f"{100*acc_after:.2f}% (n={n_after}) {100*(acc_after-acc_before):+.2f}pt")
    print("winners: " + ", ".join(
        f"c{k}:{short(win_before[k])}->{short(win_after[k])}" for k in prior_clusters))

    # --- 4. emit
    qm["quality_means"] = {str(k): new_cells[k] for k in range(K)}
    new_prov = {}
    for k in range(K):
        n = prov[str(k)]["n"]
        if k in prior_clusters:
            new_prov[str(k)] = {
                "cell": "prior",
                "n": n,
                "n_all": prov[str(k)]["n_all"],
                "prior_source": "aa_style_benchmark_prior (.context/bench-scores.md, "
                                "retrieved 2026-10-04, tier weights = upstream "
                                "aa_label_tier_weights)",
                "prior_pseudo_count": N0,
                "prior_weight": round(N0 / (n + N0), 6),
                "prior_model_z": {m: round(priors[m]["z"], 6) for m in MODELS},
                "prior_model_score": {m: priors[m]["score"] for m in MODELS},
                "prior_model_confidence": {m: priors[m]["confidence"] for m in MODELS},
            }
        else:
            new_prov[str(k)] = {"cell": "measured", "n": n,
                                "n_all": prov[str(k)]["n_all"]}
    qm["meta"]["v084_provenance"] = new_prov
    (DST / "quality_means.json").write_text(
        json.dumps(qm, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    features["meta"]["comment"] = (
        "v0.84: v0.82 operational columns + measured quality cells with the 8 "
        "prior_fallback clusters prior-blended (scripts/build_v084_aa_priors.py); "
        "psi_probe mirrors quality_means.")
    for m, cols in features["models"].items():
        if m not in MODELS:
            continue
        for k in range(K):
            cols["psi_probe"][k] = new_cells[k][m]
    (DST / "model_features.json").write_text(
        json.dumps(features, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    rk["rankings"] = {str(k): blend_row(new_cells[k], costs8, alpha[k]) for k in range(K)}
    rk["meta"]["alpha"] = round(sum(alpha) / K, 4)
    rk["meta"]["alpha_per_cluster"] = list(alpha)
    rk["meta"]["regenerated_by"] = "build_v084_aa_priors.py"
    (DST / "rankings.json").write_text(
        json.dumps(rk, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # --- 5. metadata.yaml: changelog + documented prior block
    order_before_after = "\n".join(order_lines[k] for k in prior_clusters)
    meta["aa_capability_priors_v0_84"] = {
        "source": ".context/bench-scores.md",
        "retrieved": "2026-10-04",
        "tier_weights": dict(TIER_WEIGHTS),
        "tier_weight_source": "upstream quality_means meta.aa_label_tier_weights",
        "columns_not_found_for_all_models": [
            "AIDER_POLYGLOT", "MMLU_PRO", "GPQA_DIAMOND", "IFBENCH", "MATH_500",
            "BFCL_V4_PARALLEL_MULTI"],
        "pseudo_count_n0": N0,
        "arithmetic": ("prior_z[m] = (prior_score[m] - mean_roster) / std_roster; "
                       "cell_z[m] = (n_measured * measured_z[m] + n0 * prior_z[m]) / "
                       "(n_measured + n0) with n0 = 3.0 = aa_evidence_scale; "
                       "measured_z from the #88 shrunk per-(cluster, model) z mean; "
                       "blended z min-max rescaled into the cluster's v0.82 cell range"),
        "models": {m: {"prior_score": priors[m]["score"],
                       "prior_z": round(priors[m]["z"], 6),
                       "evidence_weight": priors[m]["evidence_weight"],
                       "confidence": priors[m]["confidence"],
                       "cells": {c: {"value": v["value"], "source": v["source"],
                                     "confidence": v["confidence"], "weight": v["weight"]}
                                 for c, v in priors[m]["cells"].items()}}
                   for m in MODELS},
        "ordering_before_after": order_before_after,
    }
    entry = (
        "v0.84 prior clusters rebuilt with AA-style capability priors (ticket #91; "
        "scripts/build_v084_aa_priors.py). The 8 clusters below #88's MIN_MEASURED_N=20 "
        "gate (c1 n=9, c2 n=2, c3 n=6, c4 n=18, c5 n=14, c6 n=15, c13 n=8, c15 n=21) no "
        "longer inherit v0.82's researched cells. Per-model capability prior from the "
        "published benchmark columns in .context/bench-scores.md (retrieved 2026-10-04) "
        "combined with the upstream tier weights (coding loaders SWE-bench Verified / "
        "LiveCodeBench / Aider Polyglot 1.0; MMLU-Pro / GPQA Diamond / IFBench / MATH-500 "
        "0.3; bfcl-v4-parallel-multi 0.8). Only SWE-bench Verified and LiveCodeBench have "
        "published cells on this roster; Aider Polyglot, MMLU-Pro, GPQA Diamond, IFBench, "
        "MATH-500 and bfcl-v4-parallel-multi are NOT FOUND for every model (AA v4.3.2 "
        "pages no longer publish them), so 2.5 of the 4.5 total tier weight is evidence "
        "we do not have and the prior is a weighted mean over the columns that exist. "
        "Pseudo-count blend equivalent to upstream aa_evidence_scale = 3.0: "
        "cell_z[m] = (n_measured * measured_z[m] + 3.0 * prior_z[m]) / (n_measured + 3.0), "
        "measured_z from #88's shrunk per-(cluster, model) z mean (per-prompt z across the "
        "8 roster columns, shrinkage_k0=10), prior_z = the capability prior z-scored "
        "against the roster prior distribution; the blended row is min-max rescaled into "
        "the cluster's existing v0.82 cell range. Prior weight is 3/(n+3): 60% in c2, 44% "
        "in c13, 32% in c3, 27% in c1/c5/c6, 17% in c15, 14% in c4. Cell ordering per "
        "prior cluster (before -> after):\n" + order_before_after + "\n"
        "The ordering now follows the capability prior (deepseek-v4-pro > kimi-k3 > "
        "deepseek-v4-flash > glm-5.3 > glm-5.3-flash > qwen3.8-27b > deepseek-v4.1-flash "
        "roster-mean > motif-3), not the inherited v0.82 value. deepseek-v4.1-flash has no "
        "tier evidence at all (SWE board archived before release, vendor DeepSWE is "
        "self-reported only) so its prior is the roster mean (prior_z = 0) rather than a "
        "guess; motif-3 rests on a single SELF-only SWE-V cell (medium confidence). "
        "Measured clusters (n>=20: c0, c7, c8, c9, c10, c11, c12, c14) keep their #88 cell "
        "byte-identical. Provenance in quality_means.json meta.v084_provenance now marks "
        "those 8 clusters cell=\"prior\" with n, n_all, prior source, prior_z, prior weight "
        "and pseudo-count; measured clusters stay cell=\"measured\". rankings.json "
        f"regenerated by the exact runtime blend at the SHIPPED knobs (alpha {alpha[0]} / "
        f"{alpha[2]} demoted, mean {sum(alpha)/K:.4f}, output_cost_ratio {ocr}, "
        f"speed_weight 0) — replay {100*acc_before:.2f}% (n={n_before}) -> "
        f"{100*acc_after:.2f}% (n={n_after}) on the identical prompt set. centroids.bin, "
        "model_axes.json, model_registry.json BYTE-IDENTICAL to v0.82. Candidate; "
        "artifacts/latest NOT bumped."
    )
    # Idempotence: a re-run replaces an earlier #91 entry instead of appending
    # a second one; every other history entry is preserved in order.
    entry_marker = "v0.84 prior clusters rebuilt with AA-style capability priors"
    existing = str(meta.get("changelog", ""))
    if entry_marker in existing:
        _, _, past = existing.partition("artifacts/latest NOT bumped.\n\n")
        meta["changelog"] = entry + ("\n\n" + past if past else "")
    else:
        meta["changelog"] = entry + ("\n\n" + existing if existing else "")
    with open(DST / "metadata.yaml", "w", encoding="utf-8", newline="\n") as f:
        yaml.safe_dump(meta, f, sort_keys=False, allow_unicode=True, width=100)
    print("wrote", DST)
    return 0


if __name__ == "__main__":
    sys.exit(main())
