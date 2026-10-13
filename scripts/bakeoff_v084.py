#!/usr/bin/env python3
"""Bake-off the rebuilt router (v0.84) against the pinned baseline (v0.82).

Offline replay over the measured label corpus (the same method the v0.82 /
v0.83 builders and the tickets cite):

  1. The routing input is the live cluster embedding: every corpus prompt is
     embedded with the committed jina-v2-base-code-int8 embedder (same masked
     mean-pool + L2-norm contract as the Go Embedder and the v0.82/v0.83
     builders) and assigned its top-1 cosine cluster from centroids.bin, the
     same id Route receives. The committed
     .context/label-runs/prompt-clusters-v082-full.json is audited, not
     trusted: it predates the final centroid retrain (150 of its 1,682 entries
     disagree with the live route) and using it would feed both arms a stale
     cluster id. --verify-clusters prints the disagreement.
  2. For every prompt, the route decision is recomputed exactly as
     internal/router/cluster/scorer.go Route does: sum blendScoresV2 over the
     prompt's top_p=4 nearest clusters (topPNearest, ties by ascending cluster
     id), then restrict the argmax candidate set with applyCostCap
     (DefaultCostCapMultiple=3.0 over the roster's effective costs), then argmax
     with the deployed-roster order tie-break.
  3. The chosen model's measured label (score, cost) is the outcome.

Both arms run through the identical code path; only the quality cells differ
(v0.82's baseline knobs vs the v0.84 bundle's default_routing_knobs). Every
routed prompt is either scored or bucketed as unroutable with an explicit
reason; no prompt is silently dropped and the eval n is printed, never inferred.

Deterministic: no RNG on the primary path; the only sampling (the paired
bootstrap) runs a fixed seed. Rerunning reproduces the same numbers.

Usage:
    python scripts/bakeoff_v084.py                # full bake-off
    python scripts/bakeoff_v084.py --self-check    # parity assertions only
    python scripts/bakeoff_v084.py --verify-clusters   # re-embed + top-1 check
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import random
from collections import Counter
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v082-full.json"
CANDIDATE = REPO / "internal/router/cluster/artifacts/v0.84"
BASELINE = REPO / "internal/router/cluster/artifacts/v0.82"
EMBEDDER_ASSETS = REPO / "assets-extracted/jina-v2-base-code-int8"
# PEM1 embedding cache (same auditable format as testdata/register_probes.emb):
# magic "PEM1" + uint32 header length + JSON header + [n][dim]float32 rows, in
# corpus (sorted global_index) order. Keyed by the corpus sha256, so a corpus
# edit invalidates it and forces a re-embed rather than silently reusing stale
# vectors.
EMBED_CACHE = REPO / ".context/label-runs/corpus-embeds-v084.bin"
PEM_MAGIC = b"PEM1"

# internal/router/cluster/scorer.go DefaultConfig(): TopP 4, MaxPromptChars
# 1024; DefaultCostCapMultiple is the knee Route applies unless overridden.
TOP_P = 4
MAX_PROMPT_CHARS = 1024
MAX_TOKENS = 256
COST_CAP_MULTIPLE = 3.0

# bench/weave_bench/stats.py (stdlib only, same constants).
Z_95 = 1.959963984540054
BOOTSTRAP_RESAMPLES = 10_000
BOOTSTRAP_SEED = 20260904

# Bias-control buckets: every routed prompt is scored or carries one of these.
REASON_NO_LABEL = "no_label_for_chosen_model"
REASON_NO_CLUSTER = "no_cluster_assignment"
REASON_NO_SCORE = "chosen_model_label_score_missing"


class Interval:
    __slots__ = ("point", "low", "high")

    def __init__(self, point: float, low: float, high: float):
        self.point, self.low, self.high = point, low, high

    def as_dict(self) -> dict:
        return {"point": self.point, "low": self.low, "high": self.high}


def wilson(successes: int, trials: int, z: float = Z_95) -> Interval:
    """Score interval for a binomial proportion; stays inside [0, 1]."""
    if trials == 0:
        return Interval(0.0, 0.0, 1.0)
    p = successes / trials
    denom = 1 + z * z / trials
    center = (p + z * z / (2 * trials)) / denom
    half = z * math.sqrt(p * (1 - p) / trials + z * z / (4 * trials * trials)) / denom
    return Interval(p, max(0.0, center - half), min(1.0, center + half))


def bootstrap_mean(values, resamples: int = BOOTSTRAP_RESAMPLES, seed: int = BOOTSTRAP_SEED) -> Interval:
    """Percentile bootstrap of the mean, resampling per-prompt paired deltas."""
    if not values:
        return Interval(0.0, 0.0, 0.0)
    rng = random.Random(seed)
    n = len(values)
    means = sorted(sum(rng.choices(values, k=n)) / n for _ in range(resamples))
    return Interval(sum(values) / n, means[int(0.025 * resamples)], means[min(resamples - 1, int(0.975 * resamples))])


# ---------------------------------------------------------------- artifacts

def load_knobs(bundle: Path) -> dict:
    """default_routing_knobs from the bundle's metadata.yaml — the same source
    the scorer boots with (scorer.go defaultActiveKnobs)."""
    import yaml
    out = {
        "alpha": [0.96] * 16,
        "speed_weight": 0.0,
        "output_cost_ratio": 0.25,
        "expected_output_tokens": 2000,
        "per_model_verbosity": True,
    }
    knobs = ((yaml.safe_load((bundle / "metadata.yaml").read_text(encoding="utf-8"))
              .get("training") or {}).get("default_routing_knobs") or {})
    if knobs.get("alpha"):
        out["alpha"] = [float(a) for a in knobs["alpha"]]
    out["speed_weight"] = float(knobs.get("speed_weight", out["speed_weight"]))
    out["output_cost_ratio"] = float(knobs.get("output_cost_ratio", out["output_cost_ratio"]))
    out["expected_output_tokens"] = int(knobs.get("expected_output_tokens", out["expected_output_tokens"]))
    out["per_model_verbosity"] = bool(knobs.get("per_model_verbosity", out["per_model_verbosity"]))
    return out


def load_bundle(bundle: Path):
    """(quality_means, axes, median_verbosity, deployed_order).

    The loader prefers model_features.json and validates that it mirrors
    quality_means.json for the deployed roster, so either file routes the same.
    We read quality_means.json + model_axes.json and assert the mirror in
    --self-check.
    """
    qm = json.loads((bundle / "quality_means.json").read_text(encoding="utf-8"))
    quality = {int(k): {m: float(v) for m, v in row.items()}
               for k, row in qm["quality_means"].items()}
    axes = json.loads((bundle / "model_axes.json").read_text(encoding="utf-8"))["axes"]
    registry = json.loads((bundle / "model_registry.json").read_text(encoding="utf-8"))
    deployed = [e["model"] for e in registry["deployed_models"]]
    # artifacts.go: median of verbosity_tokens over deployed models with data,
    # taking the upper of the two middle values (even count -> vals[len//2]).
    vals = sorted(axes[m]["verbosity_tokens"] for m in deployed
                  if axes.get(m, {}).get("verbosity_tokens") is not None)
    med = vals[len(vals) // 2] if vals else 1.0
    return quality, axes, med, deployed


def sha256_of(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def fingerprints(bundle: Path) -> dict:
    return {name: sha256_of(bundle / name) for name in
            ("quality_means.json", "model_axes.json", "metadata.yaml", "model_registry.json")}


# ------------------------------------------------------- runtime parity

def effective_costs(axes, med, knobs) -> dict:
    """scorer.go / blendScoresV2 cost term: input_per_1k +
    output_cost_ratio * output_per_1k * verbosity_factor (per_model_verbosity)."""
    costs = {}
    for m, ax in axes.items():
        v = 1.0
        if knobs["per_model_verbosity"] and ax.get("verbosity_tokens") is not None and med > 0:
            v = ax["verbosity_tokens"] / med
        costs[m] = (ax.get("input_per_1k_usd") or 0.0) \
            + knobs["output_cost_ratio"] * (ax.get("output_per_1k_usd") or 0.0) * v
    return costs


def blend_scores_v2(quality, axes, med, knobs, deployed, eligible, top_clusters):
    """blendScoresV2 for one prompt: quality min-max per cluster, cost min-max
    over the deployed pool, summed over the top-P clusters. speed_weight is 0
    in both bundles' default knobs, so the no-speed branch (weights redistributed
    into wQ/wC, which is the identity at wQ+wC==1) is what runs."""
    costs = effective_costs(axes, med, knobs)
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


def apply_cost_cap(order, axes, med, knobs, cap_multiple):
    """scorer.go applyCostCap: models within cap_multiple x the cheapest
    eligible effective cost; never narrows to empty."""
    if cap_multiple is None or cap_multiple <= 0:
        return order
    costs = effective_costs(axes, med, knobs)
    budget = cap_multiple * min(costs[m] for m in order)
    capped = [m for m in order if costs[m] <= budget]
    return capped if capped else order


def route_prompt(quality, axes, med, deployed, knobs, top_clusters, cap_multiple):
    """scorer.go Route: blend -> cost cap -> argmax (deployed-order tie-break)."""
    scores = blend_scores_v2(quality, axes, med, knobs, deployed, deployed, top_clusters)
    capped = apply_cost_cap(deployed, axes, med, knobs, cap_multiple)
    best, best_score = None, -math.inf
    for m in capped:
        s = scores.get(m)
        if s is None:
            continue
        if best is None or s > best_score:
            best, best_score = m, s
    return best, best_score


# ------------------------------------------------------------- corpus

def load_rows_and_assign(labels_path: Path, assign_path: Path):
    rows = []
    with labels_path.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    return rows, by_prompt, json.loads(assign_path.read_text(encoding="utf-8"))


def load_centroids(bundle: Path):
    """centroids.bin (CRT1, L2-normalized rows) — identical bytes in both arms."""
    import numpy as np
    raw = (bundle / "centroids.bin").read_bytes()
    assert raw[:4] == b"CRT1", "bad centroids magic"
    k = int.from_bytes(raw[8:12], "little")
    dim = int.from_bytes(raw[12:16], "little")
    cents = np.frombuffer(raw[16:16 + k * dim * 4], dtype="<f4").reshape(k, dim).copy()
    cents /= np.clip(np.linalg.norm(cents, axis=1, keepdims=True), 1e-9, None)
    return cents


def top_p_clusters(vec, cents, p: int) -> list:
    """topPNearest: p highest-cosine clusters (ties by lower cluster index),
    returned ascending — the Go code sorts the chosen indices before blending."""
    sims = cents @ vec
    order = sorted(range(len(cents)), key=lambda k: (-sims[k], k))[:p]
    return sorted(order)


def embed_prompts(texts):
    """The embedder contract the Go Embedder and the v0.82/v0.83 builders use:
    tail-truncate to 1024 bytes, tokenize (last 256 ids), masked mean pool,
    L2 normalize. Committed jina-v2-base-code-int8 ONNX assets.

    Batches are grouped by token length so padding inside a batch stays small
    (the model's cost is linear in padded length). Deterministic: order within a
    bucket is the input order and pooling is per-row.
    """
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    tok = Tokenizer.from_file(str(EMBEDDER_ASSETS / "tokenizer.json"))
    sess = ort.InferenceSession(str(EMBEDDER_ASSETS / "model.onnx"),
                               providers=["CPUExecutionProvider"])

    order = []  # (bucket, row index, token ids) in input order
    for idx, t in enumerate(texts):
        b = t.encode("utf-8", errors="replace")
        s = b[-MAX_PROMPT_CHARS:].decode("utf-8", errors="ignore") if len(b) > MAX_PROMPT_CHARS else t
        enc = tok.encode(s)
        ids_j = enc.ids[-MAX_TOKENS:]
        if ids_j:
            bucket = (16 if len(ids_j) <= 16 else 32 if len(ids_j) <= 32 else
                      64 if len(ids_j) <= 64 else 128 if len(ids_j) <= 128 else MAX_TOKENS)
            order.append((bucket, idx, ids_j))

    rows_by_bucket: dict[int, list] = {}
    for bucket, idx, ids_j in order:
        rows_by_bucket.setdefault(bucket, []).append((idx, ids_j))
    out = np.zeros((len(texts), 768), dtype=np.float32)
    for bucket, rows in sorted(rows_by_bucket.items()):
        for i in range(0, len(rows), 32):
            chunk = rows[i:i + 32]
            ids = np.zeros((len(chunk), bucket), dtype=np.int64)
            mask = np.zeros((len(chunk), bucket), dtype=np.int64)
            for j, (idx, ids_j) in enumerate(chunk):
                ids[j, :len(ids_j)] = ids_j
                mask[j, :len(ids_j)] = 1
            vec = sess.run(None, {"input_ids": ids, "attention_mask": mask})[0]
            m = mask.astype(np.float32)
            pooled = (vec * m[:, :, None]).sum(1) / np.clip(m.sum(1, keepdims=True), 1e-9, None)
            pooled = pooled / np.clip(np.linalg.norm(pooled, axis=1, keepdims=True), 1e-9, None)
            for j, (idx, _) in enumerate(chunk):
                out[idx] = pooled[j]
    return out


def load_or_embed(prompts, texts, corpus_sha):
    """Embed the corpus, caching the vectors as PEM1 beside the labels (same
    auditable format as testdata/register_probes.emb). The cache is keyed by
    corpus sha256 and the embedder id, so a corpus or embedder change re-embeds
    instead of reusing stale vectors. Vector rows follow the sorted-prompt order
    the caller passes in, and that order is recorded in the header."""
    import struct
    import numpy as np
    header = {
        "embedder_id": "jina-v2-base-code-int8",
        "embed_dim": 768,
        "n": len(prompts),
        "corpus_sha256": corpus_sha,
        "prompt_order": "sorted(global_index)",
        "centroids_sha256": sha256_of(CANDIDATE / "centroids.bin"),
        "comment": "Bake-off corpus embeddings; regenerate by deleting the file "
                   "(scripts/bakeoff_v084.py re-embeds automatically).",
    }
    hb = json.dumps(header, sort_keys=True).encode()
    if EMBED_CACHE.exists():
        raw = EMBED_CACHE.read_bytes()
        if (len(raw) >= 8 and raw[:4] == PEM_MAGIC
                and json.loads(raw[8:8 + int.from_bytes(raw[4:8], "little")]) == header):
            vecs = np.frombuffer(raw[8 + len(hb):], dtype="<f4").reshape(len(prompts), 768)
            print(f"embeddings: cache hit ({len(prompts)} rows, corpus sha256 {corpus_sha[:12]})")
            return vecs.copy()
        print("embeddings: cache stale (corpus/embedder changed); re-embedding")
    print(f"embedding {len(prompts)} prompts (one-time; cached to "
          f"{EMBED_CACHE.relative_to(REPO)})...")
    vecs = embed_prompts([texts[p] for p in prompts]).astype("<f4")
    EMBED_CACHE.parent.mkdir(parents=True, exist_ok=True)
    with EMBED_CACHE.open("wb") as fh:
        fh.write(PEM_MAGIC)
        fh.write(struct.pack("<I", len(hb)))
        fh.write(hb)
        fh.write(vecs.tobytes())
    return vecs


def route_all(by_prompt, assign, cents, bundle, knobs, cap_multiple, vecs, label=""):
    """Route every prompt; return (decisions, reason counts, scored/unroutable).

    decisions[p] = (model, score, cost) for scored prompts.
    """
    quality, axes, med, deployed = bundle
    decisions: dict[str, tuple[str, float, float]] = {}
    reasons: Counter = Counter()
    for p, vec in vecs.items():
        if p not in assign:
            reasons[REASON_NO_CLUSTER] += 1
            continue
        model, _ = route_prompt(quality, axes, med, deployed, knobs,
                               top_p_clusters(vec, cents, TOP_P), cap_multiple)
        if model is None:
            raise RuntimeError(f"{label}: argmax produced no model for {p}")
        got = by_prompt[p].get(model)
        if got is None:
            reasons[REASON_NO_LABEL] += 1
            continue
        if got.get("score") is None:
            reasons[REASON_NO_SCORE] += 1
            continue
        decisions[p] = (model, float(got["score"]), float(got.get("cost") or 0.0))
    return decisions, reasons


def summarize(decisions, name):
    n = len(decisions)
    acc = sum(v[1] for v in decisions.values()) / n if n else 0.0
    mix = Counter(m for m, _, _ in decisions.values())
    return {
        "arm": name,
        "n": n,
        "accuracy": acc,
        "accuracy_ci": wilson(round(acc * n), n),
        "cost_per_prompt": sum(v[2] for v in decisions.values()) / n if n else 0.0,
        "mix": dict(sorted(mix.items(), key=lambda kv: (-kv[1], kv[0]))),
    }


# ------------------------------------------------------------------ modes

def self_check() -> int:
    cand, base = load_bundle(CANDIDATE), load_bundle(BASELINE)
    for tag, bundle in (("v0.82", base), ("v0.84", cand)):
        quality, axes, med, deployed = bundle
        assert len(quality) == 16, (tag, len(quality))
        assert len(deployed) == 8, (tag, deployed)
        # model_features.json mirrors quality_means for the deployed roster.
        root = CANDIDATE if tag == "v0.84" else BASELINE
        feats = json.loads((root / "model_features.json").read_text(encoding="utf-8"))["models"]
        for m in deployed:
            assert feats[m]["operational"] == axes[m], (tag, m)
            for k in range(16):
                assert abs(feats[m]["psi_probe"][k] - quality[k][m]) < 1e-12, (tag, k, m)
    k84, k82 = load_knobs(CANDIDATE), load_knobs(BASELINE)
    assert k84["alpha"] == [0.96, 0.96, 0.8, 0.8, 0.8, 0.96, 0.8, 0.96,
                            0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96, 0.96], k84["alpha"]
    assert k84["speed_weight"] == 0.0 and k84["output_cost_ratio"] == 0.25
    assert len(k82["alpha"]) == 16 and k82["speed_weight"] == 0.0
    assert cand[3] == base[3], "roster differs between arms"
    c82, c84 = load_centroids(BASELINE), load_centroids(CANDIDATE)
    assert (c82 == c84).all(), "centroids differ between arms"
    # median verbosity must reproduce artifacts.go's upper-middle rule.
    assert cand[2] == base[2] == 516.4268292682926, (cand[2], base[2])
    print("self-check OK: 16 clusters, identical roster/centroids/axes, "
          "model_features.json mirrors quality_means.json, "
          "v0.84 alpha=[0.96,0.96,0.8,0.8,0.8,0.96,0.8,0.96,...], ocr=0.25")
    return 0


def verify_clusters() -> int:
    """Re-embed the corpus and confirm the assignment cache is the live top-1
    cosine route for every prompt (so the cache is the runtime's cluster id).

    The cache is regenerated, never trusted: the live top-P neighbourhood from
    this embedding is the routing input. The cache is only audited so the
    bake-off can report how far stale it is (it predates the final centroid
    retrain; see bakeoff.md's "cluster assignment" note).
    """
    import numpy as np
    rows, by_prompt, assign_all = load_rows_and_assign(LABELS, ASSIGN)
    prompts = sorted(by_prompt)
    texts = {}
    for r in rows:
        texts.setdefault(r["global_index"], r.get("question") or "")
    cents = load_centroids(CANDIDATE)
    vecs = embed_prompts([texts[p] for p in prompts])
    top1 = np.argmax(cents @ vecs.T, axis=0)
    mism = [p for i, p in enumerate(prompts)
            if p in assign_all and int(top1[i]) != int(assign_all[p])]
    covered = sum(1 for p in prompts if p in assign_all)
    print(f"verify-clusters: {len(prompts)} prompts embedded, {len(mism)}/{covered} "
          f"cached assignments disagree with the live top-1 cosine route")
    for p in mism[:10]:
        i = prompts.index(p)
        print(f"  {p}: cache={assign_all[p]} live={int(top1[i])}")
    return 1 if mism else 0


def run_bakeoff() -> dict:
    rows, by_prompt, assign_all = load_rows_and_assign(LABELS, ASSIGN)
    prompts = sorted(by_prompt)
    all8 = [p for p in prompts if len(by_prompt[p]) == 8]
    texts = {}
    for r in rows:
        texts.setdefault(r["global_index"], r.get("question") or "")

    cents = load_centroids(CANDIDATE)
    corpus_sha = sha256_of(LABELS)
    vecs = load_or_embed(prompts, texts, corpus_sha)
    vecs_all = {p: v for p, v in zip(prompts, vecs)}

    # Live cluster assignment: top-1 cosine per prompt (the runtime's cluster id),
    # recomputed here rather than trusted from prompt-clusters-v082-full.json —
    # the cache predates the final centroid retrain (150/1682 entries disagree),
    # and every prior builder's cache would make the two arms disagree about
    # which clusters a prompt belongs to. Both arms use the same live ids.
    import numpy as np
    top1 = np.argmax(cents @ vecs.T, axis=0)
    live_assign = {p: int(top1[i]) for i, p in enumerate(prompts)}
    stale = sum(1 for p in prompts if p in assign_all and int(assign_all[p]) != live_assign[p])

    k82, k84 = load_knobs(BASELINE), load_knobs(CANDIDATE)
    base_bundle, cand_bundle = load_bundle(BASELINE), load_bundle(CANDIDATE)

    out = {
        "corpus": {
            "file": str(LABELS.relative_to(REPO).as_posix()),
            "sha256": corpus_sha,
            "assign_sha256": sha256_of(ASSIGN),
            "rows": len(rows),
            "prompts": len(prompts),
            "assigned": len(assign_all),
            "all8": len(all8),
            "stale_cache_entries": stale,
        },
        "arms": {},
    }

    def arm(tag, bundle, knobs, cap):
        dec, reasons = route_all(by_prompt, live_assign, cents, bundle, knobs, cap, vecs_all, tag)
        s = summarize(dec, tag)
        s["unroutable"] = dict(reasons)
        dec8 = {p: v for p, v in dec.items() if p in set(all8)}
        s["n_all8"] = len(dec8)
        s["accuracy_all8"] = (sum(v[1] for v in dec8.values()) / len(dec8)) if dec8 else 0.0
        s["accuracy_all8_ci"] = wilson(round(s["accuracy_all8"] * len(dec8)), len(dec8)) if dec8 else Interval(0, 0, 1)
        s["mix_all8"] = dict(sorted(Counter(m for m, _, _ in dec8.values()).items(),
                                    key=lambda kv: (-kv[1], kv[0])))
        return s, dec

    for cap_tag, cap in (("cap3.0", COST_CAP_MULTIPLE), ("uncapped", None)):
        for tag, bundle, knobs in (("v0.82", base_bundle, k82), ("v0.84", cand_bundle, k84)):
            s, _ = arm(tag, bundle, knobs, cap)
            out["arms"][f"{tag}/{cap_tag}"] = s

    # Paired delta on the identical scored prompt set (matched prompts only).
    def paired(cap):
        dec_a, rea_a = route_all(by_prompt, live_assign, cents, base_bundle, k82, cap, vecs_all, "v0.82")
        dec_b, rea_b = route_all(by_prompt, live_assign, cents, cand_bundle, k84, cap, vecs_all, "v0.84")
        common = sorted(set(dec_a) & set(dec_b))
        diffs = [dec_b[p][1] - dec_a[p][1] for p in common]
        wins = sum(1 for d in diffs if d > 0)
        losses = sum(1 for d in diffs if d < 0)
        set8 = set(all8)
        common8 = [p for p in common if p in set8]
        diffs8 = [dec_b[p][1] - dec_a[p][1] for p in common8]
        return {
            "n": len(common),
            "acc_a": sum(dec_a[p][1] for p in common) / len(common),
            "acc_b": sum(dec_b[p][1] for p in common) / len(common),
            # Wilson intervals on the SAME identical prompt set the delta uses
            # (successes = prompts the arm scores strictly above 0.5), so the
            # two arms' accuracies are directly comparable.
            "acc_a_ci": wilson(sum(1 for p in common if dec_a[p][1] > 0.5), len(common)),
            "acc_b_ci": wilson(sum(1 for p in common if dec_b[p][1] > 0.5), len(common)),
            "delta_mean": sum(diffs) / len(diffs),
            "delta_ci": bootstrap_mean(diffs),
            "delta_wilson_on_pairs": wilson(sum(1 for d in diffs if d > 0),
                                            sum(1 for d in diffs if d != 0)),
            "cost_a": sum(dec_a[p][2] for p in common) / len(common),
            "cost_b": sum(dec_b[p][2] for p in common) / len(common),
            "wins": wins, "losses": losses, "ties": len(diffs) - wins - losses,
            "mix_a": dict(sorted(Counter(dec_a[p][0] for p in common).items(),
                                 key=lambda kv: (-kv[1], kv[0]))),
            "mix_b": dict(sorted(Counter(dec_b[p][0] for p in common).items(),
                                 key=lambda kv: (-kv[1], kv[0]))),
            "reasons_a": dict(rea_a), "reasons_b": dict(rea_b),
            "only_a_scored": len(set(dec_a) - set(dec_b)),
            "only_b_scored": len(set(dec_b) - set(dec_a)),
            "n8": len(common8),
            "acc_a8": sum(dec_a[p][1] for p in common8) / len(common8),
            "acc_b8": sum(dec_b[p][1] for p in common8) / len(common8),
            "delta_mean8": sum(diffs8) / len(diffs8),
            "delta_ci8": bootstrap_mean(diffs8),
        }

    out["paired"] = {tag: paired(cap) for tag, cap in
                     (("cap3.0", COST_CAP_MULTIPLE), ("uncapped", None))}
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[1])
    ap.add_argument("--self-check", action="store_true")
    ap.add_argument("--verify-clusters", action="store_true")
    ap.add_argument("--json", action="store_true", help="print the raw result dict")
    args = ap.parse_args()
    if args.self_check:
        return self_check()
    if args.verify_clusters:
        return verify_clusters()

    res = run_bakeoff()
    if args.json:
        print(json.dumps(res, indent=2, sort_keys=True,
                        default=lambda o: o.as_dict() if isinstance(o, Interval) else str(o)))
        return 0

    print(f"corpus: {res['corpus']['rows']} rows, {res['corpus']['prompts']} prompts "
          f"({res['corpus']['assigned']} cluster-assigned, {res['corpus']['all8']} with all 8 models scored)")
    for tag, s in res["arms"].items():
        print(f"{tag:>16}: n={s['n']:5d} acc={100*s['accuracy']:.4f}% "
              f"CI=[{100*s['accuracy_ci'].low:.4f},{100*s['accuracy_ci'].high:.4f}]% "
              f"cost=${s['cost_per_prompt']:.6f} unroutable={s['unroutable']}")
        print(f"{'':>16}  all8: n={s['n_all8']} acc={100*s['accuracy_all8']:.4f}% "
              f"CI=[{100*s['accuracy_all8_ci'].low:.4f},{100*s['accuracy_all8_ci'].high:.4f}]%")
        print(f"{'':>16}  mix: "
              + ", ".join(f"{m.split('/')[-1]} {c} ({100*c/s['n']:.1f}%)" for m, c in s['mix'].items()))
    for tag, p in res["paired"].items():
        print(f"paired[{tag}]: n={p['n']} {100*p['acc_a']:.4f}% -> {100*p['acc_b']:.4f}% "
              f"delta={1000*p['delta_mean']:+.3f}pt CI=[{1000*p['delta_ci'].low:+.3f},{1000*p['delta_ci'].high:+.3f}]pt "
              f"W/T/L={p['wins']}/{p['ties']}/{p['losses']} "
              f"cost ${p['cost_a']:.6f} -> ${p['cost_b']:.6f}")
        print(f"{'':>16}  all-8 subset: n={p['n8']} {100*p['acc_a8']:.4f}% -> {100*p['acc_b8']:.4f}% "
              f"delta={1000*p['delta_mean8']:+.3f}pt "
              f"CI=[{1000*p['delta_ci8'].low:+.3f},{1000*p['delta_ci8'].high:+.3f}]pt")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
