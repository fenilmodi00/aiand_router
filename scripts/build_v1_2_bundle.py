#!/usr/bin/env python3
"""Build the v1.2 cluster bundle from v1.1 (roster refresh: flash-1017 swap +
kimi-k3.1-pro inert onboarding).

v1.2 = v1.1 with two roster swaps and no retraining:
  - deepseek/deepseek-v4.1-flash   -> deepseek/deepseek-v4.1-flash-1017
    (proxy: true; quality column cloned from the v4.1-flash column — same
    v4.1 line, newer 1017 weights, identical price; takes c1 by the
    same-column swap, mirroring the v0.75 opus-5 same-line-swap precedent)
  - moonshotai/kimi-k3             -> moonshotai/kimi-k3.1-pro
    (proxy: true; roster-listed-but-inert day-0: quality column = kimi-k3's
    v1.1 column, its direct predecessor — the same prior lineage muse-spark
    already uses — and the operational block, cost axis included, is borrowed
    from kimi-k3 wholesale. Wins ZERO clusters.)

Both borrows are the day-0 no-extrapolation rule: a new model enters at its
predecessor's prior and at most ties the incumbent winners; published
leaderboard scores (e.g. AA) never move quality cells until a measured probe
grade exists (the sonnet-5 precedent). Borrowing the cost axis wholesale also
keeps the blend's cost-normalization span identical to v1.1, so routing is
bit-identical to v1.1 — zero probe flips. When k3.1-pro is re-anchored with
real grades + its real prices ($2/$10 per 1M), the span changes and lanes may
legitimately flip then; that re-anchor is gated on the #96 measurement
campaign. Catalog pricing (the billing source) carries the real $2/$10.

centroids.bin is copied byte-identical from v1.1 (sha256 recorded in
metadata.yaml). The per-cluster alpha knobs are unchanged.

Usage: python scripts/build_v1_2_bundle.py [--src v1.1] [--apply]
Defaults to a dry run; --apply writes artifacts/v1.2.
"""
import argparse
import hashlib
import json
import shutil
import tempfile
from pathlib import Path

ART = Path(__file__).resolve().parent.parent / "internal/router/cluster/artifacts"
VERSION = "v1.2"

OLD_FLASH = "deepseek/deepseek-v4.1-flash"
NEW_FLASH = "deepseek/deepseek-v4.1-flash-1017"
OLD_KIMI = "moonshotai/kimi-k3"
NEW_KIMI = "moonshotai/kimi-k3.1-pro"
GLM53 = "z-ai/glm-5.3"

# Per-cluster default alpha (metadata default_routing_knobs.alpha; unchanged).
ALPHA = [0.80, 0.96, 0.80, 0.80, 0.96, 0.96, 0.96, 0.96,
         0.80, 0.96, 0.96, 0.96, 0.80, 0.96, 0.96, 0.96]


def swap_quality_row(cells):
    """Rewrite one 16-cell quality block onto the v1.2 roster."""
    out = {}
    for c in map(str, range(16)):
        row = {}
        for m, v in cells[c].items():
            if m == OLD_FLASH:
                row[NEW_FLASH] = v
            elif m == OLD_KIMI:
                row[NEW_KIMI] = v
            else:
                row[m] = v
        out[c] = row
    return out


def compute_rankings(cells, ops):
    """Blend parity artifact: alpha * row-normalized quality + (1-alpha) *
    (1 - min-max-normalized cost), rounded to 6dp — the formula
    scripts/build_v1_0_cells.py bakes into rankings.json."""
    costs = {m: o["input_per_1k_usd"] + 0.25 * o["output_per_1k_usd"]
             for m, o in ops.items()}
    cmin, cmax = min(costs.values()), max(costs.values())
    rankings = {}
    for c in map(str, range(16)):
        row = cells[c]
        qmin, qmax = min(row.values()), max(row.values())
        span = qmax - qmin or 1.0
        a = ALPHA[int(c)]
        rankings[c] = {
            m: round(a * (row[m] - qmin) / span +
                     (1 - a) * (1 - (costs[m] - cmin) / (cmax - cmin)), 6)
            for m in row
        }
    return rankings


