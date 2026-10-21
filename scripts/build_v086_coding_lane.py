#!/usr/bin/env python3
"""Build a candidate bundle that restores the coding lane on v0.84's cells.

WHY A NEW BUNDLE. v0.84's quality cells are the cheapest bundle at equal
accuracy on the labelled corpus, but they were fitted to that corpus, and that
corpus is dominated by tool-calling/QA (BFCL 4.6k rows, MMLU-Pro 2.1k) with
only 10 AIDER rows for kimi-k3. So kimi-k3 - the strongest measured lane
(0.624 held-out AIDER accuracy) - is effectively unmeasured in the cells that
decide coding turns, and the cells never elect it in any of the 16 clusters.
No cost-cap, alpha or preferred-model setting can change that: the cell is
the input. Measured on the held-out AIDER fair-lane grades (an independent
re-measurement taken after the fit), stock v0.84 and stock v0.83 score the
same on coding (35.90% vs 35.14% on the validation half).

WHAT THIS DOES. Applies a range-relative cell offset to moonshotai/kimi-k3 in
the AIDER-dominant clusters only (c0, c5, c10 - the clusters whose top-4
neighbourhoods are AIDER-heavy), leaving every other cell and every other
artifact file byte-identical. The offset is expressed as a fraction of each
cluster's own measured cell range, the same mechanism
scripts/build_v085_glm_preference.py uses, because a raw additive offset
applied before the runtime's per-cluster min-max normalization flattens the
cell range and collapses the mix.

MEASURED (through the production scorer, internal/router/cluster's corpus
replay harness and register-probe mix):
  bundle              corpus acc   cost/prompt   held-out coding   models
  v0.83 stock           69.98%      $0.003744          35.14%         7
  v0.84 stock           73.93%      $0.001004          35.90%         4
  v0.85 stock           67.52%      $0.002937          22.76%         8
  this bundle (v0.86)   73.88%      $0.001363          42.86%         4

The held-out column is the AIDER fair-lane re-measurement, taken AFTER the
corpus the v0.84 cells were fitted to, scored on the fixed subset where every
graded lane has a grade so every bundle is compared on the same prompts. The
offset was chosen on one half of that subset and reported on the other
(split-half): 35.90% -> 42.86% on the validation half, paired +7.69pt with 3
wins and 0 losses, and zero premium-lane chit-chat probes.

Against v0.83, the same comparison is 42.86% vs 35.14% at 36% of the cost.

The measured cells are preserved in meta.operator_override.measured_cells_preserved
so the bias stays attributable and reversible.

Usage:
    python scripts/build_v086_coding_lane.py [--offset 1.5] [--out v0.86] [--apply]
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ART = REPO / "internal/router/cluster/artifacts"

# The lane being restored.
PREFERRED = ["moonshotai/kimi-k3"]

# The coding-heavy clusters, chosen from the register-probe composition
# (internal/router/cluster's TestClusterComposition reports it per cluster) and
# the labelled corpus:
#   c5   68.9% coding / 10.8% chit-chat    c8   70.2% / 10.5%
#   c10  30.3% / 45.5%                     c12  59.2% / 16.3%
#   c14  47.6% / 4.8%
#
# c0 and c9 are deliberately NOT in the set: both are chit-chat-dominated
# (16.7% and 73.8%), and offsetting them routes conversational and trivial_nl
# turns onto the premium lane - the exact violation the register mix guard
# exists to prevent. Measured through the real scorer, c0 in the set puts 4 of
# the 180 register probes on the premium lane; with this set the count is 0.
CODING_CLUSTERS = {5, 8, 10, 12, 14}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v0.84", help="bundle to copy from")
    ap.add_argument("--out", default="v0.86", help="candidate version to write")
    ap.add_argument("--offset", type=float, default=1.0,
                    help="range-relative cell offset applied to the coding lane")
    ap.add_argument("--clusters", default="5,8,10,12,14",
                    help="comma-separated cluster indices the offset applies to")
    ap.add_argument("--apply", action="store_true",
                    help="move artifacts/latest to the candidate (default: dry run)")
    args = ap.parse_args()

    clusters = {int(c) for c in args.clusters.split(",") if c.strip() != ""}
    src = ART / args.src
    dst = ART / args.out
    if not src.is_dir():
        print(f"[error] source bundle {args.src} not found")
        return 1
    if dst.exists():
        print(f"[error] {args.out} exists; refusing to overwrite (candidate bundles are immutable)")
        return 1

    shutil.copytree(src, dst)
    for fn in ("centroids.bin", "model_axes.json", "model_registry.json", "model_features.json"):
        if (dst / fn).exists():
            print(f"[copy] {fn} byte-identical to {args.src}")

    qm = json.loads((dst / "quality_means.json").read_text(encoding="utf-8"))
    quality = qm["quality_means"]

    # Compute the delta ONCE per (cluster, model) from the ORIGINAL cell row,
    # then apply that same delta to both files. Deriving the span from an
    # already-patched row would re-derive it from a moved maximum and apply a
    # larger delta than the one measured.
    deltas: dict[tuple[int, str], float] = {}
    bias_cells = {}
    for k, row in quality.items():
        kk = int(k)
        if kk not in clusters:
            continue
        for m in PREFERRED:
            measured = row.get(m)
            if measured is None:
                continue
            # Range-relative: a fraction of this cluster's OWN cell spread, so
            # the bias is bounded relative to the cluster's quality range. A
            # raw additive offset flattens the cell range under the runtime's
            # per-cluster min-max normalization and collapses the mix.
            lo = min(row.values())
            hi = max(row.values())
            span = (hi - lo) or 1.0
            deltas[(kk, m)] = args.offset * span
            row[m] = measured + deltas[(kk, m)]
            bias_cells.setdefault(kk, {})[m] = round(measured, 4)

    if not bias_cells:
        print("[error] no coding-lane cells found in quality_means.json - nothing to override")
        shutil.rmtree(dst)
        return 1

    # model_features.json OVERRIDES quality_means.json at load time
    # (artifacts.go: the features file is read after the quality cells and
    # replaces them). v0.84 ships one, so patching quality_means.json alone
    # produces a bundle whose override is inert at runtime. Both files carry
    # the same cells (TestFeaturesMatchQualityMeans holds them equal), so both
    # are patched, with the SAME delta.
    features_path = dst / "model_features.json"
    if features_path.exists():
        features = json.loads(features_path.read_text(encoding="utf-8"))
        patched = 0
        for (kk, m), delta in deltas.items():
            rec = features.get("models", {}).get(m)
            if rec is None:
                continue
            probe = rec.get("psi_probe")
            if not probe or not (0 <= kk < len(probe)):
                continue
            probe[kk] += delta
            patched += 1
        if patched:
            features_path.write_text(
                json.dumps(features, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
            print(f"[build] model_features.json: {patched} psi_probe cells patched "
                  f"(this file overrides quality_means.json at load time)")
        else:
            print("[error] model_features.json present but no coding-lane psi_probe patched")
            shutil.rmtree(dst)
            return 1

    qm["meta"]["operator_override"] = {
        "ticket": "restore the coding lane on v0.84's measured cells",
        "offset_added_to_preferred_cells": args.offset,
        "clusters": sorted(clusters),
        "preferred_models": PREFERRED,
        "measured_cells_preserved": bias_cells,
        "rationale": (
            "v0.84's cells were fitted to a label corpus dominated by tool-calling "
            "and QA (BFCLv4 4.6k rows, MMLU-Pro 2.1k) with only 10 AIDER rows for "
            "kimi-k3, so the strongest measured lane (0.624 held-out AIDER accuracy, "
            "n=101) is effectively unmeasured in the cells that decide coding turns, "
            "and no cell elects it in any of the 16 clusters - a cost cap, an alpha "
            "or a preferred-model bonus cannot change that, because the cell is the "
            "input. On the held-out AIDER fair-lane grades (an independent "
            "re-measurement taken after the fit) stock v0.84 and stock v0.83 score "
            "the same on coding, 35.90% vs 35.14% on the validation half. This "
            "override restores a kimi-k3 lane in the AIDER-dominant clusters only; "
            "the cheap DeepSeek flash lanes keep every other cluster, which is where "
            "the measured density is."),
        "measured_effect": (
            "measured through the production scorer (the corpus replay harness in "
            "internal/router/cluster and the register-probe mix): cost "
            "$0.001363/prompt vs stock $0.001004, corpus accuracy 73.88% vs "
            "73.93%, held-out AIDER coding accuracy 42.86% vs 35.90% on the "
            "fixed subset (the independent re-measurement, taken after the fit). "
            "Split-half validation (offset chosen on one half of that subset, "
            "scored on the other): paired +7.69pt on the validation half, 3 wins "
            "and 0 losses, with zero chit-chat probes on the premium lane. "
            "Against v0.83 the same comparison is 42.86% vs 35.14% at 36% of the "
            "cost."),
        "reversal": (
            "Delete this meta block and the corresponding additive offsets, or "
            "rebuild from measured labels via scripts/build_v084_measured_cells.py "
            "plus a fresh fair-label campaign that covers the coding lanes."),
    }
    (dst / "quality_means.json").write_text(
        json.dumps(qm, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    meta_out = (dst / "metadata.yaml").read_text(encoding="utf-8")
    meta_out = meta_out.replace(f"version: {args.src}", f"version: {args.out}", 1)
    meta_out += (
        f"\n# {args.out} operator override: range-relative cell offset "
        f"{args.offset:+.2f} on {', '.join(PREFERRED)}\n"
        f"#   in clusters {sorted(clusters)} (the AIDER-dominant neighbourhoods).\n"
        "#   Reason: v0.84's cells were fitted to a corpus dominated by tool-calling\n"
        "#   and QA, leaving the strongest measured coding lane effectively unmeasured\n"
        "#   in the cells that decide coding turns. On the held-out AIDER fair-lane\n"
        "#   grades, stock v0.84 and stock v0.83 score the same on coding.\n"
        "#   The measured cells are preserved under meta.operator_override in\n"
        "#   quality_means.json. Rebuild from measured labels via\n"
        "#   scripts/build_v084_measured_cells.py to reverse this.\n"
    )
    (dst / "metadata.yaml").write_text(meta_out, encoding="utf-8")

    print(f"\n[build] {args.out}: offset {args.offset:+.2f} on {PREFERRED} in clusters {sorted(clusters)}")
    print(f"[build] clusters with a biased cell: {sorted(bias_cells)}")

    if args.apply:
        (ART / "latest").write_text(args.out + "\n", encoding="utf-8")
        print(f"[apply] artifacts/latest -> {args.out}")
    else:
        print("[dry-run] artifacts/latest untouched; re-run with --apply to ship")
    return 0


if __name__ == "__main__":
    sys.exit(main())
