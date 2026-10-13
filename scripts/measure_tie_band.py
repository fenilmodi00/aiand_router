#!/usr/bin/env python3
"""Measure upstream tie-band quality exploration over the labelled corpus.

Ticket #93. Reproduces the exploration decorator (internal/router/
banditexplore/explorer.go) in simulation over every labelled corpus prompt
that has a cluster assignment, for band widths 0.00 / 0.05 / 0.10, and reports
the ticket's table:

    band_width | expected accuracy | cost per prompt | reachable models

Method (mirrors the live path exactly):
  1. Load .context/label-runs/v082-expanded-labels.jsonl (per-(prompt, model)
     measured ground-truth rows) and prompt-clusters-v084.json (cluster
     assignment per prompt = the live top-1 cosine route, so no embedder is
     needed; refreshed by scripts/refresh_cluster_assignments.py).
  2. Recompute the blend exactly as internal/router/cluster/scorer.go
     blendScoresV2 does, over the cells and axes in
     internal/router/cluster/artifacts/v0.84/. Every knob is loaded from that
     artifact's metadata.yaml default_routing_knobs (the same source the scorer
     boots with) — currently alpha 0.96 (0.80 on the four measurement-demoted
     clusters), speed_weight 0, output_cost_ratio 0.25,
     expected_output_tokens 2000, per_model_verbosity true:
       cost[m] = input_per_1k + output_cost_ratio * output_per_1k * verbosity
                 / median_verbosity   (model_axes.json field verbosity_tokens;
                 median taken over all axis rows, matching the Go median)
       score[m] = alpha[k]*qNorm[m] + (1-alpha[k])*(1-cNorm[m])   (top-P = 1;
                 with speed_weight = 0 the Go speed branch is dead)
     with qNorm/cNorm normalized over the deployed pool (s.models in Go), the
     same 8 roster models the explorer samples among (all aiand-served).
  3. For each prompt, the explorer's band = every servable roster model with
     score within bandWidth of the argmax. Uniform sampling among band members:
     each member is charged exactly 1/|band| (an exact split, not Monte-Carlo),
     which is the estimator's expectation, so the result is stable across runs
     and seeds (--seed is kept for the ticket's fixed-seed wording).
  4. Ground truth per (prompt, model) is that model's measured row: score is
     the true accuracy, cost is the measured per-prompt USD burn. Expected
     accuracy = Σ_band (1/|band|) * true score; cost per prompt = the same
     weighting of measured cost; prompts where a model is unscored contribute
     only a scaled weight (never NaN). Reusable for tuning: reachable counts
     how many of the 8 models are inside some band for some prompt.

Measured result (band widths 0.00 / 0.05 / 0.10, 1682 cluster-assigned
labelled prompts of 1963, exact uniform band split, fixed seed 93), pinned to
artifact fingerprints metadata.yaml=8f299ddec956c7ec, model_axes.json=
cd94bd60f39f4189, quality_means.json=38278361de1b51ee (re-run after any
artifact write; the script prints the fingerprints every run):

    band_width | expected accuracy | cost/prompt | reachable of 8
              0.00 |  0.729700       | $0.002474   |   1 (argmax only)
              0.05 |  0.728496       | $0.002287   |   3
              0.10 |  0.726765       | $0.002367   |   5

    mean band size: 1.17 (0.05) / 1.48 (0.10); singleton bands — where the
    exploration policy is a no-op: 83.3% (0.05) / 66.2% (0.10). At the default
    half-width 0.05, only about 1 in 6 prompts explores at all, and the
    expected accuracy delta is -0.0012 while cost/prompt moves -7% (the
    in-band peers happen to be marginally cheaper than the argmax on this
    corpus): the band members are near-equivalent on the blend, so exploration
    buys the propensity-logged trajectories the off-policy estimator needs at
    negligible expected-accuracy cost. 0.10 costs a further -0.0017 accuracy
    for 5-of-8 reach — a widening only a coverage-driven bake-off justifies.
"""
from __future__ import annotations

import argparse
import json
import random
import statistics
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v084.json"  # live cluster ids (refreshed by scripts/refresh_cluster_assignments.py); the v082 cache is stale — 150/1682 entries disagree with the live top-1 cosine route
ART = REPO / "internal/router/cluster/artifacts/v0.84"

