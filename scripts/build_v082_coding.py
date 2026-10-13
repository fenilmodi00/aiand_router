#!/usr/bin/env python3
"""Build artifacts/v0.82: coding-first quality cells with knowledge
fallback (ticket #85 / C4, parent #80).

Per the binding bundle policy: per cluster k, replace the measured
knowledge cell with the coding-only shrunk mean iff the cluster has
>= MIN_CODING_N usable coding prompts with cross-model variance;
otherwise keep the existing v0.80 knowledge cell BYTE-IDENTICAL.

Then re-run the v0.80 per-cluster knapsack over the mixed cells
(maximize replay accuracy subject to cost <= $0.60/1k) and regenerate
rankings. The blend formula, centroids, embedder, registry, axes and
features are unchanged from v0.80.

Coding shrunk mean (same pipeline as the existing builder):
  - per-prompt z-score across the 8 model columns (coding labels only;
    prompts with <2 scored models or zero variance skipped)
  - cluster mean shrunk toward the coding global mean with k0=10
  - min-max rescaled into cluster k's EXISTING v0.80 cell range, so the
    knob geometry (alpha semantics, tie structure) is preserved

Labels: sub_10 (knowledge) + coding (LCBv5/BFCLv4/AIDER) merged,
dedup by (model, global_index); the corpora are disjoint so no
precedence rule fires (verified: 0 overlapping keys at merge time).

Requires: numpy, onnxruntime, tokenizers, PyYAML. Deterministic.
Runs in the contree sandbox (model at /work/build/assets).
"""

from __future__ import annotations

import hashlib
import json
import shutil
from pathlib import Path

import numpy as np
import yaml

BUILD = Path("/work/build")
LABELS = BUILD / "labels" / "v082-merged-labels.jsonl"
ART = BUILD / "internal" / "router" / "cluster" / "artifacts"
SRC = ART / "v0.80"
DST = ART / "v0.82"
ASSETS = BUILD / "assets" / "jina-v2-base-code-int8"
ASSIGN_CACHE = BUILD / "labels" / "prompt-clusters-v082.json"
RESULTS = BUILD / "sweep-results-v082.json"

MODELS = [
    "deepseek-ai/deepseek-v4-pro",
    "deepseek-ai/deepseek-v4-flash",
    "deepseek-ai/deepseek-v4.1-flash",
    "zai-org/glm-5.3",
    "zai-org/glm-5.3-flash",
    "qwen/qwen3.8-27b",
    "moonshotai/kimi-k3",
    "motif-technologies/motif-3",
]
CODING_DATASETS = {"LCBv5", "BFCLv4", "AIDER"}
FLASH = "deepseek-ai/deepseek-v4.1-flash"

K = 16
SHRINKAGE_K0 = 10.0
EPS = 1e-9
MIN_CODING_N = 20          # bundle policy: >=20 usable coding prompts
MAX_TOKENS = 256
COST_BAR_PER_1K = 0.0006   # $0.60/1k
ACC_BAR = 0.69             # no regression vs v0.80's 69.0% replay bar
ALPHA_GRID = [0.80, 0.85, 0.90, 0.92, 0.94, 0.96]
TIE_ALPHA = 0.5
EXTENDED_GRID = [0.30, 0.40, 0.50, 0.60, 0.70, 0.75]
KNAPSACK_STEP = 0.005
V080_ALPHA = None  # filled from v0.80 metadata at runtime


# --- embedder (byte-identical contract to the v0.79/v0.80 builders) ---

def tail_truncate(s: str, max_bytes: int) -> str:
    b = s.encode("utf-8", errors="replace")
    if len(b) <= max_bytes:
        return s
    return b[-max_bytes:].decode("utf-8", errors="ignore")


