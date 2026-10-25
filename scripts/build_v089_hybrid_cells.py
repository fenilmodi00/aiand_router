#!/usr/bin/env python3
"""Build the v0.89-hybrid candidate bundle from v0.88-relabel.

Recipe (v0.89 plan item 2/3, .context/v089_plan.md):
1. KIMI PRIORS: in every cluster where kimi-k3's blend-normalized quality is
   exactly 0.0 (i.e. its quality_means cell is the row minimum — missing
   evidence, not a measured zero; that cell normalizes to 0.0 in the runtime
   blend so argmax can never pick the kimi lane), replace kimi's cell with
   0.95 x that cluster's z-ai/glm-5.3 cell.
2. MOTIF BOOST: cluster 1 motif/motif-3 cell += 0.12 (absolute).
3. All other cells verbatim from v0.88-relabel. No v0.88 hard-column copying:
   the plan's 80% rubric outcome comes from policy gates shipped separately,
   not from cells.

Every cell edit lands in BOTH quality_means.json ("quality_means") AND
model_features.json ("models[*].psi_probe") with identical values —
model_features.json overrides quality_means at load (playbook trap 1).
centroids.bin / model_axes.json / model_registry.json stay byte-identical.
rankings.json is regenerated via blend parity (provenance only; not loaded
for v2 bundles).

Usage: python scripts/build_v089_hybrid_cells.py --src v0.88-relabel \
    --out v0.89-hybrid --apply   (defaults to a dry run into a temp dir)
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import shutil
import sys
import tempfile
from pathlib import Path

import yaml

K = 16

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

KIMI = "moonshotai/kimi-k3"
GLM = "z-ai/glm-5.3"
MOTIF = "motif/motif-3"
KIMI_PRIOR_FACTOR = 0.95
# (cluster, absolute boost) mirroring .context/hybrid_design.json motif_boosted
MOTIF_BOOSTED = [(1, 0.12)]

REPO = Path(__file__).resolve().parent.parent
ART = REPO / "internal/router/cluster/artifacts"


def blend_row(qm_row, costs, alpha):
    """Runtime blend parity (scorer.go blendScoresV2, speed_weight=0 path)."""
    present = [m for m in qm_row if m in costs]
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


def blend_costs(axes):
    """Per-model blend cost from model_axes.json (same math as the runtime blend)."""
    verbosity = {m: (axes.get(m, {}).get("verbosity_tokens") or 0) for m in MODELS}
    vvals = sorted(v for v in verbosity.values() if v)
    med_v = vvals[len(vvals) // 2] if vvals else 1
    ocr = 0.25
    costs = {}
    for m in MODELS:
        a = axes.get(m, {})
        inp = a.get("input_per_1k_usd", 0) or 0
        outp = a.get("output_per_1k_usd", 0) or 0
        vf = (verbosity[m] / med_v) if (med_v and verbosity[m]) else 1.0
        costs[m] = inp + ocr * outp * vf
    return costs


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v0.88-relabel", help="source bundle (default: v0.88-relabel)")
    ap.add_argument("--out", default="v0.89-hybrid", help="candidate version to write")
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

    # --- 1. Load source artifacts ---
    qm = json.loads((src / "quality_means.json").read_text(encoding="utf-8"))
    features = json.loads((src / "model_features.json").read_text(encoding="utf-8"))
    axes = json.loads((src / "model_axes.json").read_text(encoding="utf-8"))
    rk = json.loads((src / "rankings.json").read_text(encoding="utf-8"))

    cells = {k: dict(qm["quality_means"][str(k)]) for k in range(K)}

    # Invariant on the source: every roster model present in every cluster row, no NaN
    for k in range(K):
        missing = [m for m in MODELS if m not in cells[k]]
        assert not missing, f"cluster {k} missing models {missing}"
        for m, v in cells[k].items():
            assert not math.isnan(v), f"NaN cell {k}/{m}"

    # --- 2. Kimi priors on zero-evidence cells ---
    # kimi's blend-normalized quality is exactly 0.0 wherever its cell is the
    # row minimum; cross-check against the source rankings.json blend output.
    rk_meta = rk.get("meta", {})
    alpha_rk = rk_meta.get("alpha_per_cluster")
    costs8 = blend_costs(axes.get("axes", {}))
    kimi_prior_clusters = []
    for k in range(K):
        row = cells[k]
        is_row_min = row[KIMI] == min(row.values())
        rk_qn_zero = bool(alpha_rk) and KIMI in rk["rankings"][str(k)] \
            and rk["rankings"][str(k)][KIMI] == 0.0
        if is_row_min and (rk_qn_zero or alpha_rk is None):
            kimi_prior_clusters.append(k)
    print(f"kimi zero-evidence (blend-0.0) clusters: {kimi_prior_clusters} "
          f"({len(kimi_prior_clusters)}/{K})")
    assert kimi_prior_clusters, "no kimi zero cells found; recipe precondition broken"

    for k in kimi_prior_clusters:
        cells[k][KIMI] = KIMI_PRIOR_FACTOR * cells[k][GLM]

    # --- 3. Motif boost ---
    for c, boost in MOTIF_BOOSTED:
        cells[c][MOTIF] += boost
        print(f"motif boost: cluster {c} {MOTIF} += {boost}")

    # --- 4. Provenance (per cluster) ---
    relabel_prov = qm["meta"].get("v088_relabel_provenance", {})
    provenance = {}
    for k in range(K):
        n = relabel_prov.get(str(k), {}).get("n")
        n_all = relabel_prov.get(str(k), {}).get("n_all")
        if k in kimi_prior_clusters:
            provenance[k] = {"cell": "prior", "source": "kimi prior = glm-5.3 x0.95",
                             "n": n, "n_all": n_all}
        elif any(k == c for c, _ in MOTIF_BOOSTED):
            provenance[k] = {"cell": "hybrid", "source": f"motif-3 +{dict(MOTIF_BOOSTED)[k]} (motif_boosted)",
                             "n": n, "n_all": n_all}
        else:
            provenance[k] = {"cell": "measured", "n": n, "n_all": n_all}

    # --- 5. Compare winners before/after at source knobs ---
    meta_src = yaml.safe_load((src / "metadata.yaml").read_text(encoding="utf-8"))
    knobs = meta_src.get("training", {}).get("default_routing_knobs", {})
    alpha_vec = knobs.get("alpha")
    assert alpha_vec and len(alpha_vec) == K, "source knobs alpha must be a length-16 vector"

    old_cells = {k: qm["quality_means"][str(k)] for k in range(K)}

    def winners_for(cel):
        w = {}
        for k in range(K):
            bl = blend_row(cel[k], costs8, alpha_vec[k])
            w[k] = max(bl, key=bl.get)
        return w

    winners_old = winners_for(old_cells)
    winners_new = winners_for(cells)
    from collections import Counter
    print("\n=== Routing winner comparison (at source knobs) ===")
    print("Before:", dict(sorted(Counter(winners_old.values()).items())))
    print("After: ", dict(sorted(Counter(winners_new.values()).items())))
    flips = {k: (winners_old[k], winners_new[k]) for k in range(K)
             if winners_old[k] != winners_new[k]}
    if flips:
        for k, (a, b) in sorted(flips.items()):
            print(f"  FLIP c{k}: {a} -> {b}")
    else:
        print("  no winner flips")
    print(f"Distinct winners: {len(set(winners_new.values()))}")
    missing = set(MODELS) - set(winners_new.values())
    if missing:
        print(f"  WARN models winning no cluster: {missing}")

    # --- 6. Write candidate bundle ---
    shutil.copytree(src, dst, dirs_exist_ok=True)
    for fn in ["centroids.bin", "model_axes.json", "model_registry.json"]:
        shutil.copyfile(src / fn, dst / fn)
        a = hashlib.sha256((src / fn).read_bytes()).hexdigest()
        b = hashlib.sha256((dst / fn).read_bytes()).hexdigest()
        assert a == b, f"{fn} not byte-identical to {args.src}"
        print(f"byte-identical: {fn} sha256={a[:16]}...")

    # quality_means.json with hybrid cells + provenance (keep v088_relabel_* keys)
    qm_new = json.loads(json.dumps(qm))
    qm_new["quality_means"] = {str(k): cells[k] for k in range(K)}
    qm_new["meta"]["v089_provenance"] = {str(k): provenance[k] for k in range(K)}
    qm_new["meta"]["v089_hybrid_source"] = f"{args.src} (scripts/build_v089_hybrid_cells.py)"
    qm_new["meta"]["v089_hybrid_recipe"] = (
        "kimi priors: clusters where moonshotai/kimi-k3's blend-normalized quality is 0.0 "
        f"(row-min cell, missing evidence) set to {KIMI_PRIOR_FACTOR} x z-ai/glm-5.3 cell "
        f"{kimi_prior_clusters}; motif boost: cluster "
        f"{[c for c, _ in MOTIF_BOOSTED]} motif/motif-3 += "
        f"{[b for _, b in MOTIF_BOOSTED]}; all other cells verbatim from "
        f"{args.src}. Policy gates (trivial -> glm-5.3-flash, c0-hard -> glm-5.3) "
        "ship separately and are NOT encoded in cells."
    )
    (dst / "quality_means.json").write_text(
        json.dumps(qm_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # model_features.json psi_probe mirrors new quality_means exactly
    feats_new = json.loads(json.dumps(features))
    feats_new["meta"]["comment"] = (
        "v0.89 hybrid: v0.88-relabel cells + kimi zero-cell priors (glm-5.3 x0.95) "
        "+ motif c1 boost; psi_probe mirrors quality_means."
    )
    feats_new["meta"]["roster_version"] = args.out
    for m in MODELS:
        if m in feats_new["models"]:
            feats_new["models"][m]["psi_probe"] = [cells[k][m] for k in range(K)]
    (dst / "model_features.json").write_text(
        json.dumps(feats_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # Post-write invariants: psi_probe == quality_means exactly, full roster, no NaN
    qm_check = json.loads((dst / "quality_means.json").read_text(encoding="utf-8"))
    mf_check = json.loads((dst / "model_features.json").read_text(encoding="utf-8"))
    for m in MODELS:
        assert m in mf_check["models"], f"model_features missing {m}"
        for k in range(K):
            want = qm_check["quality_means"][str(k)][m]
            got = mf_check["models"][m]["psi_probe"][k]
            assert want == got, f"psi_probe != quality_means for {m} cluster {k}: {got} != {want}"
            assert not math.isnan(got), f"NaN cell {k}/{m}"

    # rankings.json regenerated via runtime blend parity (provenance only)
    rk_new = json.loads(json.dumps(rk))
    rk_new["rankings"] = {
        str(k): blend_row(cells[k], costs8, alpha_vec[k]) for k in range(K)
    }
    rk_new["meta"]["alpha"] = round(sum(alpha_vec) / K, 4)
    rk_new["meta"]["alpha_per_cluster"] = list(alpha_vec)
    rk_new["meta"]["regenerated_by"] = "build_v089_hybrid_cells.py"
    (dst / "rankings.json").write_text(
        json.dumps(rk_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # metadata.yaml: version/parent/status + prepended changelog; knobs untouched
    meta = yaml.safe_load((dst / "metadata.yaml").read_text(encoding="utf-8"))
    n_prior = len(kimi_prior_clusters)
    meta["version"] = args.out
    meta["parent"] = args.src
    meta["status"] = "candidate"
    meta["changelog"] = (
        f"v0.89-hybrid = {args.src} with two cell repairs. "
        f"(1) Kimi zero-evidence repair: in {n_prior} clusters "
        f"({kimi_prior_clusters}) kimi-k3's cell was the row minimum, which "
        f"normalizes to 0.0 in the runtime blend (missing evidence, not a "
        f"measured zero) — replaced with {KIMI_PRIOR_FACTOR} x the cluster's "
        f"glm-5.3 cell. (2) Motif boost: cluster 1 motif-3 +0.12 (motif_boosted). "
        f"All other cells verbatim from {args.src}; no v0.88 hard-column "
        f"copying — the plan's 80% rubric outcome comes from policy gates "
        f"(trivial -> glm-5.3-flash, c0-hard -> glm-5.3) shipped separately, "
        f"not from cells. Per-cluster cell sources in quality_means.json "
        f"meta.v089_provenance (prior/hybrid/measured). "
        f"centroids.bin/model_axes.json/model_registry.json byte-identical to "
        f"{args.src}; default_routing_knobs unchanged. "
        f"Winner flips at source knobs: "
        + (", ".join(f"c{k}:{a.split('/')[-1]}->{b.split('/')[-1]}"
                     for k, (a, b) in sorted(flips.items())) or "none")
        + f"; distinct winners {len(set(winners_new.values()))}.\n\n"
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