# The 8-roster deployed models (artifacts/v0.84/model_registry.json
# deployed_models) — the servable set behind default_provider aiand, i.e. the
# pool the explorer samples among.
SERVABLE = [
    "deepseek-ai/deepseek-v4-pro",
    "deepseek-ai/deepseek-v4-flash",
    "deepseek-ai/deepseek-v4.1-flash",
    "zai-org/glm-5.3",
    "zai-org/glm-5.3-flash",
    "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3",
    "motif-technologies/motif-3",
]

# Fallbacks matching artifacts/v0.84/metadata.yaml default_routing_knobs
# (scored blend: alpha 0.96 / 0.80 cluster demotions, speed_weight 0,
# output_cost_ratio 0.25, expected_output_tokens 2000, per_model_verbosity true).
# The live values are always read from metadata.yaml when present — these
# constants only stand in for a missing/failed yaml read.
DEFAULT_ALPHA = [
    0.96, 0.96, 0.8, 0.8, 0.8, 0.96, 0.8, 0.96,
    0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96,
]
DEFAULT_SPEED_WEIGHT = 0.0
DEFAULT_OUTPUT_COST_RATIO = 0.25
DEFAULT_EXPECTED_OUTPUT_TOKENS = 2000
DEFAULT_PER_MODEL_VERBOSITY = True


def load_knobs() -> dict:
    """Every blend knob from metadata.yaml's default_routing_knobs — the same
    source the scorer boots with. Falls back to the constants above."""
    out = {
        "alpha": list(DEFAULT_ALPHA),
        "speed_weight": DEFAULT_SPEED_WEIGHT,
        "output_cost_ratio": DEFAULT_OUTPUT_COST_RATIO,
        "expected_output_tokens": DEFAULT_EXPECTED_OUTPUT_TOKENS,
        "per_model_verbosity": DEFAULT_PER_MODEL_VERBOSITY,
    }
    try:
        import yaml
        meta = yaml.safe_load((ART / "metadata.yaml").read_text(encoding="utf-8"))
        knobs = (meta.get("training") or {}).get("default_routing_knobs") or {}
        if knobs.get("alpha"):
            out["alpha"] = [float(a) for a in knobs["alpha"]]
        out["speed_weight"] = float(knobs.get("speed_weight", out["speed_weight"]))
        out["output_cost_ratio"] = float(knobs.get("output_cost_ratio", out["output_cost_ratio"]))
        out["expected_output_tokens"] = int(knobs.get("expected_output_tokens", out["expected_output_tokens"]))
        out["per_model_verbosity"] = bool(knobs.get("per_model_verbosity", out["per_model_verbosity"]))
    except Exception as exc:  # noqa: BLE001 - measurement script, loud is fine
        print(f"warning: falling back to hardcoded knobs ({exc})", file=sys.stderr)
    return out


def load_rows() -> tuple[list[dict], dict[str, int]]:
    rows = []
    with LABELS.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    assign = json.loads(ASSIGN.read_text(encoding="utf-8"))
    return rows, assign


