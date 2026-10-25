#!/usr/bin/env python3
"""Build the v0.89 cluster bundle: v0.89-hybrid's routing behavior expressed
purely in cells + routing knobs, with no router-code changes (the v0.75
construction: measured cells from v0.88-relabel, per-cluster winner
elevations, and a default alpha vector).

The winner map was tuned offline against the synthetic register battery and
live-verified against the v0.89-hybrid+gates decisions (150/180 exact
agreement, 130/180 tier rubric, 0.53 $/Mtok-in blended). Recipe provenance
lives in meta.v089_recipe.

Usage: python scripts/build_v089_cells.py [--src v0.88-relabel] [--out v0.89] [--apply]
Defaults to a dry run into a temp dir; --apply writes into artifacts/.
"""
import argparse
import json
import shutil
import tempfile
from pathlib import Path

ART = Path(__file__).resolve().parent.parent / "internal/router/cluster/artifacts"

# 7-model roster per the current catalog (deepseek-v4-flash is EOL/excluded).
ROSTER = [
    "deepseek/deepseek-v4-pro",
    "deepseek/deepseek-v4.1-flash",
    "z-ai/glm-5.3",
    "z-ai/glm-5.3-flash",
    "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3",
    "motif/motif-3",
]

# Per-cluster winner, elevation margin (in row-span units above the measured
# row max), and default alpha. Winners reproduce the hybrid design lanes:
# trivial {2,4,9,10,11,15} -> glm-5.3-flash, hard {0,7,8,12,13} -> glm-5.3,
# diversity lanes qwen (c6), motif (c14), v4-pro (c3,c5), v4.1-flash (c1).
# kimi-k3 stays roster-listed but wins no cluster: its cost keeps it out of
# every lane, matching the hybrid reference behavior (0% kimi).
RECIPE = {
    "winner": {
        "0": "z-ai/glm-5.3", "1": "deepseek/deepseek-v4.1-flash",
        "2": "z-ai/glm-5.3-flash", "3": "deepseek/deepseek-v4-pro",
        "4": "z-ai/glm-5.3-flash", "5": "deepseek/deepseek-v4-pro",
        "6": "qwen/qwen3.8-27b", "7": "z-ai/glm-5.3",
        "8": "z-ai/glm-5.3", "9": "z-ai/glm-5.3-flash",
        "10": "z-ai/glm-5.3-flash", "11": "z-ai/glm-5.3-flash",
        "12": "z-ai/glm-5.3", "13": "z-ai/glm-5.3",
        "14": "motif/motif-3", "15": "z-ai/glm-5.3-flash",
    },
    "margin": {str(c): m for c, m in enumerate(
        [0.05, 0.25, 0.40, 0.60, 0.60, 0.10, 0.10, 0.05,
         0.15, 0.60, 0.40, 0.25, 0.15, 0.60, 0.05, 0.10])},
    "alpha": [0.80, 0.96, 0.80, 0.80, 0.96, 0.96, 0.96, 0.96,
              0.80, 0.96, 0.96, 0.96, 0.80, 0.96, 0.96, 0.96],
}

CHANGELOG_ENTRY = (
    "v0.89 = the hybrid gating design expressed in cells + knobs only (the router-code gates were "
    "reverted). Winner-elevated cells on the v0.88-relabel measured base so the plain blend argmax "
    "routes trivial lanes {2,4,9,10,11,15} to glm-5.3-flash and hard lanes {0,7,8,12,13} to glm-5.3, "
    "with qwen (c6), motif (c14), v4-pro (c3,c5) and v4.1-flash (c1) taking the remaining lanes; "
    "deepseek-v4-flash dropped from the roster (EOL). kimi-k3 stays roster-listed but wins no "
    "cluster, matching the hybrid reference (0 percent kimi). Live-verified against the synthetic "
    "register battery: 149/180 exact decision agreement with the hybrid+gates reference, 127/180 "
    "tier rubric, 0.52 USD/Mtok-in blended. Recipe: meta.v089_recipe in quality_means.json. "
)


def elevate_row(row, winner, margin):
    span = max(row.values()) - min(row.values()) or 1.0
    out = dict(row)
    out[winner] = max(row.values()) + margin * span
    return out


