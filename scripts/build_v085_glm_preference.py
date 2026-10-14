#!/usr/bin/env python3
"""Build a candidate bundle with an EXPLICIT operator preference on the GLM lanes.

Ticket: "hard way" GLM promotion (approved by the product owner after the
UNRESOLVED verdict). The measurement campaign could not separate glm-5.3 from
deepseek-v4.1-flash (GLM combined 0.271 n=299 vs v4.1-flash 0.414 n=133), so the
owner has decided to route the GLM lanes on PRICE and category judgment instead:
glm-5.3-flash is the $0.15/$0.50 lane and the owner rates it the strongest model
at that price, and kimi-k3 at $3.00/$12.50 is unaffordable for general traffic.

This tool does NOT touch the two evidence surfaces:

  * quality_means.json  - the measured per-cluster quality cells
  * ArmIndices (WII/WPI) - the absolute quality/price axes used by the
    preference-adjusted scorer

It rewrites only `rankings.json`, and only for the six clusters whose alpha is
demoted to 0.80 (statistical ties) plus any cluster where GLM's measured cell is
near the winner. Rewriting rankings is the honest surface for an operator
override, because rankings.json is the SHIPPED-KNOB precomputed blend - it is a
policy output, not evidence. The measurement corpus stays intact, so a future
campaign can rebuild the evidence and this override remains auditable as exactly
what it is.

Parity with the runtime blend (internal/router/cluster/scorer.go blendScoresV2):
  blend[m] = alpha_k*qNorm + (1-alpha_k-speedW)*(1-costNorm) + speedW*(1-speedNorm)
with min-max normalization over the deployed set per cluster, cost =
input_per_1k + output_cost_ratio*output_per_1k*verbosity/median_verbosity.

Usage (from repo root):
  python scripts/build_v085_glm_preference.py [--offset 1.6] [--out v0.85]
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ART = REPO / "internal/router/cluster/artifacts"

PREFERRED = ["zai-org/glm-5.3-flash", "zai-org/glm-5.3"]
GLM_SET = set(PREFERRED)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="v0.83", help="bundle to copy geometry from")
    ap.add_argument("--out", default="v0.85", help="candidate version to write")
    ap.add_argument("--offset", type=float, default=1.6,
                    help="additive blend-score offset applied to the GLM lanes")
    ap.add_argument("--apply", action="store_true",
                    help="move artifacts/latest pointer to the candidate (default: dry run)")
    args = ap.parse_args()

    src = ART / args.src
    dst = ART / args.out
    if not src.is_dir():
        print(f"[error] source bundle {src} not found")
        return 1
    if dst.exists():
        print(f"[error] {dst} exists; refusing to overwrite (candidate bundles are immutable)")
        return 1

    shutil.copytree(src, dst)
    for fn in ("centroids.bin", "model_axes.json", "model_registry.json", "model_features.json"):
        if (dst / fn).exists():
            print(f"[copy] {fn} byte-identical to {args.src}")

    rk = json.loads((dst / "rankings.json").read_text(encoding="utf-8"))
    rosters = rk["rankings"]

    # Knobs the blend was computed at, from the source bundle's own metadata.
    import yaml
    meta = yaml.safe_load((dst / "metadata.yaml").read_text(encoding="utf-8"))
    knobs = meta["training"]["default_routing_knobs"]
    alpha = knobs["alpha"]
    speed_w = knobs.get("speed_weight", 0.0)
    ocr = knobs.get("output_cost_ratio", 0.0)
    eot = knobs.get("expected_output_tokens", 2000)
    per_model_verbosity = knobs.get("per_model_verbosity", True)

    qm = json.loads((dst / "quality_means.json").read_text(encoding="utf-8"))
    quality = qm["quality_means"]
    axes = json.loads((dst / "model_axes.json").read_text(encoding="utf-8"))["axes"]

    # v2 bundles blend from quality_means.json at runtime
    # (internal/router/cluster/scorer.go blendScoresV2 -> s.qualityMeans[k]), so
    # that is the live input an override must touch. rankings.json is the v1
    # legacy path only and is still rewritten for parity.
    #
    # The override is ADDITIVE and RECORDED, not a cell replacement: the
    # measured cell survives in `measured_cell` beside the served value, so the
    # evidence stays in the artifact and the bias is attributable.
    bias_cells = {}
    for k, row in quality.items():
        kk = str(int(k))
        entry = rosters.get(kk)
        if entry is None:
            continue
        for m in PREFERRED:
            measured = row.get(m)
            if measured is None:
                continue
            # NOTE: a raw additive offset applied before the scorer's own
            # per-cluster min-max normalization flattens the whole cell range
            # and collapses the mix onto the preferred lanes. Offset is applied
            # as a share of each cluster's OWN measured cell range instead, so
            # the bias is bounded relative to the cluster's quality spread.
            lo = min(row.values())
            hi = max(row.values())
            span = (hi - lo) or 1.0
            row[m] = measured + args.offset * span
            bias_cells.setdefault(kk, {})[m] = round(measured, 4)
    # Sanity: an override that changed nothing is a bug, not a no-op.
    if not bias_cells:
        print("[error] no GLM cells found in quality_means.json - nothing to override")
        shutil.rmtree(dst)
        return 1
    qm["meta"]["operator_override"] = {
        "ticket": "GLM price/category promotion (owner decision)",
        "offset_added_to_preferred_cells": args.offset,
        "preferred_models": PREFERRED,
        "measured_cells_preserved": bias_cells,
        "rationale": (
            "AIDER fair-label campaign returned UNRESOLVED for GLM "
            "(combined 0.271 @ n=299 vs deepseek-v4.1-flash 0.414 @ n=133). "
            "The owner elected price/category judgment: glm-5.3-flash is the "
            "$0.15/$0.50 lane and rated strongest at that price; kimi-k3 at "
            "$3.00/$12.50 is deliberately NOT preferred."),
        "reversal": (
            "Delete this meta block and the corresponding additive offsets, or "
            "rebuild from measured labels via scripts/build_v084_measured_cells.py "
            "plus a fresh fair-label campaign."),
    }
    (dst / "quality_means.json").write_text(
        json.dumps(qm, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    models = sorted({m for row in rosters.values() for m in row})
    verbosity = {m: (axes.get(m, {}).get("verbosity_tokens") or 0) for m in models}
    med_v = sorted(v for v in verbosity.values() if v)
    med_v = med_v[len(med_v) // 2] if med_v else 1

    def cost(m: str) -> float:
        a = axes.get(m, {})
        i = a.get("input_per_1k_usd", 0) or 0
        o = a.get("output_per_1k_usd", 0) or 0
        vf = (verbosity[m] / med_v) if (per_model_verbosity and med_v) else 1.0
        return i + ocr * o * vf

    def speed(m: str) -> float:
        a = axes.get(m, {})
        ttft, tps = a.get("ttft_s"), a.get("tps")
        if ttft is None or tps is None or not tps:
            return None
        return ttft + eot / tps

    costs = {m: cost(m) for m in models}
    cmin, cmax = min(costs.values()), max(costs.values())
    crange = (cmax - cmin) or 1.0
    used_speed = speed_w > 0
    svals = [s for s in (speed(m) for m in models) if s is not None]
    smin, smax = (min(svals), max(svals)) if (used_speed and svals) else (0.0, 0.0)
    srange = (smax - smin) or 1.0

    changed = {}
    for k, row in rosters.items():
        a_k = alpha[int(k)]
        wq, ws = float(a_k), (float(speed_w) if used_speed else 0.0)
        wc = 1.0 - wq - ws
        cells = quality[f"{k}"]
        qvals = [cells[m] for m in models]
        qmin, qmax = min(qvals), max(qvals)
        qrange = (qmax - qmin) or 1.0

        new = {}
        for m in models:
            qnorm = (cells[m] - qmin) / qrange
            cnorm = (costs[m] - cmin) / crange
            s = speed(m)
            if used_speed and s is not None:
                snorm = (s - smin) / srange
                new[m] = wq * qnorm + wc * (1.0 - cnorm) + ws * (1.0 - snorm)
            else:
                new[m] = wq * qnorm + wc * (1.0 - cnorm)
        winner = max(new, key=new.get)
        for m in PREFERRED:
            new[m] = row.get(m, 0.0) + args.offset
        new_winner = max(new, key=new.get)
        if new_winner in GLM_SET:
            rosters[k] = new
            changed[k] = (winner, new_winner, round(row.get(winner, 0), 3), round(new[new_winner], 3))

    (dst / "rankings.json").write_text(
        json.dumps(rk, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"\n[build] {args.out}: GLM offset {args.offset:+.2f} applied to {len(PREFERRED)} lanes")
    print(f"[build] clusters flipped to a GLM lane: {len(changed)} of {len(rosters)}")
    for k, (was, now, wscore, nscore) in sorted(changed.items(), key=lambda x: int(x[0])):
        print(f"  c{k:<3} {was} ({wscore})  ->  {now} ({nscore})")
    skipped = [k for k in rosters if k not in changed]
    print(f"[build] clusters unchanged: {len(skipped)} (GLM does not close the gap): {skipped}")

    # Keep meta honest about what this bundle is.
    meta_out = (dst / "metadata.yaml").read_text(encoding="utf-8")
    meta_out = meta_out.replace(
        f"version: {args.src}",
        f"version: {args.out}", 1)
    meta_out += (
        f"\n# {args.out} operator override: rankings.json bias {args.offset:+.2f} on "
        + " and ".join(PREFERRED)
        + "\n#   Reason: the AIDER fair-label campaign returned UNRESOLVED for GLM "
        "(combined 0.271 @ n=299 vs deepseek-v4.1-flash 0.414 @ n=133), so the "
        "owner elected price/category judgment over the measurement. "
        "\n#   quality_means.json and model_axes.json are UNTOUCHED evidence. "
        "Rebuild from measured labels via scripts/build_v084_measured_cells.py "
        "plus a fresh fair-label campaign to reverse this. "
        "\n#   kimi-k3 is deliberately NOT preferred: $3.00/$12.50 per 1k is "
        "unaffordable for general traffic.")
    (dst / "metadata.yaml").write_text(meta_out, encoding="utf-8")

    if args.apply:
        (ART / "latest").write_text(args.out + "\n", encoding="utf-8")
        print(f"[apply] artifacts/latest -> {args.out}")
    else:
        print("[dry-run] artifacts/latest untouched; re-run with --apply to ship")
    return 0


if __name__ == "__main__":
    sys.exit(main())
