#!/usr/bin/env python3
"""Per-class (AIDER vs rest) contribution report for the v0.84 corpus rebuild.

Throwaway analysis helper (kept because the bake-off's per-class table is
regenerated from it). Computes, from `.context/label-runs/v082-expanded-labels.jsonl`
+ `.context/label-runs/prompt-clusters-v084.json`:

  1. AIDER rows per model with mean score (all rows, and with the newest 80
     (model, global_index) rows excluded = "before")
  2. AIDER prompts per cluster
  3. AIDER vs non-AIDER accuracy per (cluster, model) with n
  4. share of the newest 80 rows' clusters each class contributed
  5. clone of the bake-off routing (mirror of scripts/bakeoff_v084.py, minimal):
     which v0.84 cluster each AIDER prompt lands in and what the capped/uncapped
     arms pick, so the class contribution to the routed mix is attributable
"""
from __future__ import annotations

import collections
import json
import sys
from pathlib import Path

import yaml

REPO = Path(__file__).resolve().parent.parent
LABELS = REPO / ".context/label-runs/v082-expanded-labels.jsonl"
ASSIGN = REPO / ".context/label-runs/prompt-clusters-v084.json"
ART = REPO / "internal/router/cluster/artifacts/v0.84"
K = 16
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
SHORT = {m: m.split("/")[-1] for m in MODELS}


def load():
    rows = [json.loads(l) for l in LABELS.read_text(encoding="utf-8").splitlines() if l.strip()]
    assign = json.loads(ASSIGN.read_text(encoding="utf-8"))
    return rows, assign


