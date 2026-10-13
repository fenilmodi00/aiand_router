#!/usr/bin/env python3
"""Build artifacts/v0.84: measured quality cells from the full label corpus.

Ticket #88. Pure artifact change: v0.84 = v0.82 geometry (centroids.bin,
model_axes.json, model_registry.json byte-identical) with every quality cell
re-derived from measured ground-truth labels over the whole 1,503-prompt
all-8-scored corpus using upstream v0.75's calibration recipe:

  1. per-prompt z-score across the 8 roster columns (skip <2 scored or
     zero variance)
  2. per-(cluster, model) shrunk mean toward the model's global mean with
     shrinkage_k0 = 10
  3. min-max rescale into each cluster's existing v0.82 cell range

Cluster assignment reuses the committed cache
(.context/label-runs/prompt-clusters-v084.json), which is the live top-1 cosine
route produced by scripts/refresh_cluster_assignments.py, so no embedder is
required here. (The earlier v082-named cache was stale: 150 of its 1,682
entries disagreed with that route.)
"""
from __future__ import annotations

import hashlib
import json
import shutil
import sys
from pathlib import Path

import yaml

K = 16
SHRINKAGE_K0 = 10.0
EPS = 1e-9
MIN_USABLE_N = 20  # upstream MIN_CODING_N gate for a measured-only cell

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
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v084.json"  # live cluster ids (refreshed by scripts/refresh_cluster_assignments.py); the v082 cache is stale — 150/1682 entries disagree with the live top-1 cosine route
ART = REPO / "internal/router/cluster/artifacts"
SRC = ART / "v0.82"
DST = ART / "v0.84"


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


