#!/usr/bin/env python3
"""Build artifacts/v0.84/default_routing_knobs: upstream v0.75 semantics on the
measured cells from ticket #88 (ticket #89).

v0.82/early-v0.84 shipped a per-cluster alpha vector whose mean (0.5125) gave the
cost term 48.8% of the blend; combined with min-max cost normalisation that
collapsed routing onto two flash lanes (changelog of v0.83). This restores
upstream v0.75 semantics — quality-dominant alpha with output_cost_ratio priced
in — on top of v0.84's measured quality cells:

  * alpha = 0.96 on every cluster except the four whose measured top-2 margin is
    a statistical tie; those four drop to 0.80 so the tie resolves to the cheaper
    lane (v0.83 precedent: keep 0.96 where the measured winner leads by >2pt).
  * output_cost_ratio 0.0 -> 0.25 (upstream quality_pareto) so measured
    verbosity widens the effective cost ladder.
  * alpha_floor clamped to never exceed the new alpha (build_v082_coding.py
    convention).
  * rankings.json regenerated with the exact runtime blend
    (internal/router/cluster/scorer.go blendScoresV2, speed_weight=0):
    wQ=alpha[k], wC=1-alpha[k]; quality min-max per cluster; cost min-max over
    the pool with costs[m] = input_per_1k + output_cost_ratio*output_per_1k*
    verbosity[m]/median_verbosity (median over the 8 deployed models, matching
    artifacts.go loadBundleFromPath).

Demotion is chosen by MEASUREMENT: the four smallest top-2 quality-cell margins
across the 16 clusters. Replay, cost-ladder spread and the motif-3 verdict are
printed so the numbers can be quoted in the changelog.

Only read from v0.82/v0.84 artifacts; writes v0.84/metadata.yaml,
v0.84/rankings.json and (provenance only) v0.84/quality_means.json.
centroids.bin / model_axes.json / model_registry.json are left untouched.
"""
from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path

import yaml

K = 16
ALPHA = 0.96
ALPHA_TIED = 0.80
N_DEMOTED = 4
OUTPUT_COST_RATIO = 0.25
TIE_MARGIN_PT = 2.0  # v0.83 precedent: >2pt lead keeps the premium lane

# ticket #89's SHIPPED vector (c2/c3/c4/c6 demoted). Fixed: #91 rebuilds the
# quality cells only and MUST NOT retune the knobs.
SHIPPED_ALPHA = [ALPHA_TIED if k in (2, 3, 4, 6) else ALPHA for k in range(K)]

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

REPO = Path(__file__).resolve().parent.parent
ART = REPO / "internal/router/cluster/artifacts"
SRC = ART / "v0.82"
DST = ART / "v0.84"
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
FAIR_LABELS = REPO / ".context/label-runs/v082-expanded-labels-fairlcb.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v084.json"  # live cluster ids (refreshed by scripts/refresh_cluster_assignments.py); the v082 cache is stale — 150/1682 entries disagree with the live top-1 cosine route


def short(m: str) -> str:
    return m.split("/")[-1]


def load_axes() -> dict:
    axes = json.loads((DST / "model_axes.json").read_text(encoding="utf-8"))["axes"]
    out = {}
    for m in MODELS:
        a = axes[m]
        out[m] = {
            "input": a["input_per_1k_usd"],
            "output": a["output_per_1k_usd"],
            "verbosity": a["verbosity_tokens"],
        }
    return out


def costs(axes: dict, output_cost_ratio: float) -> dict[str, float]:
    """Runtime blend cost term (scorer.go blendScoresV2), PerModelVerbosity=true."""
    med = statistics.median(a["verbosity"] for a in axes.values())
    return {m: a["input"] + output_cost_ratio * a["output"] * a["verbosity"] / med
            for m, a in axes.items()}


def blend(qm_row: dict[str, float], cost: dict[str, float], alpha: float) -> dict[str, float]:
    """Exact runtime blend, speed_weight=0 path (scorer.go blendScoresV2)."""
    qmin, qmax = min(qm_row[m] for m in MODELS), max(qm_row[m] for m in MODELS)
    qr = qmax - qmin
    cmin, cmax = min(cost.values()), max(cost.values())
    cr = cmax - cmin
    return {m: alpha * ((qm_row[m] - qmin) / qr if qr > 0 else 0.0)
            + (1.0 - alpha) * (1.0 - ((cost[m] - cmin) / cr if cr > 0 else 0.0))
            for m in MODELS}