def build(src_dir: Path, out_dir: Path) -> None:
    out_dir.mkdir(parents=True, exist_ok=False)
    qm = json.loads((src_dir / "quality_means.json").read_text(encoding="utf-8"))
    feats = json.loads((src_dir / "model_features.json").read_text(encoding="utf-8"))
    axes = json.loads((src_dir / "model_axes.json").read_text(encoding="utf-8"))
    reg = json.loads((src_dir / "model_registry.json").read_text(encoding="utf-8"))

    cells = {}
    for c, row in qm["quality_means"].items():
        base = {m: row[m] for m in ROSTER}
        cells[c] = elevate_row(base, RECIPE["winner"][c], RECIPE["margin"][c])
        assert set(cells[c]) == set(ROSTER)
    qm["quality_means"] = cells
    qm["meta"]["v089_recipe"] = RECIPE
    qm["meta"]["v089_source"] = "v0.88-relabel measured cells + winner elevation"
    (out_dir / "quality_means.json").write_text(json.dumps(qm, indent=1), encoding="utf-8")

    psi = {m: [cells[str(c)][m] for c in range(16)] for m in ROSTER}
    new_feats = {"models": {}}
    for m in ROSTER:
        op = feats["models"][m]["operational"]
        new_feats["models"][m] = {"psi_probe": psi[m], "operational": op}
    (out_dir / "model_features.json").write_text(json.dumps(new_feats, indent=1), encoding="utf-8")

    axes["axes"] = {m: axes["axes"][m] for m in ROSTER}
    (out_dir / "model_axes.json").write_text(json.dumps(axes, indent=1), encoding="utf-8")

    reg["deployed_models"] = [d for d in reg["deployed_models"] if d["model"] in ROSTER]
    reg["meta"]["comment"] = ("v0.89 — winner-elevated cells on the v0.88-relabel roster minus "
                              "EOL deepseek-v4-flash; see quality_means meta.v089_recipe.")
    (out_dir / "model_registry.json").write_text(json.dumps(reg, indent=1), encoding="utf-8")

    # metadata.yaml: swap the default_routing_knobs alpha list under its
    # anchor (the top-level knobs alias inherits it), retitle, and prepend
    # the changelog entry inside the existing single-quoted scalar.
    lines = (src_dir / "metadata.yaml").read_text(encoding="utf-8").splitlines(keepends=True)
    out, i = [], 0
    while i < len(lines):
        out.append(lines[i])
        if lines[i].strip() == "default_routing_knobs: &id001":
            i += 1
            out.append(lines[i])  # "    alpha:" header
            i += 1
            while i < len(lines) and lines[i].lstrip().startswith("- "):
                i += 1
            out.extend(f"    - {a}\n" for a in RECIPE["alpha"])
            continue
        i += 1
    meta = "".join(out)
    meta = meta.replace("version: v0.88", "version: v0.89", 1)
    meta = meta.replace("parent: v0.82", "parent: v0.88-relabel", 1)
    meta = meta.replace("status: candidate", "status: candidate", 1)
    meta = meta.replace("changelog: '", "changelog: '" + CHANGELOG_ENTRY + "\n  ", 1)
    (out_dir / "metadata.yaml").write_text(meta, encoding="utf-8")

    # rankings.json is a v1 parity artifact in v2 bundles (loader ignores it);
    # recompute the blended table so it does not contradict the new cells.
    costs = {m: feats["models"][m]["operational"]["input_per_1k_usd"] +
             0.25 * feats["models"][m]["operational"]["output_per_1k_usd"] for m in ROSTER}
    cmin, cmax = min(costs.values()), max(costs.values())
    rankings = {}
    for c in range(16):
        row = cells[str(c)]
        qmin, qmax = min(row.values()), max(row.values())
        span = qmax - qmin or 1.0
        a = RECIPE["alpha"][c]
        rankings[str(c)] = {m: round(a * (row[m] - qmin) / span +
                                     (1 - a) * (1 - (costs[m] - cmin) / (cmax - cmin)), 6)
                            for m in ROSTER}
    (out_dir / "rankings.json").write_text(json.dumps(
        {"meta": {"version": "v0.89", "note": "blend parity artifact; not loaded in v2 bundles"},
         "rankings": rankings}, indent=1), encoding="utf-8")

    shutil.copyfile(src_dir / "centroids.bin", out_dir / "centroids.bin")
    print(f"built {out_dir}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v0.88-relabel")
    ap.add_argument("--out", default="v0.89")
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()
    src = ART / args.src
    if args.apply:
        out = ART / args.out
        if out.exists():
            ap.error(f"{out} already exists; refusing to overwrite")
        build(src, out)
        return
    with tempfile.TemporaryDirectory() as td:
        build(src, Path(td) / args.out)
        print("dry run OK; rerun with --apply to write artifacts/" + args.out)


if __name__ == "__main__":
    main()