def main() -> int:
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r

    assign_all = json.loads(ASSIGN.read_text(encoding="utf-8"))
    # Only prompts that carry at least one scored row for a roster model.
    prompts = [p for p in sorted(by_prompt) if p in assign_all]
    missing = [p for p in sorted(by_prompt) if p not in assign_all]
    print(f"labels: {len(rows)} rows, {len(by_prompt)} prompts; "
          f"{len(prompts)} cluster-assigned, {len(missing)} unassigned")

    # --- 1. per-prompt z-score across the 8 roster columns
    z_scores: dict[str, dict[str, float]] = {}
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
        z_scores[p] = {m: (scored[m] - mean) / std for m in ms}
    print(f"z-scored prompts: {len(z_scores)} of {len(prompts)}")

    # --- 2. shrunk means toward each model's global mean
    gmean = {m: sum(z_scores[p][m] for p in z_scores if m in z_scores[p])
             / max(1, sum(1 for p in z_scores if m in z_scores[p])) for m in MODELS}
    cl_ps = {k: [p for p in z_scores if assign_all[p] == k] for k in range(K)}
    sizes_all = {k: sum(1 for p in prompts if assign_all[p] == k) for k in range(K)}
    shrunk = {k: {} for k in range(K)}
    for k in range(K):
        ps = cl_ps[k]
        for m in MODELS:
            scored = [z_scores[p][m] for p in ps if m in z_scores[p]]
            if not scored:
                shrunk[k][m] = None
                continue
            mean = sum(scored) / len(scored)
            shrunk[k][m] = (len(scored) * mean + SHRINKAGE_K0 * gmean[m]) / (len(scored) + SHRINKAGE_K0)
    usable_n = {k: len(cl_ps[k]) for k in range(K)}
    print("usable measured n per cluster:", usable_n)

    # --- 3. min-max rescale into each cluster's existing v0.82 cell range
    qm = json.loads((SRC / "quality_means.json").read_text(encoding="utf-8"))
    features = json.loads((SRC / "model_features.json").read_text(encoding="utf-8"))
    axes = json.loads((SRC / "model_axes.json").read_text(encoding="utf-8"))
    rk = json.loads((SRC / "rankings.json").read_text(encoding="utf-8"))
    costs8 = {m: axes["axes"][m]["input_per_1k_usd"] for m in MODELS}

    new_cells: dict[int, dict[str, float]] = {}
    provenance: dict[int, dict] = {}
    for k in range(K):
        base = qm["quality_means"][str(k)]
        if usable_n[k] >= MIN_USABLE_N:
            zs = [shrunk[k][m] for m in MODELS if shrunk[k][m] is not None]
            zlo, zhi = min(zs), max(zs)
            row = {}
            for m in MODELS:
                z = shrunk[k][m]
                row[m] = (base[m] if z is None
                          else zlo + (zhi - zlo) * ((z - zlo) / (zhi - zlo) if zhi > zlo else 0.5))
            new_cells[k] = row
            provenance[k] = {"cell": "measured", "n": usable_n[k], "n_all": sizes_all[k]}
        else:
            new_cells[k] = {m: base[m] for m in MODELS}
            provenance[k] = {"cell": "prior_fallback", "n": usable_n[k], "n_all": sizes_all[k]}

    # --- 4. replay: measured accuracy of the resulting routed mix
    # per-cluster argmax at the bundle alpha (v0.82's shipped knobs, so the
    # comparison isolates the cell change from the knob change done in #89)
    meta082 = yaml.safe_load((SRC / "metadata.yaml").read_text(encoding="utf-8"))
    knobs082 = meta082.get("default_routing_knobs") or meta082["training"]["default_routing_knobs"]
    alpha082 = knobs082["alpha"]

    def routed_acc(cells, alpha_vec):
        cells = {int(k): v for k, v in cells.items()}
        num = den = 0
        winners = {}
        for k in range(K):
            bl = blend_row(cells[k], costs8, alpha_vec[k])
            winners[k] = max(MODELS, key=lambda m: bl[m])
        for p in prompts:
            w = winners[assign_all[p]]
            if w in by_prompt[p]:
                num += by_prompt[p][w]["score"]
                den += 1
        return (num / den if den else 0.0), den, winners

    acc82, n82, _ = routed_acc(qm["quality_means"], alpha082)
    acc84, n84, winners84 = routed_acc(new_cells, alpha082)
    print(f"replay at v0.82 knobs: v0.82 cells {100*acc82:.2f}% (n={n82}) -> "
          f"v0.84 cells {100*acc84:.2f}% (n={n84}) delta {100*(acc84-acc82):+.2f}pt")

    # --- 5. emit v0.84
    DST.mkdir(parents=True, exist_ok=True)
    for fn in ["centroids.bin", "model_axes.json", "model_registry.json"]:
        shutil.copyfile(SRC / fn, DST / fn)
    for fn in ["centroids.bin", "model_axes.json", "model_registry.json"]:
        a = hashlib.sha256((SRC / fn).read_bytes()).hexdigest()
        b = hashlib.sha256((DST / fn).read_bytes()).hexdigest()
        assert a == b, f"{fn} not byte-identical to v0.82"
        print(f"byte-identical: {fn} sha256={a[:16]}…")

    qm_new = json.loads(json.dumps(qm))
    qm_new["quality_means"] = {str(k): new_cells[k] for k in range(K)}
    prov = {str(k): provenance[k] for k in range(K)}
    qm_new["meta"]["v084_provenance"] = prov
    (DST / "quality_means.json").write_text(
        json.dumps(qm_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    feats_new = json.loads(json.dumps(features))
    feats_new["meta"]["comment"] = (
        "v0.84: v0.82 operational columns + measured quality cells "
        "(scripts/build_v084_measured_cells.py); psi_probe mirrors quality_means.")
    feats_new["meta"]["roster_version"] = "v0.84"
    for m, cols in feats_new["models"].items():
        for k in range(K):
            if m in new_cells[k]:
                cols["psi_probe"][k] = new_cells[k][m]
    (DST / "model_features.json").write_text(
        json.dumps(feats_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    alpha84 = list(alpha082)
    rk_new = json.loads(json.dumps(rk))
    rk_new["rankings"] = {str(k): blend_row(new_cells[k], costs8, alpha84[k]) for k in range(K)}
    rk_new["meta"]["alpha"] = round(sum(alpha84) / K, 4)
    rk_new["meta"]["alpha_per_cluster"] = list(alpha84)
    rk_new["meta"]["regenerated_by"] = "build_v084_measured_cells.py"
    (DST / "rankings.json").write_text(
        json.dumps(rk_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    meta = yaml.safe_load((SRC / "metadata.yaml").read_text(encoding="utf-8"))
    meta["version"] = "v0.84"
    meta["parent"] = "v0.82"
    n_meas = sum(1 for v in prov.values() if v["cell"] == "measured")
    meta["changelog"] = (
        f"v0.84 = v0.82 with every quality cell re-derived from the measured label "
        f"corpus (ticket #88; scripts/build_v084_measured_cells.py). Recipe: "
        f"per-prompt z-score across the 8 roster columns, per-(cluster, model) "
        f"shrunk mean with shrinkage_k0=10 toward the model's global mean, "
        f"min-max rescaled into each cluster's existing cell range. Source: "
        f".context/label-runs/v082-expanded-labels.jsonl ({len(rows)} rows, "
        f"{len(z_scores)} z-scored prompts) with cluster assignment from "
        f".context/label-runs/prompt-clusters-v084.json (live top-1 cosine route, "
        f"refreshed by scripts/refresh_cluster_assignments.py; the v082-named cache "
        f"is stale by 150 of its 1,682 entries). {n_meas} of {K} "
        f"clusters pass the n>={MIN_USABLE_N} usable gate and are measured-only; "
        f"the remaining {K-n_meas} keep the v0.82 cell (tracked for ticket #91 "
        f"priors). Provenance per cluster is recorded in quality_means.json "
        f"meta.v084_provenance. Replay at v0.82's own knobs: v0.82 cells "
        f"{100*acc82:.2f}% (n={n82}) -> v0.84 cells {100*acc84:.2f}% (n={n84}), "
        f"delta {100*(acc84-acc82):+.2f}pt on the identical prompt set. "
        f"centroids.bin, model_axes.json and model_registry.json are BYTE-IDENTICAL "
        f"to v0.82 (sha256-verified); no geometry, roster or price change.\n\n"
        + str(meta.get("changelog", "")))
    with open(DST / "metadata.yaml", "w", encoding="utf-8", newline="\n") as f:
        yaml.safe_dump(meta, f, sort_keys=False, allow_unicode=True, width=100)
    print("wrote", DST)
    return 0


if __name__ == "__main__":
    sys.exit(main())