def load_artifacts() -> tuple[dict[str, dict[str, float]], dict[str, dict], float]:
    """quality_means[k][model], axes[model], median verbosity."""
    qm = json.loads((ART / "quality_means.json").read_text(encoding="utf-8"))
    quality = {int(k): v for k, v in qm["quality_means"].items()}
    axes = json.loads((ART / "model_axes.json").read_text(encoding="utf-8"))["axes"]
    verbs = sorted(v["verbosity_tokens"] for v in axes.values()
                   if v.get("verbosity_tokens") is not None)
    n = len(verbs)
    med = (verbs[n // 2] if n % 2 else (verbs[n // 2 - 1] + verbs[n // 2]) / 2.0) if n else 0.0
    return quality, axes, med


def blend(quality, axes, med, top_cluster: int, knobs: dict) -> dict[str, float]:
    """blendScoresV2 parity for one cluster over the 8-model roster.

    cost[m] = input_per_1k + output_cost_ratio * output_per_1k * verbosity
    / median_verbosity (per_model_verbosity). With speed_weight = 0 the Go code
    takes the no-speed branch and redistributes the whole weight into wQ/wC;
    a nonzero speed_weight adds the TTFT+TPS term below, same as the Go path.
    """
    qm_row = quality[top_cluster]
    output_cost_ratio = knobs["output_cost_ratio"]
    per_model_verbosity = knobs["per_model_verbosity"]
    costs = {}
    speeds = {}
    for m in SERVABLE:
        axis = axes[m]
        v_factor = 1.0
        if per_model_verbosity and axis.get("verbosity_tokens") is not None and med > 0:
            v_factor = axis["verbosity_tokens"] / med
        costs[m] = (axis.get("input_per_1k_usd") or 0.0) \
            + output_cost_ratio * (axis.get("output_per_1k_usd") or 0.0) * v_factor
        if axis.get("tps") and axis["tps"] > 0:
            speeds[m] = (axis.get("ttft_s") or 0.0) + knobs["expected_output_tokens"] / axis["tps"]
        else:
            speeds[m] = None

    # Normalization pools: qMin/qMax and cost min/max over the deployed pool —
    # the runtime iterates s.models (deployed) for both, same as here.
    qvals = [qm_row[m] for m in SERVABLE]
    qmin, qmax = min(qvals), max(qvals)
    qrange = qmax - qmin
    cmin, cmax = min(costs[m] for m in SERVABLE), max(costs[m] for m in SERVABLE)
    crange = cmax - cmin
    timed = [speeds[m] for m in SERVABLE if speeds[m] is not None]
    srange = 0.0
    if knobs["speed_weight"] > 0 and timed:
        smin, smax = min(timed), max(timed)
        srange = smax - smin

    w_q = knobs["alpha"][top_cluster]
    w_s = knobs["speed_weight"] if srange > 0 else 0.0
    w_c = 1.0 - w_q - w_s
    scores = {}
    for m in SERVABLE:
        q_norm = (qm_row[m] - qmin) / qrange if qrange > 0 else 0.0
        c_norm = (costs[m] - cmin) / crange if crange > 0 else 0.0
        if srange > 0:
            # Untimed peers count as worst-case speed (sNorm=1), mirroring the Go path.
            s_norm = 1.0 if speeds[m] is None else (speeds[m] - smin) / srange
            score = w_q * q_norm + w_c * (1.0 - c_norm) + w_s * (1.0 - s_norm)
        else:
            total = w_q + w_c
            score = (w_q / total) * q_norm + (w_c / total) * (1.0 - c_norm) if total > 0 else q_norm
        scores[m] = score
    return scores


def artifact_fingerprint() -> dict[str, str]:
    """sha256 of the files the measurement reads, so a table always carries the
    exact artifact state it was computed over."""
    import hashlib
    out = {}
    for name in ("quality_means.json", "model_axes.json", "metadata.yaml"):
        out[name] = hashlib.sha256((ART / name).read_bytes()).hexdigest()[:16]
    return out


def measure(band_widths, n_samples, seed, verbose=True):
    rows, assign = load_rows()
    quality, axes, med = load_artifacts()
    knobs = load_knobs()

    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    prompts = sorted(p for p in by_prompt if p in assign)
    missing = sorted(p for p in by_prompt if p not in assign)
    if verbose:
        print(f"artifact fingerprints: {json.dumps(artifact_fingerprint(), sort_keys=True)}")
        print(f"labels: {len(rows)} rows, {len(by_prompt)} prompts; "
              f"{len(prompts)} cluster-assigned, {len(missing)} unassigned")
        print(f"median verbosity tokens: {med:.2f}")
        print(f"knobs: alpha={knobs['alpha']} speed_weight={knobs['speed_weight']} "
              f"output_cost_ratio={knobs['output_cost_ratio']} "
              f"expected_output_tokens={knobs['expected_output_tokens']} "
              f"per_model_verbosity={knobs['per_model_verbosity']}")

    # Per-prompt blend scores (cache; identical for every band width).
    blend_cache: dict[str, dict[str, float]] = {}
    for p in prompts:
        blend_cache[p] = blend(quality, axes, med, assign[p], knobs)

    results = {}
    for width in band_widths:
        if width == 0.0:
            # Pure pass-through: the explorer never leaves the argmax.
            acc_num = cost_num = w_sum = 0.0
            band_sizes = []
            for p in prompts:
                scores = blend_cache[p]
                argmax_m = max(scores, key=lambda m: (scores[m], [i for i, s in enumerate(SERVABLE) if s == m][0]))
                row = by_prompt[p].get(argmax_m)
                if row is None:
                    continue
                wt = 1.0
                acc_num += wt * row["score"]
                cost_num += wt * row["cost"]
                w_sum += wt
                band_sizes.append(1)
            results[width] = {
                "accuracy": acc_num / w_sum,
                "cost": cost_num / w_sum,
                "reachable": 1,
                "mean_band": statistics.fmean(band_sizes),
                "singleton_share": 1.0,
            }
            continue

        acc_num = cost_num = w_sum = 0.0
        band_sizes = []
        reachable: set[str] = set()
        # prop per model: number of sampled draws that landed on it (for a
        # usage histogram, purely informational).
        for p in prompts:
            scores = blend_cache[p]
            top = max(scores.values())
            band = sorted(m for m in SERVABLE if scores[m] >= top - width)
            band_sizes.append(len(band))
            reachable.update(band)
            if len(band) == 1:
                acc_row = by_prompt[p].get(band[0])
                if acc_row is not None:
                    acc_num += acc_row["score"]
                    cost_num += acc_row["cost"]
                    w_sum += 1.0
                continue
            # n_samples uniform draws among band members; each band member gets
            # n/|band| draws in expectation — the integer split charges every
            # member exactly n/|band|, so the estimator is exact, no Monte-Carlo
            # (the seed is kept for API stability with the ticket's
            # "use a fixed seed" wording).
            base, rem = divmod(n_samples, len(band))
            for i, m in enumerate(band):
                draws = base + (1 if i < rem else 0)
                row = by_prompt[p].get(m)
                if row is None:
                    continue
                wt = draws / n_samples
                acc_num += wt * row["score"]
                cost_num += wt * row["cost"]
                w_sum += wt
        results[width] = {
            "accuracy": acc_num / w_sum,
            "cost": cost_num / w_sum,
            "reachable": len(reachable),
            "mean_band": statistics.fmean(band_sizes),
            "singleton_share": sum(1 for n in band_sizes if n == 1) / len(band_sizes),
        }

    print()
    print("band_width | expected accuracy | cost/prompt | reachable models")
    print("-----------|-------------------|-------------|-----------------")
    for width in band_widths:
        r = results[width]
        print(f"   {width:5.2f}   |      {r['accuracy']:.6f}      |  "
              f"${r['cost']:.6f}  |  {r['reachable']} of {len(SERVABLE)}")
    print()
    for width in band_widths:
        r = results[width]
        print(f"band {width:.2f}: mean band size {r['mean_band']:.3f}, "
              f"singleton bands {r['singleton_share']*100:.1f}% "
              f"(exploration is a no-op there)")
    return results


def self_check() -> int:
    """Tiny parity checks: knobs load from the artifact's default_routing_knobs
    and the v0.84 roster is exactly the 8 servable models."""
    quality, axes, med = load_artifacts()
    knobs = load_knobs()
    assert len(knobs["alpha"]) == 16, knobs["alpha"]
    assert len(quality) == 16, len(quality)

    registry = json.loads((ART / "model_registry.json").read_text(encoding="utf-8"))
    deployed = [e["model"] for e in registry["deployed_models"]]
    assert deployed == SERVABLE, deployed
    assert all(m in axes for m in SERVABLE)
    assert len(quality[0]) == len(SERVABLE)
    # Knobs as loaded must equal what metadata.yaml itself declares.
    import yaml
    meta = yaml.safe_load((ART / "metadata.yaml").read_text(encoding="utf-8"))
    declared = meta["training"]["default_routing_knobs"]
    assert knobs["alpha"] == [float(a) for a in declared["alpha"]], knobs["alpha"]
    assert knobs["output_cost_ratio"] == float(declared["output_cost_ratio"])
    assert knobs["expected_output_tokens"] == int(declared["expected_output_tokens"])
    assert knobs["per_model_verbosity"] is bool(declared["per_model_verbosity"])
    scores = blend(quality, axes, med, 0, knobs)
    assert set(scores) == set(SERVABLE)
    print(f"self-check OK: 16 clusters, 8 servable models, "
          f"alpha={[round(a, 2) for a in knobs['alpha']]}, "
          f"output_cost_ratio={knobs['output_cost_ratio']}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--widths", type=float, nargs="+", default=[0.0, 0.05, 0.10])
    ap.add_argument("--samples", type=int, default=2000,
                    help="uniform draws per prompt (exact split; kept for the ticket's "
             "fixed-seed wording, result does not depend on the seed)")
    ap.add_argument("--seed", type=int, default=93)
    ap.add_argument("--self-check", action="store_true",
                    help="run the parity assertions and exit")
    args = ap.parse_args()
    if args.self_check:
        return self_check()
    measure(args.widths, args.samples, args.seed)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
