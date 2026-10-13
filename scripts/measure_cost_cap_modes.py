#!/usr/bin/env python3
"""Measure the per-mode reachability of the cost cap (global vs cluster mode).

Ticket: make models that are unreachable only because of the GLOBAL 3x cost cap
reachable, without relaxing cost posture where the cap is doing its job.

Two corpora, both real runtime paths:
  1. The 180 register probes (testdata/register_probes.jsonl + the committed
     embedding cache) driven through a verbatim port of Route: topPNearest ->
     blendScoresV2 -> the mode's cap -> argmax. No ONNX needed (precomputed
     vectors), same trick as internal/router/cluster/mix_collapse_test.go.
  2. The labelled corpus (.context/label-runs/v082-expanded-labels.jsonl +
     corpus-embeds-v084.bin), the corpus scripts/bakeoff_v084.py measures.

Cap semantics, ported from internal/router/cluster/scorer.go:
  global  applyCostCap: models within capMultiple x the cheapest ELIGIBLE
          effective cost; never narrows to empty.
  cluster applyCostCapCluster: the union of the global set and, for each top-P
          cluster k, the models k itself elects (at k's own blend argmax) whose
          effective cost is within capMultiple x the cheapest elected model's.

Measured result is printed with the artifact fingerprints and a reachability
table per model: which prompts each mode routes to each model, and the cost per
prompt in each mode (measured burn from the labels, not the effective-cost
ladder).
"""
from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from collections import Counter
from pathlib import Path

import numpy as np

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO / "internal" / "router" / "cluster" / "artifacts"))

ART = REPO / "internal/router/cluster/artifacts/v0.84"
PROBE_CORPUS = REPO / "internal/router/cluster/testdata/register_probes.jsonl"
PROBE_EMB = REPO / "internal/router/cluster/testdata/register_probes.emb"
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
LABEL_EMB = REPO / ".context/label-runs/corpus-embeds-v084.bin"

TOP_P = 4
CAP = 3.0


# --------------------------------------------------------------- artifacts

def load_knobs() -> dict:
    import yaml
    meta = yaml.safe_load((ART / "metadata.yaml").read_text(encoding="utf-8"))
    knobs = meta["training"]["default_routing_knobs"]
    return {
        "alpha": [float(a) for a in knobs["alpha"]],
        "speed_weight": float(knobs.get("speed_weight", 0.0)),
        "output_cost_ratio": float(knobs.get("output_cost_ratio", 0.0)),
        "expected_output_tokens": int(knobs.get("expected_output_tokens", 2000)),
        "per_model_verbosity": bool(knobs.get("per_model_verbosity", True)),
    }