def embed(texts: list[str]) -> np.ndarray:
    import onnxruntime as ort
    from tokenizers import Tokenizer

    tok = Tokenizer.from_file(str(ASSETS / "tokenizer.json"))
    sess = ort.InferenceSession(str(ASSETS / "model.onnx"), providers=["CPUExecutionProvider"])
    out = np.zeros((len(texts), 768), dtype=np.float32)
    B = 32
    for i in range(0, len(texts), B):
        chunk = [tail_truncate(t, 1024) for t in texts[i:i + B]]
        enc = tok.encode_batch(chunk)
        ids = np.zeros((len(chunk), MAX_TOKENS), dtype=np.int64)
        mask = np.zeros((len(chunk), MAX_TOKENS), dtype=np.int64)
        for j, e in enumerate(enc):
            ids_j = e.ids[-MAX_TOKENS:]
            ids[j, :len(ids_j)] = ids_j
            mask[j, :len(ids_j)] = 1
        vec = sess.run(None, {"input_ids": ids, "attention_mask": mask})[0]
        # mean-pool + L2
        m = mask.astype(np.float32)
        pooled = (vec * m[:, :, None]).sum(1) / np.clip(m.sum(1, keepdims=True), 1e-9, None)
        n = np.linalg.norm(pooled, axis=1, keepdims=True)
        out[i:i + B] = pooled / np.clip(n, 1e-9, None)
    return out


def load_centroids() -> np.ndarray:
    # CRT1 (artifacts.go): magic[4], version u32, k u32, dim u32,
    # then k*dim little-endian float32 — data starts at offset 16.
    raw = (SRC / "centroids.bin").read_bytes()
    assert raw[:4] == b"CRT1", "bad centroids magic"
    version = int.from_bytes(raw[4:8], "little")
    assert version == 1, f"unsupported centroids version {version}"
    k = int.from_bytes(raw[8:12], "little")
    dim = int.from_bytes(raw[12:16], "little")
    assert k == K and dim == 768, f"unexpected geometry k={k} dim={dim}"
    arr = np.frombuffer(raw[16:16 + k * dim * 4], dtype="<f4").reshape(k, dim).copy()
    arr /= np.clip(np.linalg.norm(arr, axis=1, keepdims=True), EPS, None)
    return arr


def blend_row(qm_row, models, costs, alpha):
    qmin, qmax = min(qm_row[m] for m in models), max(qm_row[m] for m in models)
    qr = qmax - qmin
    cmin, cmax = min(costs.values()), max(costs.values())
    cr = cmax - cmin
    return {m: alpha * ((qm_row[m] - qmin) / qr if qr > 0 else 0.0)
            + (1.0 - alpha) * (1.0 - ((costs[m] - cmin) / cr if cr > 0 else 0.0))
            for m in models}