def build(src_dir: Path, out_dir: Path) -> None:
    out_dir.mkdir(parents=True, exist_ok=False)

    qm = json.loads((src_dir / "quality_means.json").read_text(encoding="utf-8"))
    feats = json.loads((src_dir / "model_features.json").read_text(encoding="utf-8"))
    axes = json.loads((src_dir / "model_axes.json").read_text(encoding="utf-8"))
    reg = json.loads((src_dir / "model_registry.json").read_text(encoding="utf-8"))

    # --- quality_means.json ---
    cells = swap_quality_row(qm["quality_means"])
    qm["quality_means"] = cells
    qm["meta"]["v1_2_derivation"] = {
        "version": VERSION,
        "parent": "v1.1",
        "swapped": [OLD_FLASH, OLD_KIMI],
        "added": [NEW_FLASH, NEW_KIMI],
        "flash_clone_from": OLD_FLASH,
        "k31_base_column": OLD_KIMI,
        "k31_operational_borrowed_from": OLD_KIMI,
        "k31_wins_clusters": [],
        "tool": "build_v1_2_bundle.py",
        "date": "2026-11-05",
        "note": (
            "v1.2 = v1.1 roster swap, no retraining. 1017 flash column cloned "
            "from deepseek/deepseek-v4.1-flash (same v4.1 line, newer weights, "
            "identical price; takes c1 by the same-column swap). "
            "kimi-k3.1-pro is roster-listed-but-inert day-0: quality column "
            "and the whole operational block (cost axis included) borrowed "
            "from kimi-k3, its direct predecessor, per the day-0 "
            "no-extrapolation rule (a new model at most ties the best "
            "existing coder until a measured probe grade exists). Wins zero "
            "clusters; routing is bit-identical to v1.1. Re-anchor gated on "
            "measured grades (#96). Centroids, embedder and the per-cluster "
            "alpha knobs are unchanged."
        ),
    }
    (out_dir / "quality_means.json").write_text(json.dumps(qm, indent=1), encoding="utf-8")

    # --- model_features.json ---
    new_models = {}
    for m, rec in feats["models"].items():
        if m == OLD_FLASH or m == OLD_KIMI:
            continue
        new_models[m] = rec
    new_models[NEW_FLASH] = {
        "psi_probe": [cells[str(c)][NEW_FLASH] for c in range(16)],
        "operational": feats["models"][OLD_FLASH]["operational"],
    }
    new_models[NEW_KIMI] = {
        "psi_probe": [cells[str(c)][NEW_KIMI] for c in range(16)],
        "operational": feats["models"][OLD_KIMI]["operational"],
    }
    (out_dir / "model_features.json").write_text(
        json.dumps({"models": new_models}, indent=1), encoding="utf-8")

    # --- model_axes.json (loader fail-fasts on a registry model missing here) ---
    new_axes = {}
    for m, a in axes["axes"].items():
        if m == OLD_FLASH or m == OLD_KIMI:
            continue
        new_axes[m] = a
    new_axes[NEW_FLASH] = dict(axes["axes"][OLD_FLASH])
    new_axes[NEW_KIMI] = dict(axes["axes"][OLD_KIMI])
    axes["axes"] = new_axes
    (out_dir / "model_axes.json").write_text(json.dumps(axes, indent=1), encoding="utf-8")

    # --- model_registry.json (swap entries in place, preserving order) ---
    new_entries = []
    for e in reg["deployed_models"]:
        if e["model"] == OLD_FLASH:
            e = dict(e)
            e["model"] = NEW_FLASH
            e["proxy"] = True
            e["proxy_note"] = (
                "Clone of the deepseek/deepseek-v4.1-flash column: same v4.1 "
                "line, newer 1017 weights, identical price ($0.30/$0.60 per 1M)."
            )
            e["research_note"] = "AA 39 (artificialanalysis.ai, 2026-10-23)."
            e["research_date"] = "2026-11-05"
        elif e["model"] == OLD_KIMI:
            e = dict(e)
            e["model"] = NEW_KIMI
            e["proxy"] = True
            e["proxy_note"] = (
                "Day-0 inert prior: quality column and operational block "
                "(cost axis included) cloned from kimi-k3, its direct "
                "predecessor; wins no cluster until re-anchored on measured "
                "grades."
            )
            e["research_note"] = (
                "AA II 55, Terminal-Bench 4.0 67% (best of any model), "
                "GDPval-AA 1846 Elo (artificialanalysis.ai, 2026-10-23) — "
                "recorded, not applied; published scores never move quality "
                "cells day-0."
            )
            e["research_date"] = "2026-11-05"
        new_entries.append(e)
    reg["deployed_models"] = new_entries
    reg["meta"]["comment"] = (
        "v1.2 \u2014 v1.1 roster with deepseek/deepseek-v4.1-flash swapped for "
        "deepseek/deepseek-v4.1-flash-1017 (column clone) and moonshotai/kimi-k3 "
        "swapped for moonshotai/kimi-k3.1-pro (inert kimi-k3 predecessor "
        "prior; wins no cluster); meta/muse-spark stays on the kimi-k3 proxy "
        "prior (pending re-anchor). See quality_means meta.v1_2_derivation."
    )
    (out_dir / "model_registry.json").write_text(json.dumps(reg, indent=1), encoding="utf-8")

    # --- rankings.json (parity artifact; values byte-equal to v1.1 modulo rename) ---
    ops = {m: rec["operational"] for m, rec in new_models.items()}
    rankings = compute_rankings(cells, ops)
    (out_dir / "rankings.json").write_text(json.dumps(
        {"meta": {"version": VERSION, "note": "blend parity artifact; not loaded in v2 bundles"},
         "rankings": rankings}, indent=1), encoding="utf-8")

    # --- centroids.bin: byte-identical copy ---
    (out_dir / "centroids.bin").write_bytes((src_dir / "centroids.bin").read_bytes())

    # --- metadata.yaml (text-surgical edit to preserve everything else) ---
    src_cent_sha = hashlib.sha256((src_dir / "centroids.bin").read_bytes()).hexdigest()
    out_cent_sha = hashlib.sha256((out_dir / "centroids.bin").read_bytes()).hexdigest()
    assert src_cent_sha == out_cent_sha, "centroid copy diverged"

    meta = (src_dir / "metadata.yaml").read_text(encoding="utf-8")
    meta = meta.replace("version: v1.1\nparent: v1.0\n", "version: v1.2\nparent: v1.1\n", 1)
    meta = meta.replace("- deepseek-ai/deepseek-v4.1-flash\n", "- deepseek-ai/deepseek-v4.1-flash-1017\n", 1)
    meta = meta.replace("- moonshotai/kimi-k3\n", "- moonshotai/kimi-k3.1-pro\n", 1)
    meta = meta.replace("  deepseek-ai/deepseek-v4.1-flash: 0.0003\n",
                        "  deepseek-ai/deepseek-v4.1-flash-1017: 0.0003\n", 1)
    meta = meta.replace("  moonshotai/kimi-k3: 0.003\n", "  moonshotai/kimi-k3.1-pro: 0.003\n", 1)

    v12_changelog = (
        "v1.2 = v1.1 roster swap, no retraining, routing bit-identical to "
        "v1.1 (zero probe flips): deepseek/deepseek-v4.1-flash -> "
        "deepseek/deepseek-v4.1-flash-1017 (proxy: true; quality column "
        "cloned from the v4.1-flash column — same v4.1 line, newer 1017 "
        "weights, identical $0.30/$0.60 pricing; day-0 clone, mirrors the "
        "v1.0 v4.1-flash entry shape; takes c1 by the same-column swap, "
        "mirroring the v0.75 opus-5 same-line-swap precedent) and "
        "moonshotai/kimi-k3 -> moonshotai/kimi-k3.1-pro (proxy: true; "
        "roster-listed-but-inert day-0 — quality column AND the whole "
        "operational block, cost axis included, cloned from kimi-k3, its "
        "direct predecessor, the same prior lineage muse-spark uses; wins "
        "zero clusters). Day-0 no-extrapolation rule: a new model at most "
        "ties the best existing coder; published leaderboard scores never "
        "move quality cells until a measured probe grade exists (the "
        "sonnet-5 measured-probe precedent). Recorded, not applied: "
        "kimi-k3.1-pro AA II 55, Terminal-Bench 4.0 67% (best of any model), "
        "GDPval-AA 1846 Elo (artificialanalysis.ai, 2026-10-23). Borrowed "
        "axes: kimi-k3.1-pro tps/ttft/verbosity AND the bundle cost axis "
        "(0.003/0.0125 per 1k prior) are kimi-k3 v1.1 values; catalog "
        "pricing is the real $2/$10 per 1M (cache 0.2x, billing source) — "
        "the bundle cost axis is a prior, not a price. When re-anchored with "
        "real grades + prices the cost span changes and lanes may "
        "legitimately flip; that re-anchor is gated on measured grades "
        "(#96). muse-spark stays in the pool on the kimi-k3 proxy prior, "
        "now pending re-anchor (its prior is known-stale after the kimi-k3 "
        "demotion). centroids.bin byte-identical to v1.1 (sha256 "
        f"{out_cent_sha}); embedder contract and per-cluster "
        "default_routing_knobs unchanged. Old flash and kimi-k3 are "
        "pin-only in the catalog: force-dispatchable, never auto-routed. "
        "\n  "
    )
    marker = "changelog: '"
    idx = meta.index(marker)
    meta = meta[:idx] + marker + v12_changelog + meta[idx + len(marker):]
    (out_dir / "metadata.yaml").write_text(meta, encoding="utf-8")

    print(f"built {out_dir}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v1.1", help="source bundle to derive from")
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()
    src = ART / args.src
    if not src.exists():
        ap.error(f"{src} does not exist")
    if args.apply:
        out = ART / VERSION
        if out.exists():
            ap.error(f"{out} already exists; bundles are write-once")
        build(src, out)
        return
    with tempfile.TemporaryDirectory() as td:
        build(src, Path(td) / VERSION)
        print("dry run OK; rerun with --apply to write artifacts/" + VERSION)


if __name__ == "__main__":
    main()