def load_bundle():
    qm = json.loads((ART / "quality_means.json").read_text(encoding="utf-8"))
    quality = {int(k): v for k, v in qm["quality_means"].items()}
    axes = json.loads((ART / "model_axes.json").read_text(encoding="utf-8"))["axes"]
    verbs = sorted(v["verbosity_tokens"] for v in axes.values()
                   if v.get("verbosity_tokens") is not None)
    n = len(verbs)
    med = (verbs[n // 2] if n % 2 else (verbs[n // 2 - 1] + verbs[n // 2]) / 2.0) if n else 0.0
    reg = json.loads((ART / "model_registry.json").read_text(encoding="utf-8"))
    deployed = [e["model"] for e in reg["deployed_models"]]
    return quality, axes, med, deployed


def load_centroids():
    raw = (ART / "centroids.bin").read_bytes()
    assert raw[:4] == b"CRT1", "bad centroids magic"
    k = int.from_bytes(raw[8:12], "little")
    dim = int.from_bytes(raw[12:16], "little")
    cents = np.frombuffer(raw[16:16 + k * dim * 4], dtype="<f4").reshape(k, dim).copy()
    cents /= np.clip(np.linalg.norm(cents, axis=1, keepdims=True), 1e-9, None)
    return cents


def fingerprint() -> str:
    import hashlib
    out = {}
    for name in ("quality_means.json", "model_axes.json", "metadata.yaml", "model_registry.json"):
        out[name] = hashlib.sha256((ART / name).read_bytes()).hexdigest()[:16]
    return json.dumps(out, sort_keys=True)


# ------------------------------------------------------- runtime port

def effective_costs(axes, med, knobs) -> dict:
    """scorer.go / blendScoresV2 cost term."""
    costs = {}
    for m, ax in axes.items():
        v = 1.0
        if knobs["per_model_verbosity"] and ax.get("verbosity_tokens") is not None and med > 0:
            v = ax["verbosity_tokens"] / med
        costs[m] = (ax.get("input_per_1k_usd") or 0.0) \
            + knobs["output_cost_ratio"] * (ax.get("output_per_1k_usd") or 0.0) * v
    return costs


def blend(quality, costs, deployed, knobs, top_clusters, eligible):
    """blendScoresV2 for one prompt: cost min-max over the DEPLOYED pool,
    quality min-max per cluster, summed over the top-P clusters."""
    cmin = min(costs[m] for m in deployed)
    cmax = max(costs[m] for m in deployed)
    crange = cmax - cmin
    scores = {m: 0.0 for m in eligible}
    for k in top_clusters:
        row = quality[k]
        qmin = min(row[m] for m in deployed)
        qmax = max(row[m] for m in deployed)
        qrange = qmax - qmin
        wq = knobs["alpha"][k]
        for m in eligible:
            qn = (row[m] - qmin) / qrange if qrange > 0 else 0.0
            cn = (costs[m] - cmin) / crange if crange > 0 else 0.0
            scores[m] += wq * qn + (1.0 - wq) * (1.0 - cn)
    return scores


def argmax(scores, order):
    """argmax over the candidate order, tie-break by first (deployed order)."""
    best, best_s = None, -math.inf
    for m in order:
        s = scores.get(m)
        if s is None:
            continue
        if best is None or s > best_s:
            best, best_s = m, s
    return best


def cap_global(order, costs, cap):
    """scorer.go applyCostCap: budget = cap x cheapest ELIGIBLE cost;
    never narrows to empty."""
    if cap is None or cap <= 0 or not order:
        return list(order)
    budget = cap * min(costs[m] for m in order)
    capped = [m for m in order if costs[m] <= budget]
    return capped if capped else list(order)


def cap_cluster(top_clusters, quality, costs, deployed, knobs, order, cap):
    """scorer.go applyCostCapCluster: global set UNION, per top-P cluster k,
    the models k itself elects (k's own argmax) within cap x that elected
    set's cheapest cost. Never narrows to empty."""
    if cap is None or cap <= 0 or not order or not top_clusters:
        return list(order)
    keep = set(cap_global(order, costs, cap))
    for k in top_clusters:
        row = quality[k]
        qmin = min(row[m] for m in deployed)
        qmax = max(row[m] for m in deployed)
        qrange = qmax - qmin
        wq = knobs["alpha"][k]
        cmin = min(costs[m] for m in deployed)
        cmax = max(costs[m] for m in deployed)
        crange = cmax - cmin
        single = {}
        for m in order:
            qn = (row[m] - qmin) / qrange if qrange > 0 else 0.0
            cn = (costs[m] - cmin) / crange if crange > 0 else 0.0
            single[m] = wq * qn + (1.0 - wq) * (1.0 - cn)
        top = max(single.values()) if single else None
        if top is None:
            continue
        elected = [m for m in order if single[m] == top]
        budget = cap * min(costs[m] for m in elected)
        keep.update(m for m in elected if costs[m] <= budget)
    return [m for m in order if m in keep] or list(order)


def route(vec, cents, quality, costs, deployed, knobs, mode):
    """Route: topPNearest -> blendScoresV2 -> mode's cap -> argmax."""
    sims = cents @ vec
    top = sorted(sorted(range(len(cents)), key=lambda k: (-sims[k], k))[:TOP_P])
    scores = blend(quality, costs, deployed, knobs, top, deployed)
    capped = (cap_global if mode == "global" else cap_cluster)(
        **({"top_clusters": top, "quality": quality, "costs": costs,
           "deployed": deployed, "knobs": knobs, "order": deployed, "cap": CAP}
          if mode == "cluster" else
          {"order": deployed, "costs": costs, "cap": CAP}))
    return argmax(scores, capped)


def read_pem(path):
    raw = path.read_bytes()
    assert raw[:4] == b"PEM1"
    hlen = int.from_bytes(raw[4:8], "little")
    hdr = json.loads(raw[8:8 + hlen])
    n, dim = hdr["n"], hdr["embed_dim"]
    rows = np.frombuffer(raw[8 + hlen:8 + hlen + n * dim * 4], dtype="<f4").reshape(n, dim)
    return hdr, rows.copy()


# ------------------------------------------------------------------ corpora

def measure_probes(quality, axes, med, deployed, knobs, cents):
    import math  # noqa: F401 - route() uses it
    costs = effective_costs(axes, med, knobs)
    probes = [json.loads(l) for l in PROBE_CORPUS.read_text(encoding="utf-8").splitlines() if l.strip()]
    hdr, rows = read_pem(PROBE_EMB)
    assert len(rows) == len(probes), (len(rows), len(probes))
    out = {}
    for mode in ("global", "cluster"):
        picks = Counter()
        per_register = {}
        for p, vec in zip(probes, rows):
            m = route(vec, cents, quality, costs, deployed, knobs, mode)
            picks[m] += 1
            per_register.setdefault(p["register"], Counter())[m] += 1
        out[mode] = {"mix": picks, "per_register": per_register, "n": len(probes)}
    return out


def measure_labels(quality, axes, med, deployed, knobs, cents):
    import math  # noqa: F401 - route() uses it
    costs = effective_costs(axes, med, knobs)
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    prompts = sorted(by_prompt)
    texts = {}
    for r in rows:
        texts.setdefault(r["global_index"], r.get("question") or "")
    hdr, emb = read_pem(LABEL_EMB)
    assert len(emb) == len(prompts), (len(emb), len(prompts))
    # The cache's prompt order is sorted(global_index) per bakeoff_v084.py's
    # header; the runtime's tail-truncate is 1024 bytes of the same text.
    vecs = {p: v for p, v in zip(prompts, emb)}

    out = {}
    for mode in ("global", "cluster"):
        mix = Counter()
        cost_num, acc_num, n = 0.0, 0.0, 0
        for p in prompts:
            m = route(vecs[p], cents, quality, costs, deployed, knobs, mode)
            row = by_prompt[p].get(m)
            if row is None or row.get("score") is None:
                continue
            mix[m] += 1
            acc_num += float(row["score"])
            cost_num += float(row.get("cost") or 0.0)
            n += 1
        out[mode] = {
            "n": n,
            "mix": mix,
            "accuracy": acc_num / n if n else 0.0,
            "cost_per_prompt": cost_num / n if n else 0.0,
        }
    return out


def reach(all_models, mixes):
    return {m: {mode: mixes[mode]["mix"].get(m, 0) for mode in ("global", "cluster")}
            for m in all_models}


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--probes-only", action="store_true")
    ap.add_argument("--labels-only", action="store_true")
    args = ap.parse_args()

    knobs = load_knobs()
    quality, axes, med, deployed = load_bundle()
    cents = load_centroids()
    print(f"artifact fingerprints: {fingerprint()}")
    print(f"alpha={knobs['alpha']} output_cost_ratio={knobs['output_cost_ratio']} "
          f"per_model_verbosity={knobs['per_model_verbosity']}")
    costs = effective_costs(axes, med, knobs)
    ladder = sorted(costs.items(), key=lambda kv: kv[1])
    print(f"median verbosity {med:.2f}; effective-cost ladder: "
          + ", ".join(f"{m.split('/')[-1]} {c:.7f} ({c/ladder[0][1]:.2f}x)" for m, c in ladder))
    print(f"cap multiple {CAP}, top_p {TOP_P}, models {len(deployed)}")

    if not args.labels_only:
        res = measure_probes(quality, axes, med, deployed, knobs, cents)
        print()
        print("=== 180 register probes ===")
        for mode in ("global", "cluster"):
            mix = res[mode]["mix"]
            reach = {m for m in deployed if mix.get(m, 0) > 0}
            print(f"{mode:>8}: {len(reach)} of {len(deployed)} reachable | " +
                  ", ".join(f"{m.split('/')[-1]} {mix.get(m, 0)}" for m in deployed if mix.get(m)))
        for m in deployed:
            g, c = res["global"]["mix"].get(m, 0), res["cluster"]["mix"].get(m, 0)
            flag = "  <-- cluster-only" if c > 0 and g == 0 else ""
            print(f"  {m:38s} global {g:4d}  cluster {c:4d}{flag}")
        for reg in sorted(res["global"]["per_register"]):
            gm = res["global"]["per_register"][reg]
            cm = res["cluster"]["per_register"][reg]
            print(f"  {reg:16s} global " +
                  ", ".join(f"{m.split('/')[-1]} {n}" for m, n in gm.most_common()) +
                  " | cluster " + ", ".join(f"{m.split('/')[-1]} {n}" for m, n in cm.most_common()))

    if not args.probes_only:
        res = measure_labels(quality, axes, med, deployed, knobs, cents)
        print()
        print("=== labelled corpus ===")
        for mode in ("global", "cluster"):
            r = res[mode]
            reach = sum(1 for m in deployed if r["mix"].get(m, 0) > 0)
            print(f"{mode:>8}: n={r['n']} reachable {reach} of {len(deployed)} | "
                  f"accuracy {100*r['accuracy']:.4f}% | cost/prompt ${r['cost_per_prompt']:.6f}")
            print(f"{'':>8}  mix: " + ", ".join(f"{m.split('/')[-1]} {n} ({100*n/r['n']:.1f}%)"
                                                for m, n in r["mix"].most_common()))
        for m in deployed:
            g, c = res["global"]["mix"].get(m, 0), res["cluster"]["mix"].get(m, 0)
            flag = "  <-- cluster-only" if c > 0 and g == 0 else ""
            print(f"  {m:38s} global {g:5d}  cluster {c:5d}{flag}")
    return 0


if __name__ == "__main__":
    import math
    raise SystemExit(main())