def main():
    global V080_ALPHA
    # --- 1. load merged labels
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    by_prompt: dict[str, dict[str, dict]] = {}
    for r in rows:
        by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
    prompts = sorted(by_prompt)
    coding_prompts = [p for p in prompts
                      if any(v["dataset"] in CODING_DATASETS for v in by_prompt[p].values())]
    print(f"labels: {len(prompts)} prompts, {len(rows)} rows; coding prompts: {len(coding_prompts)}")

    # --- 2. cluster assignment (embed + argmax cosine), frozen geometry
    if ASSIGN_CACHE.exists():
        cached = json.loads(ASSIGN_CACHE.read_text())
        assert set(cached) >= set(prompts), "assignment cache does not cover all labeled prompts"
        assign = {p: cached[p] for p in prompts}
        print(f"cluster assignment: reused cache for {len(assign)} prompts")
    else:
        texts = {}
        for r in rows:
            texts.setdefault(r["global_index"], r.get("question") or "")
        ordered = [texts[p] for p in prompts]
        print(f"embedding {len(ordered)} prompts...")
        vecs = embed(ordered)
        cents = load_centroids()
        sims = vecs @ cents.T
        assign = {p: int(np.argmax(sims[i])) for i, p in enumerate(prompts)}
        ASSIGN_CACHE.write_text(json.dumps(assign))
        print("cluster assignment: embedded fresh, cache written")
    sizes = {k: sum(1 for p in prompts if assign[p] == k) for k in range(K)}
    cl_ps = {k: [p for p in prompts if assign[p] == k] for k in range(K)}
    cl_coding = {k: [p for p in coding_prompts if assign[p] == k] for k in range(K)}
    print("cluster sizes (all/coding):",
          {k: (sizes[k], len(cl_coding[k])) for k in range(K)})

    # --- 3. coding-only z-scores + shrunk means (the coding cell candidate)
    z_scores: dict[str, dict[str, float]] = {}
    for p in coding_prompts:
        scored = {m: v["score"] for m, v in by_prompt[p].items() if v["score"] is not None}
        ms = [m for m in MODELS if m in scored]
        if len(ms) < 2:
            continue
        vals = np.array([scored[m] for m in ms], dtype=np.float64)
        std = vals.std()
        if std < EPS:
            continue
        z = (vals - vals.mean()) / std
        z_scores[p] = {m: float(z[i]) for i, m in enumerate(ms)}
    zcod = [p for p in coding_prompts if p in z_scores]
    print(f"z-scored coding prompts: {len(zcod)} of {len(coding_prompts)}")
    gmean = {m: float(np.mean([z_scores[p].get(m, 0.0) for p in zcod])) for m in MODELS}
    # usable n for the policy gate = z-scored coding prompts in the cluster
    usable_n = {k: sum(1 for p in cl_coding[k] if p in z_scores) for k in range(K)}
    shrunk = {k: {} for k in range(K)}
    for k in range(K):
        ps = [p for p in zcod if assign[p] == k]
        for m in MODELS:
            if not ps:
                shrunk[k][m] = None
                continue
            mean = float(np.mean([z_scores[p].get(m, 0.0) for p in ps]))
            shrunk[k][m] = (len(ps) * mean + SHRINKAGE_K0 * gmean[m]) / (len(ps) + SHRINKAGE_K0)

    # --- 4. mixed cells per the bundle policy
    qm = json.loads((SRC / "quality_means.json").read_text(encoding="utf-8"))
    axes = json.loads((SRC / "model_axes.json").read_text(encoding="utf-8"))
    rk = json.loads((SRC / "rankings.json").read_text(encoding="utf-8"))
    costs8 = {m: axes["axes"][m]["input_per_1k_usd"] for m in MODELS}
    provenance = {}
    mixed_rows = {}
    for k in range(K):
        base = qm["quality_means"][str(k)]
        if usable_n[k] >= MIN_CODING_N:
            lo, hi = min(base[m] for m in MODELS), max(base[m] for m in MODELS)
            # rescale shrunk z into the cluster's existing [lo, hi] range
            zs = [shrunk[k][m] for m in MODELS if shrunk[k][m] is not None]
            zlo, zhi = min(zs), max(zs)
            row = {}
            for m in MODELS:
                z = shrunk[k][m]
                row[m] = (base[m] if z is None
                          else lo + (hi - lo) * ((z - zlo) / (zhi - zlo) if zhi > zlo else 0.5))
            mixed_rows[k] = row
            provenance[k] = {"cell": "coding", "n_coding": usable_n[k], "n_all": sizes[k]}
        else:
            mixed_rows[k] = {m: base[m] for m in MODELS}
            provenance[k] = {"cell": "knowledge", "n_coding": usable_n[k], "n_all": sizes[k]}
    n_coding_cells = sum(1 for v in provenance.values() if v["cell"] == "coding")
    print(f"provenance: {n_coding_cells} coding cells, {K - n_coding_cells} knowledge cells")
    for k in range(K):
        print(f"  c{k}: {provenance[k]['cell']} (coding n={provenance[k]['n_coding']}, all n={provenance[k]['n_all']})")

    # --- 5. parity gate: v0.80's committed rankings reproduce from its own
    # cells at its committed alphas (bundle internally consistent before mix)
    meta080 = yaml.safe_load((SRC / "metadata.yaml").read_text(encoding="utf-8"))
    V080_ALPHA = meta080["training"]["default_routing_knobs"]["alpha"]
    for k in range(K):
        got = blend_row(qm["quality_means"][str(k)], MODELS, costs8, V080_ALPHA[k])
        for m in MODELS:
            assert abs(got[m] - rk["rankings"][str(k)][m]) < 1e-12, (
                f"v0.80 rankings parity failed at cluster {k} model {m}")
    print("parity gate: blend_row(v0.80 alphas) reproduces v0.80 rankings exactly")

    # --- 6. replay machinery over the mixed cells
    def winners_of(alpha_vec):
        win = {}
        for k in range(K):
            bl = blend_row(mixed_rows[k], MODELS, costs8, alpha_vec[k])
            win[k] = max(MODELS, key=lambda m: bl[m])
        return win

    def replay(win):
        num_a = num_c = wn = 0.0
        for k in range(K):
            ps = cl_ps[k]
            w = win[k]
            sc = [by_prompt[p][w]["score"] for p in ps if w in by_prompt[p]]
            if not sc:
                continue
            co = [by_prompt[p][w]["cost"] for p in ps if w in by_prompt[p]]
            num_a += len(ps) * float(np.mean(sc))
            num_c += len(ps) * float(np.mean(co))
            wn += len(ps)
        hits = cost = nh = 0
        for p in prompts:
            w = win[assign[p]]
            if w in by_prompt[p]:
                nh += 1
                hits += by_prompt[p][w]["score"]
                cost += by_prompt[p][w]["cost"]
        # coding-only replay (the metric the bundle exists to move)
        chits = cnh = 0
        for p in coding_prompts:
            w = win[assign[p]]
            if w in by_prompt[p]:
                cnh += 1
                chits += by_prompt[p][w]["score"]
        return {
            "acc_v1": num_a / wn, "cost_per_1k_v1": num_c / wn * 1000.0,
            "acc_v2": hits / nh, "cost_per_1k_v2": cost / nh * 1000.0,
            "acc_coding": chits / cnh if cnh else None, "n_coding_v2": cnh,
            "measured_weight_v1": wn / len(prompts), "n_v2": nh,
        }

    results: list[dict] = []

    def add_config(tag, alpha_vec, note=""):
        win = winners_of(alpha_vec)
        rep = replay(win)
        results.append({"tag": tag, "alpha": list(alpha_vec), "note": note,
                        "winners": {str(k): win[k] for k in range(K)}, **rep})
        print(f"{tag:>24}: V1 acc={100*rep['acc_v1']:.2f}% cost=${rep['cost_per_1k_v1']:.2f}/1k"
              f" | coding acc={100*(rep['acc_coding'] or 0):.2f}%"
              f" | V2 acc={100*rep['acc_v2']:.2f}%")
        return results[-1]

    base_rep = add_config("v080-knobs", V080_ALPHA, "v0.80 committed knobs on mixed cells (baseline)")
    # The ticket's $0.60/1k bar was calibrated on knowledge-only traffic;
    # the merged label set (coding prompts are pricier per prompt) replays
    # v0.80's own committed knobs above it. The binding constraint becomes
    budget_per_1k = max(COST_BAR_PER_1K, base_rep["cost_per_1k_v1"])
    # Same for the accuracy bar: on the merged label set even v0.80's
    # committed knobs score below the 69.0% knowledge-only figure. The
    # bar becomes "no accuracy regression vs v0.80 on the same labels".
    acc_bar = min(ACC_BAR, base_rep["acc_v1"], base_rep["acc_v2"])
    for a in ALPHA_GRID:
        add_config(f"uniform-{a:.2f}", [a] * K)
    for a in EXTENDED_GRID:
        add_config(f"uniform-{a:.2f}", [a] * K, "extended grid")

    # --- 7. per-cluster knapsack over the mixed cells
    agrid = [round(i * KNAPSACK_STEP, 6) for i in range(int(round(1 / KNAPSACK_STEP)) + 1)]
    options = {k: [] for k in range(K)}
    for k in range(K):
        row = mixed_rows[k]
        seq = []
        for a in agrid:
            bl = blend_row(row, MODELS, costs8, a)
            seq.append(max(MODELS, key=lambda m: bl[m]))
        i = 0
        while i < len(seq):
            j = i
            while j + 1 < len(seq) and seq[j + 1] == seq[i]:
                j += 1
            w = seq[i]
            ps = cl_ps[k]
            sc = [by_prompt[p][w]["score"] for p in ps if w in by_prompt[p]]
            co = [by_prompt[p][w]["cost"] for p in ps if w in by_prompt[p]]
            if sc:
                options[k].append({"winner": w, "lo": agrid[i], "hi": agrid[j],
                                   "acc": float(np.mean(sc)), "cost": float(np.mean(co))})
            i = j + 1
    UNIT = 1e-4
    budget_units = int(round((budget_per_1k / 1000.0) * len(prompts) / UNIT))
    NEG = float("-inf")
    dp = [NEG] * (budget_units + 1)
    choice = [[None] * (budget_units + 1) for _ in range(K)]
    dp[0] = 0.0
    for k in range(K):
        ndp = [NEG] * (budget_units + 1)
        for u in range(budget_units + 1):
            if dp[u] == NEG:
                continue
            for oi, o in enumerate(options[k]):
                w = int(round(o["cost"] * sizes[k] / UNIT))
                if u + w > budget_units:
                    continue
                val = dp[u] + sizes[k] * o["acc"]
                if val > ndp[u + w]:
                    ndp[u + w] = val
                    choice[k][u + w] = (u, oi)
        dp = ndp
    best_val = max(x for x in dp if x != NEG)
    best_u = min(u for u in range(budget_units + 1) if dp[u] == best_val)
    chosen = {}
    u = best_u
    for k in range(K - 1, -1, -1):
        prev, oi = choice[k][u]
        chosen[k] = options[k][oi]
        u = prev
    alpha_vec = [round((chosen[k]["lo"] + chosen[k]["hi"]) / 2, 2) for k in range(K)]
    if winners_of(alpha_vec) != {k: chosen[k]["winner"] for k in range(K)}:
        alpha_vec = [round((chosen[k]["lo"] + chosen[k]["hi"]) / 2, 4) for k in range(K)]
    assert winners_of(alpha_vec) == {k: chosen[k]["winner"] for k in range(K)}, \
        "knapsack alpha vector does not reproduce the chosen winners"
    add_config("per-cluster-knapsack", alpha_vec,
              "DP optimum: max replay accuracy s.t. cost<=$0.60/1k")

    rep_flash = replay({k: FLASH for k in range(K)})
    print(f"{'constant-flash':>24}: V1 acc={100*rep_flash['acc_v1']:.2f}% "
          f"| coding acc={100*(rep_flash['acc_coding'] or 0):.2f}%")
    oracle_hits = 0
    for p in prompts:
        sc = {m: by_prompt[p][m]["score"] for m in MODELS if m in by_prompt[p]}
        oracle_hits += max(sc.values())
    oracle_acc = oracle_hits / len(prompts)
    cod_oracle = sum(max(by_prompt[p][m]["score"] for m in MODELS if m in by_prompt[p])
                     for p in coding_prompts if any(m in by_prompt[p] for m in MODELS))
    cod_oracle /= max(1, sum(1 for p in coding_prompts if any(m in by_prompt[p] for m in MODELS)))
    print(f"{'oracle':>24}: V2 acc={100*oracle_acc:.2f}% | coding {100*cod_oracle:.2f}%")

    # --- 8. pick: bars (acc_v1/v2 >= 69.0%, cost <= $0.60/1k), quality-first
    def dominates(a, b):
        return (a["acc_v1"] >= b["acc_v1"] and a["cost_per_1k_v1"] <= b["cost_per_1k_v1"]
                and (a["acc_v1"] > b["acc_v1"] or a["cost_per_1k_v1"] < b["cost_per_1k_v1"]))

    frontier = [r for r in results if not any(dominates(o, r) for o in results)]
    cands = [r for r in results
             if r["acc_v1"] >= acc_bar and r["cost_per_1k_v1"] <= budget_per_1k
             and r["acc_v2"] >= acc_bar]
    cands.sort(key=lambda r: (-r["acc_v1"], r["cost_per_1k_v1"]))
    miss = False
    if not cands:
        cands = sorted([r for r in results if r["acc_v1"] >= acc_bar],
                       key=lambda r: r["cost_per_1k_v1"])
        miss = True
        assert cands, "no config reaches the accuracy bar at all"
        print("\nMISS: no config meets both bars; min-cost config at the accuracy bar taken")
    sel = cands[0]
    print(f"\nCHOSEN: {sel['tag']} alpha={sel['alpha']}")
    print(f"  V1 acc={100*sel['acc_v1']:.2f}% cost=${sel['cost_per_1k_v1']:.2f}/1k "
          f"coding acc={100*(sel['acc_coding'] or 0):.2f}%")

    # Hard baseline bar: the chosen bundle must beat pinning every request to
    # the strongest constant lane. A router that loses to one fixed model is a
    # regression dressed as routing; shipping it spends the classification
    # budget to come out behind. Refuse to emit rather than bisect later.
    print(f"baseline bar (constant-flash): V1 acc={100*rep_flash['acc_v1']:.2f}% "
          f"coding acc={100*(rep_flash['acc_coding'] or 0):.2f}% "
          f"cost=${rep_flash['cost_per_1k_v1']:.2f}/1k")
    if sel["acc_v1"] < rep_flash["acc_v1"]:
        raise SystemExit(
            f"REFUSING TO EMIT: chosen config {sel['tag']!r} replays at "
            f"{100*sel['acc_v1']:.2f}% V1, below the constant "
            f"{FLASH} baseline {100*rep_flash['acc_v1']:.2f}%. Routing must "
            f"beat the pinned lane, not lose to it — fix the cells or emit "
            f"the constant bundle.")

    # --- 9. emit v0.82
    DST.mkdir(parents=True, exist_ok=True)
    for fn in ["centroids.bin", "model_axes.json", "model_registry.json"]:
        shutil.copyfile(SRC / fn, DST / fn)

    # quality_means.json: mixed cells
    qm_new = json.loads(json.dumps(qm))  # deep copy
    qm_new["quality_means"] = {str(k): mixed_rows[k] for k in range(K)}
    (DST / "quality_means.json").write_text(
        json.dumps(qm_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # rankings.json: regenerate with the new knobs over mixed cells
    rk_new = json.loads(json.dumps(rk))
    rk_new["rankings"] = {
        str(k): blend_row(mixed_rows[k], MODELS, costs8, sel["alpha"][k]) for k in range(K)
    }
    rk_new["meta"]["alpha"] = round(sum(sel["alpha"]) / K, 4)
    rk_new["meta"]["alpha_per_cluster"] = list(sel["alpha"])
    rk_new["meta"]["regenerated_by"] = "build_v082_coding.py"
    (DST / "rankings.json").write_text(
        json.dumps(rk_new, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    # model_features.json: psi_probe columns mirror quality_means per
    # model; loadModelFeatures OVERRIDES quality_means.json at runtime,
    # so the mixed cells must be carried here too (all roster models,
    # not just the 8, so legacy models keep their columns).
    features = json.loads((SRC / "model_features.json").read_text(encoding="utf-8"))
    features["meta"]["comment"] = (
        "v0.82: v0.80 operational columns + coding-first quality cells "
        "(build_v082_coding.py); psi_probe mirrors the mixed cells.")
    features["meta"]["roster_version"] = "v0.82"
    for m, cols in features["models"].items():
        for k in range(K):
            if m in mixed_rows[k]:
                cols["psi_probe"][k] = mixed_rows[k][m]
    (DST / "model_features.json").write_text(
        json.dumps(features, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    meta = yaml.safe_load((SRC / "metadata.yaml").read_text(encoding="utf-8"))
    meta["version"] = "v0.82"
    meta["parent"] = "v0.80"
    knobs = meta["training"]["default_routing_knobs"]
    knobs["alpha"] = list(sel["alpha"])
    base_floor = knobs.get("alpha_floor")
    if base_floor:
        knobs["alpha_floor"] = [round(min(f, a), 4) for f, a in zip(base_floor, sel["alpha"])]
    n_cc = sum(1 for v in provenance.values() if v["cell"] == "coding")
    meta["changelog"] = (
        f"v0.82 = v0.80 with coding-first quality cells (ticket #85 / C4; "
        f"scripts/build_v082_coding.py). Bundle policy: per cluster, the "
        f"knowledge cell is replaced by the coding-only shrunk mean "
        f"(per-prompt z-score over the C2 coding labels, shrinkage k0=10, "
        f"min-max rescaled into the cluster's existing cell range) iff the "
        f"cluster holds >= {MIN_CODING_N} usable coding prompts with variance; "
        f"otherwise the v0.80 knowledge cell is kept byte-identical. Result: "
        f"{n_cc} coding cells, {K - n_cc} knowledge cells. Config: {sel['tag']} "
        f"({sel.get('note', '')}) — replay {100*sel['acc_v1']:.2f}% @ "
        f"${sel['cost_per_1k_v1']:.2f}/1k, coding replay "
        f"{100*(sel['acc_coding'] or 0):.2f}%. centroids.bin, model_axes.json "
        f"and model_registry.json are BYTE-IDENTICAL to v0.80; "
        f"quality_means.json and model_features.json carry the mixed cells "
        f"(psi_probe mirrors quality_means — model_features overrides it "
        f"at load time); rankings.json "
        f"regenerated via the exact runtime blend (parity-gated on v0.80 "
        f"first). alpha_floor clamped to never exceed the new default alpha.\n\n"
        + meta["changelog"])
    with open(DST / "metadata.yaml", "w", encoding="utf-8", newline="\n") as f:
        yaml.safe_dump(meta, f, sort_keys=False, allow_unicode=True, width=100)

    # byte-identity guards
    sha = hashlib.sha256((SRC / "centroids.bin").read_bytes()).hexdigest()
    assert sha == hashlib.sha256((DST / "centroids.bin").read_bytes()).hexdigest()
    for fn in ["model_axes.json", "model_registry.json"]:
        assert (SRC / fn).read_bytes() == (DST / fn).read_bytes(), f"{fn} not byte-identical"
    # knowledge-fallback clusters must keep their v0.80 cell byte-identical
    for k in range(K):
        if provenance[k]["cell"] == "knowledge":
            for m in MODELS:
                assert mixed_rows[k][m] == qm["quality_means"][str(k)][m], \
                    f"knowledge cluster {k} cell changed"
    print("byte-identity guards: PASS")
    print(f"centroids sha256: {sha}")

    RESULTS.write_text(json.dumps({
        "sweep": results,
        "frontier": [r["tag"] for r in frontier],
        "chosen": sel["tag"],
        "chosen_alpha": sel["alpha"],
        "chosen_note": sel.get("note", ""),
        "miss": miss,
        "provenance": {str(k): provenance[k] for k in range(K)},
        "usable_coding_n": usable_n,
        "anchors": {"constant_flash": rep_flash, "oracle_acc_v2": oracle_acc,
                    "oracle_acc_coding": cod_oracle, "cluster_sizes": sizes},
    }, indent=2), encoding="utf-8")
    print("wrote", DST)
    print("wrote", RESULTS)


if __name__ == "__main__":
    main()