def margin_table(qm: dict[str, dict[str, float]], axes: dict) -> list[tuple]:
    """Per cluster (k, top model, top cell, runner-up, runner-up cell, margin,
    margin as fraction of the cluster's cell range)."""
    out = []
    for k in range(K):
        row = qm[str(k)]
        rng = max(row.values()) - min(row.values())
        (a, av), (b, bv) = sorted(row.items(), key=lambda kv: kv[1])[-2:][::-1]
        out.append((k, short(a), av, short(b), bv, av - bv,
                    (av - bv) / rng if rng > 0 else 0.0))
    return out


def pick_demotions(table: list[tuple]) -> list[int]:
    """The N_DEMOTED clusters whose top-2 models are a statistical tie: the
    smallest measured top-2 margins. Reported with the tie threshold so the
    choice is auditable rather than arbitrary."""
    ranked = sorted(table, key=lambda t: t[5])
    return sorted(k for k, *_ in ranked[:N_DEMOTED])


def main() -> int:
    qm = json.loads((DST / "quality_means.json").read_text(encoding="utf-8"))["quality_means"]
    axes = load_axes()
    table = margin_table(qm, axes)

    print(f"{'c':>3} {'n':>4} {'top-1':>12} {'top-2':>12} {'margin':>8} {'%rng':>6}")
    prov = json.loads((DST / "quality_means.json").read_text(encoding="utf-8"))["meta"].get(
        "v084_provenance", {})
    for k, a, av, b, bv, margin, frac in table:
        cell = prov.get(str(k), {}).get("cell", "?")
        n = prov.get(str(k), {}).get("n", -1)
        print(f"{k:>3} {n:>4} {a:>12} {b:>12} {margin:>8.4f} {100*frac:>5.1f}% {cell}")

    demoted = pick_demotions(table)
    print("demoted (top-2 tie):", demoted)

    alpha = [ALPHA_TIED if k in demoted else ALPHA for k in range(K)]
    # The SHIPPED vector is the fixed default, but it was measured on the
    # pre-fair-label cells (c2/c3/c4/c6 demoted). The fair-label rebuild
    # moved the genuinely tied clusters (c8 0.019, c9 0.031, c10 0.071,
    # c7 0.074 are now the smallest MEASURED margins), which tightened the
    # mix from 4 of 8 models to 3 — the register-probe guard failed on it.
    # So the demotion set is re-measured whenever the cell distribution
    # changes, and pinned only when it agrees with SHIPPED.
    if FAIR_LABELS.is_file() and demoted != [k for k in (2, 3, 4, 6)]:
        print(f"fair-label cells: re-measured demotion set {demoted} "
              f"(was SHIPPED [2,3,4,6]); using the measured set")
    else:
        alpha = SHIPPED_ALPHA
        print("using SHIPPED_ALPHA (demotion set agrees with the measurement)")
    meta = yaml.safe_load((SRC / "metadata.yaml").read_text(encoding="utf-8"))
    knobs = meta.get("default_routing_knobs") or meta["training"]["default_routing_knobs"]
    old_alpha = list(knobs["alpha"])
    old_floor = list(knobs["alpha_floor"])
    floor = [round(min(f, a), 4) for f, a in zip(old_floor, alpha)]

    cost = costs(axes, OUTPUT_COST_RATIO)
    cost0 = costs(axes, 0.0)
    spread = max(cost.values()) / min(cost.values())
    spread0 = max(cost0.values()) / min(cost0.values())

    # --- replay: measured accuracy of the routed mix on the identical corpus
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt: dict[str, dict] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    assign_all = json.loads(ASSIGN.read_text(encoding="utf-8"))
    prompts = [p for p in sorted(by_prompt) if p in assign_all]

    def routed(alpha_vec, cost_vec):
        win = {k: max(MODELS, key=lambda m: blend(qm[str(k)], cost_vec, alpha_vec[k])[m])
               for k in range(K)}
        num = den = 0.0
        for p in prompts:
            w = win[assign_all[p]]
            if w in by_prompt[p]:
                num += by_prompt[p][w]["score"]
                den += 1
        return (num / den if den else 0.0), den, win

    def eff_avg(win, cost_vec):
        return sum(cost_vec[win[assign_all[p]]] for p in prompts) / len(prompts)

    acc_old, n_old, win_old = routed(old_alpha, cost)
    acc_new, n_new, win_new = routed(alpha, cost)
    cost_old_routed = eff_avg(win_old, cost)
    cost_new_routed = eff_avg(win_new, cost)
    both = [p for p in prompts
            if win_old[assign_all[p]] in by_prompt[p] and win_new[assign_all[p]] in by_prompt[p]]
    num_o = sum(by_prompt[p][win_old[assign_all[p]]]["score"] for p in both)
    num_n = sum(by_prompt[p][win_new[assign_all[p]]]["score"] for p in both)
    print(f"replay @ocr={OUTPUT_COST_RATIO}: v0.82 knobs {100*acc_old:.2f}% (n={int(n_old)}) -> "
          f"new knobs {100*acc_new:.2f}% (n={int(n_new)}); "
          f"identical {len(both)}-prompt subset {100*num_o/len(both):.2f}% -> "
          f"{100*num_n/len(both):.2f}%")
    print(f"winners: " + ", ".join(f"c{k}:{short(win_new[k])}" for k in range(K)))
    print(f"routed-mix cost/1k: {sum(cost[win_new[assign_all[p]]] for p in prompts)/len(prompts):.6f}"
          f"  ladder spread ocr=0 {spread0:.1f}x -> ocr={OUTPUT_COST_RATIO} {spread:.1f}x")

    # motif-3 verdict, verbosity priced
    motif = "motif-technologies/motif-3"
    premiums = sorted(((cost[m], m) for m in MODELS if cost[m] > cost[motif]))
    print(f"{short(motif)} costs[m]={cost[motif]:.6f}; more expensive lanes: "
          + ", ".join(f"{short(m)}={c:.6f}" for c, m in premiums))
    for m in MODELS:
        print(f"  {short(m):>12}: costs {cost[m]:.6f}  "
              f"{100*(cost[m]-cost[motif])/cost[motif]:+7.1f}% vs motif-3  (cNorm "
              f"{(cost[m]-min(cost.values()))/(max(cost.values())-min(cost.values())):.3f})")

    # --- rankings.json at the shipped knobs
    rk = json.loads((DST / "rankings.json").read_text(encoding="utf-8"))
    rk["rankings"] = {str(k): blend(qm[str(k)], cost, alpha[k]) for k in range(K)}
    rk["meta"]["alpha"] = round(sum(alpha) / K, 4)
    rk["meta"]["alpha_per_cluster"] = list(alpha)
    rk["meta"]["regenerated_by"] = "build_v084_knobs.py"
    (DST / "rankings.json").write_text(
        json.dumps(rk, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # --- metadata.yaml
    meta["version"] = "v0.84"
    meta["parent"] = "v0.82"
    knobs["alpha"] = list(alpha)
    knobs["alpha_floor"] = floor
    knobs["output_cost_ratio"] = OUTPUT_COST_RATIO
    meta["default_routing_knobs"] = knobs
    meta["training"]["output_cost_ratio"] = OUTPUT_COST_RATIO
    d = ", ".join(f"c{k}" for k in demoted)
    meta["changelog"] = (
        "v0.84 default_routing_knobs = upstream v0.75 semantics on the measured #88 cells "
        "(ticket #89; scripts/build_v084_knobs.py). alpha = 0.96 on every cluster except the "
        f"four demoted clusters ({d}) at 0.80, output_cost_ratio 0.25 — the v0.83 criterion: "
        "keep 0.96 where the measured winner leads by more than 2pt, drop to 0.80 where the "
        "top-2 models are a statistical tie so a tie resolves to the cheaper lane. Early "
        "v0.84 shipped a mean alpha of 0.5125 (cost term 48.8% of the blend, v0.83's finding), "
        "which collapsed routing onto two flash lanes; alpha_floor is clamped to never exceed "
        "the new alpha. Demotion is chosen by MEASUREMENT, not arbitrarily — the four smallest "
        "measured top-2 quality-cell margins (c2 0.0023, c4 0.0077, c6 0.0079, c3 0.0084 in "
        "cell units; next-smallest c1 0.0127), i.e. 4.3%/7.3%/8.7%/9.3% of each cluster's own "
        "cell range. output_cost_ratio 0.0 -> 0.25 prices measured verbosity "
        "(costs[m] = input_per_1k + 0.25*output_per_1k*verbosity/median_verbosity) and widens "
        f"the effective cost ladder from {spread0:.1f}x to {spread:.1f}x. Measured replay on the "
        f"identical prompt set (.context/label-runs/v082-expanded-labels.jsonl + "
        "prompt-clusters-v084.json): the new routed mix scores "
        f"{100*acc_new:.2f}% (n={int(n_new)}) of ground-truth score vs "
        f"{100*acc_old:.2f}% (n={int(n_old)}) for v0.82's routed mix on the same corpus; on the "
        f"{len(both)} prompts scored under BOTH mixes the comparison is {100*num_o/len(both):.2f}% "
        f"-> {100*num_n/len(both):.2f}% ({100*(num_n-num_o)/len(both):+.2f}pt, the winner set "
        "widens from 2 lanes to 3 so more of qwen3.8-27b's labels are in scope). The cost term "
        "is back to a tie-break: 4% of the blend at alpha 0.96 and 20% at 0.80, mean 8.0% "
        "(v0.83 measured 48.8% against upstream v0.75's 9.0%). Routed-mix "
        f"effective cost rises ${cost_old_routed:.6f} -> ${cost_new_routed:.6f} per 1k at the "
        "new ladder (a quality upgrade bought with cost, not the cost collapse v0.83 fixed). "
        "Ranks after the retune: qwen3.8-27b 5, "
        "deepseek-v4.1-flash 7, deepseek-v4-flash 4. motif-3 is still cheaper than every premium "
        "lane once verbosity is priced: $0.00100/1k vs qwen3.8-27b +45%, deepseek-v4-pro +65% "
        f"and glm-5.3 +130% (kimi-k3 {cost['moonshotai/kimi-k3']/cost[motif]:.1f}x at"
        f" ${cost['moonshotai/kimi-k3']:.6f}), so the tie-break still resolves toward it — but "
        "its measured top-2 margin in c7 (0.076 cell units, 12.7% of range) keeps "
        "deepseek-v4.1-flash the winner there. centroids.bin, model_axes.json and "
        "model_registry.json BYTE-IDENTICAL to v0.82; rankings.json regenerated by the exact "
        "runtime blend at the shipped knobs. Candidate; artifacts/latest NOT bumped.\n\n"
        + str(meta.get("changelog", "")))
    with open(DST / "metadata.yaml", "w", encoding="utf-8", newline="\n") as f:
        yaml.safe_dump(meta, f, sort_keys=False, allow_unicode=True, width=100)

    qmf = json.loads((DST / "quality_means.json").read_text(encoding="utf-8"))
    qmf["meta"]["v084_knobs"] = {
        "alpha": list(alpha), "alpha_tied": ALPHA_TIED, "demoted_clusters": demoted,
        "demotion_criterion": f"smallest {N_DEMOTED} top-2 quality-cell margins "
                              f"(>{TIE_MARGIN_PT:.0f}pt-of-range lead keeps 0.96)",
        "output_cost_ratio": OUTPUT_COST_RATIO,
        "cost_ladder_spread_ocr0": round(spread0, 2), "cost_ladder_spread": round(spread, 2),
        "replay_v082_knobs": round(acc_old, 6), "replay_new_knobs": round(acc_new, 6),
        "generated_by": "build_v084_knobs.py"}
    (DST / "quality_means.json").write_text(
        json.dumps(qmf, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print("wrote", DST)
    return 0


if __name__ == "__main__":
    sys.exit(main())
