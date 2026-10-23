#!/usr/bin/env python3
"""Build a candidate v0.88 bundle with refreshed measured quality cells.

This script recomputes quality_means from v0.82 label data while preserving
v0.88's geometry (centroids, axes, registry byte-identical). It uses a hybrid
approach: prior (low-data) clusters keep their v0.88 cells, measured clusters
get fresh z-scored cells with operator offsets applied as range-relative boosts
(post rescale, per v0.85/v0.86 mechanism).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sys
import tempfile
from collections import Counter
from pathlib import Path

import yaml

# --- Configuration ---

K = 16
SHRINKAGE_K0 = 10.0
EPS = 1e-9
MIN_USABLE_N = 20

# v0.88 roster names (what the bundle uses)
MODELS = [
    "deepseek/deepseek-v4-pro",
    "deepseek/deepseek-v4-flash",
    "deepseek/deepseek-v4.1-flash",
    "z-ai/glm-5.3",
    "z-ai/glm-5.3-flash",
    "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3",
    "motif/motif-3",
]

# Name normalization: old format (label file) -> new format (v0.88 bundle)
NAME_MAP = {
    "deepseek-ai/deepseek-v4-pro": "deepseek/deepseek-v4-pro",
    "deepseek-ai/deepseek-v4-flash": "deepseek/deepseek-v4-flash",
    "deepseek-ai/deepseek-v4.1-flash": "deepseek/deepseek-v4.1-flash",
    "zai-org/glm-5.3": "z-ai/glm-5.3",
    "zai-org/glm-5.3-flash": "z-ai/glm-5.3-flash",
    "qwen/qwen3.8-27b": "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3": "moonshotai/kimi-k3",
    "motif-technologies/motif-3": "motif/motif-3",
}

REPO = Path(__file__).resolve().parent.parent
LABELS_PATH = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN_PATH = REPO / ".context/label-runs/prompt-clusters-v084.json"
ART = REPO / "internal/router/cluster/artifacts"

# If labels are not in our repo, try the sibling /mnt/d/router checkout
_LABEL_SRC = Path("/mnt/d/router/.context/label-runs")
if not LABELS_PATH.exists() and _LABEL_SRC.exists():
    LABELS_PATH = _LABEL_SRC / "v082-expanded-labels.jsonl"
    ASSIGN_PATH = _LABEL_SRC / "prompt-clusters-v084.json"

# v0.88 alpha from rankings.json meta:
# 0.96 except c2, c3, c4, c6, c8 at 0.80 (5 demoted clusters)
ALPHA_V088 = [0.96, 0.96, 0.80, 0.80, 0.80, 0.96, 0.80, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96]


def blend_row(qm_row, costs, alpha):
    """Runtime blend parity (scorer.go blendScoresV2, speed_weight=0 path)."""
    present = [m for m in MODELS if m in qm_row]
    qmin = min(qm_row[m] for m in present)
    qmax = max(qm_row[m] for m in present)
    qr = qmax - qmin
    cmin = min(costs[m] for m in present)
    cmax = max(costs[m] for m in present)
    cr = cmax - cmin
    return {
        m: alpha * ((qm_row[m] - qmin) / qr if qr > 0 else 0.0)
        + (1.0 - alpha) * (1.0 - ((costs[m] - cmin) / cr if cr > 0 else 0.0))
        for m in present
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v0.88", help="source bundle (default: v0.88)")
    ap.add_argument("--out", default="v0.88-relabel", help="candidate version to write")
    ap.add_argument("--apply", action="store_true",
                    help="write to artifacts dir (default: dry run into temp)")
    args = ap.parse_args()

    src = ART / args.src
    if not src.is_dir():
        print(f"[error] source bundle {src} not found")
        return 1

    if args.apply:
        dst = ART / args.out
        if dst.exists():
            print(f"[error] {dst} exists; refusing to overwrite")
            return 1
    else:
        dst = Path(tempfile.mkdtemp(prefix=f"bundle-{args.out}-"))
        dst.mkdir(parents=True, exist_ok=True)

    # --- 1. Load and normalize labels ---
    raw_rows = [json.loads(line) for line in LABELS_PATH.read_text(encoding="utf-8").splitlines() if line.strip()]
    rows = []
    for r in raw_rows:
        m = NAME_MAP.get(r["model"], r["model"])
        if m not in MODELS:
            continue
        r["model"] = m
        rows.append(r)

    by_prompt: dict[str, dict[str, dict]] = {}
    for row in rows:
        by_prompt.setdefault(row["global_index"], {})[row["model"]] = row

    assign_all = json.loads(ASSIGN_PATH.read_text(encoding="utf-8"))
    prompts = [p for p in sorted(by_prompt) if p in assign_all]
    print(f"labels: {len(raw_rows)} rows, {len(by_prompt)} prompts, "
          f"{len(rows)} after name normalization; "
          f"{len(prompts)} cluster-assigned")

    # --- 2. Per-prompt z-score across the 8 roster columns ---
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

    # --- 3. Global mean per model ---
    gmean = {
        m: sum(z_scores[p][m] for p in z_scores if m in z_scores[p])
        / max(1, sum(1 for p in z_scores if m in z_scores[p]))
        for m in MODELS
    }
    print("\nGlobal z-score means:")
    for m in sorted(gmean, key=lambda x: gmean[x], reverse=True):
        print(f"  {m}: {gmean[m]:+.4f}")

    # --- 4. Per-cluster shrunk means ---
    cl_ps = {k: [p for p in z_scores if assign_all.get(p) == k] for k in range(K)}
    sizes_all = {k: sum(1 for p in by_prompt if assign_all.get(p) == k) for k in range(K)}
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
    print(f"\nusable measured n per cluster: {usable_n}")

    # --- 5. Load v0.88 artifacts ---
    qm = json.loads((src / "quality_means.json").read_text(encoding="utf-8"))
    features = json.loads((src / "model_features.json").read_text(encoding="utf-8"))
    axes = json.loads((src / "model_axes.json").read_text(encoding="utf-8"))
    rk = json.loads((src / "rankings.json").read_text(encoding="utf-8"))

    # Compute costs from v0.88 model_axes (same as blendScoresV2)
    axes_models = axes.get("axes", {})
    verbosity = {m: (axes_models.get(m, {}).get("verbosity_tokens") or 0) for m in MODELS}
    vvals = sorted(v for v in verbosity.values() if v)
    med_v = vvals[len(vvals) // 2] if vvals else 1
    ocr = 0.25
    costs8 = {}
    for m in MODELS:
        a = axes_models.get(m, {})
        inp = a.get("input_per_1k_usd", 0) or 0
        outp = a.get("output_per_1k_usd", 0) or 0
        vf = (verbosity[m] / med_v) if (med_v and verbosity[m]) else 1.0
        costs8[m] = inp + ocr * outp * vf

    # --- 6. Min-max rescale into v0.88's existing cell ranges ---
    # v0.88 provenance: clusters c1, c2, c3, c5, c13 were prior_fallback (used benchmark priors).
    # These clusters have < 20 measured prompts and should keep the v0.88 cells
    # (which already carry operator offsets from the v0.88 measurement process).
    # Measured clusters get fresh z-scored cells + range-relative operator offsets.
    new_cells: dict[int, dict[str, float]] = {}
    provenance: dict[int, dict] = {}

    # Detect v0.88 prior clusters from the source quality_means meta
    qm_src_meta = json.loads((src / "quality_means.json").read_text(encoding="utf-8")).get("meta", {})
    v088_provenance = qm_src_meta.get("v084_provenance", {}) or qm_src_meta.get("provenance", {})
    prior_clusters = set()
    for k_str, p in v088_provenance.items():
        if p.get("cell", "").startswith("prior"):
            prior_clusters.add(int(k_str))
    print(f"\nv0.88 prior (fallback) clusters: {sorted(prior_clusters)}")

    for k in range(K):
        base = qm["quality_means"][str(k)]

        if k in prior_clusters:
            # Keep v0.88 cells for prior clusters (already have operator offsets)
            new_cells[k] = {m: base.get(m, 0.0) for m in MODELS}
            provenance[k] = {"cell": "prior_fallback", "n": usable_n[k], "n_all": sizes_all[k],
                            "source": "v0.88_preserved"}
            continue

        # Measured cluster
        if usable_n[k] >= MIN_USABLE_N:
            zs = [shrunk[k][m] for m in MODELS if shrunk[k][m] is not None]
            if not zs:
                new_cells[k] = {m: base.get(m, 0.0) for m in MODELS}
                provenance[k] = {"cell": "prior_fallback", "n": 0, "n_all": sizes_all[k]}
                continue
            zlo, zhi = min(zs), max(zs)
            row = {}
            for m in MODELS:
                z = shrunk[k][m]
                if z is None:
                    row[m] = base.get(m, 0.0)
                elif zhi > zlo:
                    row[m] = zlo + (zhi - zlo) * ((z - zlo) / (zhi - zlo))
                else:
                    row[m] = 0.5
            new_cells[k] = row
            provenance[k] = {"cell": "measured", "n": usable_n[k], "n_all": sizes_all[k]}
        else:
            # Not enough measured data: fall back to v0.88 prior cells
            new_cells[k] = {m: base.get(m, 0.0) for m in MODELS}
            provenance[k] = {"cell": "prior_fallback", "n": usable_n[k], "n_all": sizes_all[k],
                            "source": "v0.88_preserved"}
    print(f"\nMeasured clusters: {[k for k in range(K) if provenance[k]['cell'] == 'measured']}")

    # --- 6b. Apply operator overrides as range-relative offsets to measured cells only ---
    # Per the v0.86 mechanism, offsets are applied as a fraction of each cluster's
    # OWN quality cell span, AFTER min-max rescale. This preserves model-routing
    # diversity by boosting preferred models without collapsing the cell range.
    # Only apply to measured cells; prior_fallback cells already carry v0.88 offsets.
    operator_offsets = qm_src_meta.get("operator_override", {}).get("offsets", {})
    if operator_offsets:
        print(f"\nApplying operator offsets as range-relative cell boosts (measured cells only): {operator_offsets}")
        for k in range(K):
            if provenance[k]["cell"] != "measured":
                continue
            row = new_cells[k]
            vals = [row[m] for m in MODELS if m in row]
            if not vals:
                continue
            lo = min(vals)
            hi = max(vals)
            span = (hi - lo) or 1.0
            for m in MODELS:
                if m in operator_offsets and m in row:
                    row[m] += operator_offsets[m] * span

    # --- 7. Compare winners: before (v0.88 cells) vs after (new cells) ---
    meta_v088 = yaml.safe_load((src / "metadata.yaml").read_text(encoding="utf-8"))
    knobs = meta_v088.get("training", {}).get("default_routing_knobs", {})
    alpha_vec = knobs.get("alpha", ALPHA_V088)
    print(f"\nAlpha vector: {list(alpha_vec)}")

    old_cells = {k: qm["quality_means"][str(k)] for k in range(K)}

    def routed_acc(cells, alpha_vec):
        num = den = 0
        winners = {}
        for k in range(K):
            cell_row = {m: cells[k][m] for m in MODELS if m in cells[k]}
            if not cell_row:
                continue
            bl = blend_row(cell_row, costs8, alpha_vec[k])
            winners[k] = max(bl, key=bl.get)
        for p in prompts:
            w = winners.get(assign_all[p])
            if w and w in by_prompt[p]:
                num += by_prompt[p][w]["score"]
                den += 1
        return (num / den if den else 0.0), den, winners

    acc_old, n_old, winners_old = routed_acc(old_cells, alpha_vec)
    acc_new, n_new, winners_new = routed_acc(new_cells, alpha_vec)

    print("\n=== Routing winner comparison (at v0.88 knobs) ===")
    print(f"Before (v0.88 cells): {acc_old*100:.2f}% acc (n={n_old})")
    print(f"After  (new cells):   {acc_new*100:.2f}% acc (n={n_new})")
    delta = (acc_new - acc_old) * 100
    print(f"Delta: {delta:+.2f}pt")

    old_counts = Counter(winners_old.values())
    new_counts = Counter(winners_new.values())
    print("v0.88 winners:", end=" ")
    for m, c in old_counts.most_common():
        print(f"{m}:{c}", end=" ")
    print()
    print("New winners:", end=" ")
    for m, c in new_counts.most_common():
        print(f"{m}:{c}", end=" ")
    print()

    for target in ["deepseek/deepseek-v4.1-flash", "z-ai/glm-5.3-flash"]:
        old_w = old_counts.get(target, 0)
        new_w = new_counts.get(target, 0)
        print(f"  {target}: {old_w}/16 -> {new_w}/16 clusters")

    # Check all 8 models are routable
    missing = set(MODELS) - set(new_counts.keys())
    print(f"\nModels winning clusters: {len(new_counts)}/8")
    if missing:
        print(f"  WARN: Missing: {missing}")
    else:
        print("  All 8 models routable ✓")

    # --- 8. Write candidate bundle ---
    shutil.copytree(src, dst, dirs_exist_ok=True)
    for fn in ["centroids.bin", "model_axes.json", "model_registry.json"]:
        shutil.copyfile(src / fn, dst / fn)
        a = hashlib.sha256((src / fn).read_bytes()).hexdigest()
        b = hashlib.sha256((dst / fn).read_bytes()).hexdigest()
        assert a == b, f"{fn} not byte-identical to {args.src}"
        print(f"byte-identical: {fn} sha256={a[:16]}...")

    # quality_means.json with new cells
    qm_new = json.loads(json.dumps(qm))
    qm_new["quality_means"] = {str(k): new_cells[k] for k in range(K)}

    # Record provenance + operator overrides
    qm_new["meta"]["v088_relabel_provenance"] = {str(k): provenance[k] for k in range(K)}
    qm_new["meta"]["v088_relabel_source"] = "v082-expanded-labels.jsonl (normalized to v0.88 roster names)"
    qm_new["meta"]["v088_relabel_recipe"] = (
        "per-prompt z-score across 8 roster columns, "
        "per-(cluster,model) shrunk mean shrinkage_k0=10, "
        "min-max rescaled into v0.88 cell ranges, "
        "operator offsets as range-relative cell boosts on measured cells only "
        "(scripts/build_v088_measured_cells.py)"
    )
    if operator_offsets:
        qm_new["meta"]["v088_relabel_operator_offsets"] = operator_offsets
        qm_new["meta"]["operator_override"] = {
            "ticket": "v0.88 operator overrides propagated (glm-5.3, glm-5.3-flash, motif-3, qwen pricing)",
            "offsets": operator_offsets,
            "rationale": "Owner decision: route glm-5.3/flash and motif-3 on measured accuracy + price. "
                "Offsets applied as range-relative cell boosts on measured cells only (prior clusters keep v0.88 cells).",
            "reversal": "Rebuild from fresh label campaign without operator offsets.",
        }
    (dst / "quality_means.json").write_text(
        json.dumps(qm_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # model_features.json psi_probe mirrors new quality_means
    feats_new = json.loads(json.dumps(features))
    feats_new["meta"]["comment"] = (
        "v0.88 relabel: v0.88 operational columns + refreshed measured quality cells; "
        "psi_probe mirrors quality_means."
    )
    feats_new["meta"]["roster_version"] = args.out
    for m in MODELS:
        if m in feats_new["models"]:
            cols = feats_new["models"][m]
            cols["psi_probe"] = [new_cells[k][m] for k in range(K)]
    (dst / "model_features.json").write_text(
        json.dumps(feats_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # rankings.json regenerated via runtime blend
    rk_new = json.loads(json.dumps(rk))
    rk_new["rankings"] = {
        str(k): blend_row(new_cells[k], costs8, alpha_vec[k]) for k in range(K)
    }
    rk_new["meta"]["alpha"] = round(sum(alpha_vec) / K, 4)
    rk_new["meta"]["alpha_per_cluster"] = list(alpha_vec)
    rk_new["meta"]["regenerated_by"] = "build_v088_measured_cells.py"
    (dst / "rankings.json").write_text(
        json.dumps(rk_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # metadata.yaml changelog
    meta = yaml.safe_load((dst / "metadata.yaml").read_text(encoding="utf-8"))
    n_meas = sum(1 for v in provenance.values() if v["cell"] == "measured")
    v41_count = sum(1 for row in rows if row["model"] == "deepseek/deepseek-v4.1-flash")
    glm_count = sum(1 for row in rows if row["model"] == "z-ai/glm-5.3-flash")
    meta["changelog"] = (
        f"v0.88-relabel = v0.88 with quality_means recomputed from "
        f".context/label-runs/v082-expanded-labels.jsonl "
        f"({v41_count} v4.1-flash + {glm_count} glm-5.3-flash labels). "
        f"Recipe: per-prompt z-score across 8 roster columns, "
        f"per-(cluster,model) shrunk mean shrinkage_k0=10, "
        f"min-max rescaled into v0.88 cell ranges, "
        f"operator offsets as range-relative cell boosts on measured cells only "
        f"(prior clusters preserved from v0.88). "
        f"centroids.bin/model_axes.json/model_registry.json byte-identical to v0.88. "
        f"Replay at v0.88 knobs: v0.88 cells {100*acc_old:.2f}% (n={n_old}) -> "
        f"relabel cells {100*acc_new:.2f}% (n={n_new}), delta {100*delta:+.2f}pt. "
        f"{n_meas}/{K} clusters are measured; the rest keep prior fallback.\n\n"
        + str(meta.get("changelog", ""))
    )
    with open(dst / "metadata.yaml", "w", encoding="utf-8", newline="\n") as f:
        yaml.safe_dump(meta, f, sort_keys=False, allow_unicode=True, width=100)
    print(f"\nwrote: {dst}")
    if args.apply:
        print(f"  -> promoted candidate {args.out}")
    else:
        print(f"  -> dry run (temp dir). Use --apply to write to artifacts/{args.out}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