def main() -> int:
    rows, assign = load()

    # === identify the 80 newly appended rows ============================
    # The corpus grew from 14,319 to 14,399 rows by appending 80 graded AIDER
    # rows at the end of the file (verified: lines[14319:] are the 80 new rows,
    # all dataset == "AIDER"). They cover 71 existing AIDER_0..149 prompts on
    # clusters c0/c5/c8/c10/c12. "Before" = the 14,319-row prefix.
    new_pairs = {(r["model"], r["global_index"]) for r in rows[-80:]}
    assert len(new_pairs) == 80, f"expected 80 new rows, got {len(new_pairs)}"
    assert all(r["dataset"] == "AIDER" for r in rows[-80:]), "tail rows must be AIDER"
    nc = collections.Counter(assign[r["global_index"]] for r in rows[-80:])
    print("new 80 rows by cluster:", dict(sorted(nc.items())))
    nmp = len(set(r["global_index"] for r in rows[-80:]))
    print(f"new 80 rows cover {nmp} prompts; "
          f"clusters: " + ", ".join(f"c{c}" for c in sorted(set(assign[r['global_index']] for r in rows[-80:]))))

    print("=" * 78)
    print("AIDER rows per model (newest 80 marked; 'before' = excluding them)")
    print("=" * 78)
    print(f"{'model':22} {'n':>4} {'mean':>8} {'sel':>4} {'mean*':>8} |")
    for m in MODELS:
        a = [r for r in rows if r["dataset"] == "AIDER" and r["model"] == m]
        n = len(a)
        mean = sum(r["score"] for r in a) / n if n else float("nan")
        sel = sum(1 for r in a if (r["model"], r["global_index"]) in new_pairs)
        kept = [r for r in a if (r["model"], r["global_index"]) not in new_pairs]
        mb = sum(r["score"] for r in kept) / len(kept) if kept else float("nan")
        print(f"{SHORT[m]:22} {n:>4} {mean:>8.4f} {sel:>4} {mb:>8.4f}")

    print()
    print("=" * 78)
    print("AIDER prompts per cluster")
    print("=" * 78)
    apc = collections.Counter(assign[r["global_index"]] for r in rows
                               if r["dataset"] == "AIDER")
    for c in range(K):
        old = sum(1 for r in rows if r["dataset"] == "AIDER"
                  and (r["model"], r["global_index"]) not in new_pairs
                  and assign[r["global_index"]] == c)
        new = sum(1 for r in rows if r["dataset"] == "AIDER"
                  and (r["model"], r["global_index"]) in new_pairs
                  and assign[r["global_index"]] == c)
        if old or new:
            print(f"c{c:<2} prompts={apc[c]:>3}  aider rows: {old:>4} before + {new:>3} new")

    print()
    print("=" * 78)
    print("accuracy per (cluster, model): AIDER vs non-AIDER")
    print("=" * 78)
    print(f"{'c':>3} {'model':21} {'nA':>4} {'AIDER':>8} {'nR':>4} {'rest':>8}")
    for c in range(K):
        for m in MODELS:
            a = [r for r in rows if assign.get(r["global_index"]) == c
                 and r["model"] == m and r["dataset"] == "AIDER"]
            o = [r for r in rows if assign.get(r["global_index"]) == c
                 and r["model"] == m and r["dataset"] != "AIDER"]
            if not a and not o:
                continue
            ma = sum(r["score"] for r in a) / len(a) if a else float("nan")
            mo = sum(r["score"] for r in o) / len(o) if o else float("nan")
            fa = f"{ma:.4f}" if a else "  --  "
            fo = f"{mo:.4f}" if o else "  --  "
            print(f"{c:>3} {SHORT[m]:21} {len(a):>4} {fa:>8} {len(o):>4} {fo:>8}")

    print()
    print("=" * 78)
    print("share of the new 80 rows' clusters contributed by each class")
    print("=" * 78)
    newrows = [r for r in rows if (r["model"], r["global_index"]) in new_pairs]
    for c in sorted(set(assign[r["global_index"]] for r in newrows)):
        sub = [r for r in newrows if assign[r["global_index"]] == c]
        bymodel = collections.defaultdict(list)
        for r in sub:
            bymodel[SHORT[r["model"]]].append(r["score"])
        detail = ", ".join(f"{k} {'/'.join(f'{s:.0f}' for s in v)}" for k, v in bymodel.items())
        print(f"c{c}: {len(sub)} rows over {len(set(r['global_index'] for r in sub))} prompts, "
              f"{len(bymodel)} models -> {detail}")

    print()
    print("=" * 78)
    print("shipped cells (v0.84 quality_means.json) + provenance")
    print("=" * 78)
    qm = json.loads((ART / "quality_means.json").read_text(encoding="utf-8"))
    prov = qm.get("meta", {}).get("v084_provenance", {})
    print(f"{'c':>3} {'n':>4} {'n_all':>5} {'provenance':>15}")
    for c in range(K):
        p = prov.get(str(c), {})
        print(f"{c:>3} {p.get('n', '--'):>4} {p.get('n_all', '--'):>5} "
              f"{str(p.get('cell', '--')):>15}")

    print()
    print("=" * 78)
    print("measured-cell reproduction: per-(cluster,model) shrunk z-mean")
    print("=" * 78)
    print("Mirror of scripts/build_v084_measured_cells.py step 2 (shrink toward the")
    print("model's global z mean, k0=10) on the whole roster columns of each prompt,")
    print("run once with all 14,399 rows ('after') and once on the 14,319-row prefix")
    print("('before'), so the incremental effect of the 80 new cells is visible.")

    def shrunk_means(rowslice):
        by_prompt = {}
        for r in rowslice:
            by_prompt.setdefault(r["global_index"], {})[r["model"]] = r
        z = {}
        for p in by_prompt:
            sc = {m: v["score"] for m, v in by_prompt[p].items()
                  if m in MODELS and v.get("score") is not None}
            ms = [m for m in MODELS if m in sc]
            if len(ms) < 2:
                continue
            vals = [sc[m] for m in ms]
            mean = sum(vals) / len(vals)
            var = sum((v - mean) ** 2 for v in vals) / len(vals)
            std = var ** 0.5
            if std < 1e-9:
                continue
            z[p] = {m: (sc[m] - mean) / std for m in ms}
        gmean = {m: sum(z[p][m] for p in z if m in z[p])
                 / max(1, sum(1 for p in z if m in z[p])) for m in MODELS}
        out = {}
        for c in range(K):
            ps = [p for p in z if assign.get(p) == c]
            out[c] = {}
            for m in MODELS:
                s = [z[p][m] for p in ps if m in z[p]]
                if not s:
                    out[c][m] = None
                    continue
                mu = sum(s) / len(s)
                out[c][m] = (len(s) * mu + 10.0 * gmean[m]) / (len(s) + 10.0)
            out[c]["_n"] = len(ps)
        return out

    before = shrunk_means(rows[:-80])
    after = shrunk_means(rows)
    print(f"{'c':>3} {'n_b':>4} {'n_a':>4} {'model':21} {'z before':>9} {'z after':>9} {'delta':>8}")
    for c in range(K):
        for m in MODELS:
            b, a = before[c].get(m), after[c].get(m)
            if b is None and a is None:
                continue
            fb = f"{b:9.4f}" if b is not None else "      -- "
            fa = f"{a:9.4f}" if a is not None else "      -- "
            d = f"{a - b:+8.4f}" if (b is not None and a is not None) else "     -- "
            print(f"{c:>3} {before[c]['_n']:>4} {after[c]['_n']:>4} {SHORT[m]:21} {fb} {fa} {d}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
