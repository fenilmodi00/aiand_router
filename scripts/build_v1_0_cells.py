#!/usr/bin/env python3
"""Regenerate the v1.0 cluster bundle in place.

v1.0 is the MVP release of v0.89: winner-elevated cells expressing the
hybrid gating design in cells + knobs only (no router-code changes). The
measured base cells (originally v0.88-relabel's) are snapshotted in the
bundle's own quality_means meta.v1_0_base_cells, so this script regenerates
the bundle without the retired v0.87/v0.88/v0.88-relabel directories.

Usage: python scripts/build_v1_0_cells.py [--src v1.0] [--apply]
Defaults to a dry run into a temp dir; --apply rewrites artifacts/v1.0.
"""
import argparse
import json
import shutil
import tempfile
from pathlib import Path

ART = Path(__file__).resolve().parent.parent / "internal/router/cluster/artifacts"
VERSION = "v1.0"

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
# row max), and default alpha. Trivial lanes {2,4,9,10,11,15} ->
# glm-5.3-flash, hard lanes {0,7,8,12,13} -> glm-5.3, diversity lanes qwen
# (c6), motif (c14), v4-pro (c3,c5), v4.1-flash (c1). kimi-k3 stays
# roster-listed but wins no cluster (cost keeps it out of every lane).
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


def elevate_row(row, winner, margin):
    span = max(row.values()) - min(row.values()) or 1.0
    out = dict(row)
    out[winner] = max(row.values()) + margin * span
    return out


def build(src_dir: Path, out_dir: Path) -> None:
    out_dir.mkdir(parents=True, exist_ok=False)
    qm = json.loads((src_dir / "quality_means.json").read_text(encoding="utf-8"))
    feats = json.loads((src_dir / "model_features.json").read_text(encoding="utf-8"))
    base = qm["meta"].get("v1_0_base_cells")
    if not base:
        raise SystemExit("src bundle lacks meta.v1_0_base_cells; cannot regenerate")

    cells = {c: elevate_row({m: base[c][m] for m in ROSTER},
                            RECIPE["winner"][c], RECIPE["margin"][c]) for c in base}
    assert set(cells) == {str(i) for i in range(16)} and all(set(r) == set(ROSTER) for r in cells.values())
    qm["quality_means"] = cells
    qm["meta"]["v1_0_recipe"] = RECIPE
    (out_dir / "quality_means.json").write_text(json.dumps(qm, indent=1), encoding="utf-8")

    psi = {m: [cells[str(c)][m] for c in range(16)] for m in ROSTER}
    new_feats = {"models": {m: {"psi_probe": psi[m],
                                "operational": feats["models"][m]["operational"]} for m in ROSTER}}
    (out_dir / "model_features.json").write_text(json.dumps(new_feats, indent=1), encoding="utf-8")

    for f in ("centroids.bin", "model_axes.json", "model_registry.json"):
        (out_dir / f).write_bytes((src_dir / f).read_bytes())

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
                                     (1 - a) * (1 - (costs[m] - cmin) / (cmax - cmin)), 6) for m in ROSTER}
    (out_dir / "rankings.json").write_text(json.dumps(
        {"meta": {"version": VERSION, "note": "blend parity artifact; not loaded in v2 bundles"},
         "rankings": rankings}, indent=1), encoding="utf-8")

    lines = (src_dir / "metadata.yaml").read_text(encoding="utf-8").splitlines(keepends=True)
    out_lines, i = [], 0
    while i < len(lines):
        out_lines.append(lines[i])
        if lines[i].strip() == "default_routing_knobs: &id001":
            i += 1
            out_lines.append(lines[i])  # "    alpha:" header
            i += 1
            while i < len(lines) and lines[i].lstrip().startswith("- "):
                i += 1
            out_lines.extend(f"    - {a}\n" for a in RECIPE["alpha"])
            continue
        i += 1
    meta = "".join(out_lines)
    meta = meta.replace("version: v1.0", "version: v1.0", 1)  # already v1.0
    (out_dir / "metadata.yaml").write_text(meta, encoding="utf-8")
    print(f"built {out_dir}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default=VERSION, help="source bundle carrying meta.v1_0_base_cells")
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()
    src = ART / args.src
    if not src.exists():
        ap.error(f"{src} does not exist")
    if args.apply:
        out = ART / VERSION
        if out.exists():
            tmp_parent = Path(tempfile.mkdtemp(dir=ART))  # same drive: files can replace atomically
            try:
                build(src, tmp_parent / VERSION)
                for f in out.iterdir():
                    f.unlink()
                for f in (tmp_parent / VERSION).iterdir():
                    f.replace(out / f.name)
            finally:
                shutil.rmtree(tmp_parent, ignore_errors=True)
            print(f"regenerated {out} in place")
        else:
            build(src, out)
        return
    with tempfile.TemporaryDirectory() as td:
        build(src, Path(td) / VERSION)
        print("dry run OK; rerun with --apply to regenerate artifacts/" + VERSION)


if __name__ == "__main__":
    main()
