"""Ticket #90 remainder grader: re-grade AIDER / LCBv5 / LiveCodeBench rows
that carry a non-empty generation but no evaluation_result.

Zero inference spend: only cached generations are graded. Every score comes
from the arena's real scorer functions (llm_evaluation/metrics.py:

  * AIDER      -> metrics.aider_accuracy   (run test_command, 1.0/0.0)
  * LCBv5      -> metrics.code_accuracy
  * LiveCodeBench -> metrics.code_accuracy

Ground truth comes from the arena's own corpus files, and cost from the
corpus convention in .context/label-runs/empty_cells_merge.py::canon_c5
(per-model token prices; $/1M -> $/1k).

The AIDER exercises run `sed -i ... *.spec.js && npm run test`, `go test`,
`cargo test`, and `pytest`, so grading executes inside WSL Ubuntu (which has
sed/go/python3; cargo is absent fleet-wide, see note below) with the same
environment the original grading used (~/router-arena-venv/bin/python3).

Usage (from the repo root):
    python scripts/grade_aider_remainder.py           # grade + append
    python scripts/grade_aider_remainder.py --dry-run # grade, print, don't append
"""
from __future__ import annotations

import argparse
import collections
import json
import os
import subprocess
import sys
from pathlib import Path

REPO = Path("D:/router")
ARENA = REPO.parent / "router-arena"
CR = ARENA / "cached_results"
CORPUS = REPO / ".context" / "label-runs" / "v082-expanded-labels.jsonl"
CLUSTERS = REPO / ".context" / "label-runs" / "prompt-clusters-v082-full.json"
GT = {
    "AIDER": ARENA / "dataset" / "aider_polyglot.jsonl",
    "LCBv5": ARENA / "dataset" / "lcbv5.jsonl",
    "LiveCodeBench": ARENA / "dataset" / "livecodebench_heldout.jsonl",
}

# canon_c5()'s price table, $/1M tokens -> $/1k.
PRICE = {
    "deepseek-ai/deepseek-v4-flash": (0.00015, 0.00025),
    "deepseek-ai/deepseek-v4.1-flash": (0.00030, 0.00060),
    "deepseek-ai/deepseek-v4-pro": (0.00100, 0.00250),
    "zai-org/glm-5.3": (0.00100, 0.00400),
    "zai-org/glm-5.3-flash": (0.00015, 0.00050),
    "moonshotai/kimi-k3": (0.00300, 0.01250),
    "qwen/qwen3.8-27b": (0.00040, 0.00300),
    "motif-technologies/motif-3": (0.00050, 0.00200),
}
METRIC = {"AIDER": "aider_accuracy", "LCBv5": "code_accuracy",
          "LiveCodeBench": "code_accuracy"}
# Step 4: the one coding row the fleet left success=true but answer-less
# (deepseek-v4-flash / BFCLv4_229, silent null). The arena's own evaluator
# wrote metric="error" for it and it never reached the label file; it is
# graded here with the real bfcl_ast scorer over an empty prediction, which
# is exactly what the model produced.
SILENT_NULLS = [("deepseek-ai/deepseek-v4-flash", "BFCLv4_229")]
BFCL_GT = ARENA / "dataset" / "bfclv4.jsonl"

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
# qwen's AIDER gap was filled by a Contree run whose answers live in a
# separate cache; treat them as qwen cells.
AUX_CACHES = {"qwen__qwen3.8-27b.contree-aider.jsonl": "qwen/qwen3.8-27b"}

CODING_PREFIXES = ("AIDER_", "LCBv5_", "LiveCodeBench_")


def dataset_of(gi: str) -> str:
    for p in ("AIDER", "LCBv5", "LiveCodeBench"):
        if gi.startswith(p + "_"):
            return p
    raise ValueError(f"unknown prefix: {gi}")


def load_cache(path: Path) -> dict[str, dict]:
    out = {}
    if not path.exists():
        return out
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line.strip():
            r = json.loads(line)
            out[r["global_index"]] = r
    return out


def load_ground_truth(ds: str, gis: set[str]) -> dict[str, dict]:
    """Stream the (possibly huge) arena ground-truth file for the gis we need."""
    found: dict[str, dict] = {}
    if ds == "LiveCodeBench":
        # 3.2 GB file: never read it whole; scan line-by-line for our ids.
        with open(GT[ds], encoding="utf-8") as f:
            for line in f:
                if '"LiveCodeBench_' not in line:
                    continue
                r = json.loads(line)
                if r.get("Global Index") in gis:
                    found[r["Global Index"]] = r
        return found
    for line in GT[ds].read_text(encoding="utf-8").splitlines():
        if line.strip():
            r = json.loads(line)
            if r["Global Index"] in gis:
                found[r["Global Index"]] = r
    return found


def find_ungraded() -> list[dict]:
    """Rows to grade: non-empty generated_answer, no evaluation_result score."""
    caches = [(CR / f"{m.replace('/', '__')}.jsonl", m) for m in MODELS]
    caches += [(CR / name, mapped) for name, mapped in sorted(AUX_CACHES.items())]
    todo = []
    for cache_path, model in caches:
        for gi, r in sorted(load_cache(cache_path).items()):
            if not gi.startswith(CODING_PREFIXES):
                continue  # non-coding rows are out of scope
            ds = dataset_of(gi)
            er = r.get("evaluation_result") or {}
            if er.get("score") is not None:
                continue
            ans = r.get("generated_answer")
            if not ans or not str(ans).strip():
                continue
            todo.append({"model": model, "global_index": gi, "dataset": ds,
                         "row": r, "cache": cache_path.name})
    # The aux (Contree) cache is a second source for the same qwen cells; the
    # main qwen cache wins where both carry an answer for the same row.
    best: dict[tuple[str, str], dict] = {}
    for t in todo:
        k = (t["model"], t["global_index"])
        prev = best.get(k)
        if prev is None or (prev["cache"].startswith("contree-aider")
                            and not t["cache"].startswith("contree-aider")):
            best[k] = t
    return list(best.values())


def cost_for(model: str, tu: dict) -> float:
    ti = float((tu or {}).get("input_tokens") or 0)
    to = float((tu or {}).get("output_tokens") or 0)
    pi, po = PRICE[model]
    return round(ti * pi / 1000.0 + to * po / 1000.0, 8)


def grade_in_wsl(items: list[dict], dry_run: bool) -> list[dict]:
    """Call the arena scorers inside WSL, where sed/npm/go/pytest all exist.

    One python process handles the whole batch so each scorer import happens
    once; results stream back as one JSON line per row.
    """
    driver = r'''
import json, sys
sys.path.insert(0, "/mnt/d/router-arena/llm_evaluation")
from metrics import aider_accuracy, code_accuracy

items = json.loads(sys.stdin.read())
for it in items:
    ds, ans, gt = it["dataset"], it["answer"], it["gt"]
    scorer = aider_accuracy if ds == "AIDER" else code_accuracy
    try:
        if ds == "AIDER":
            score, detail = scorer(ans, gt)
            out = {
                "score": float(score),
                "detail": {
                    "returncode": detail[0].get("returncode"),
                    "timeout": detail[0].get("timeout", False),
                    "failure_marker": detail[0].get("failure_marker"),
                    "output_tail": (detail[0].get("output_tail") or "")[-400:],
                },
            }
        elif ds == "BFCLv4":
            from metrics import bfcl_ast
            score, raw = bfcl_ast(ans, gt)
            d = raw[0]
            out = {
                "score": float(score),
                "detail": {"valid": d.get("valid"),
                           "error_type": d.get("error_type"),
                           "error": d.get("error")},
            }
        else:
            score, raw = scorer(ans, gt)
            d = raw[0]
            out = {
                "score": float(score),
                "detail": {"has_code": d.get("has_code"),
                           "extracted_len": len(d.get("extracted_code") or "")},
            }
    except Exception as e:
        out = {"score": None, "error": f"{type(e).__name__}: {e}"}
    print(json.dumps({"global_index": it["global_index"], **out}), flush=True)
'''
    payload = [{"global_index": it["global_index"], "dataset": it["dataset"],
                "answer": it["row"]["generated_answer"], "gt": it["gt"]}
               for it in items]
    proc = subprocess.run(
        ["wsl", "-e", "bash", "-c",
         "~/router-arena-venv/bin/python3 -c " + quote_arg(driver)],
        input=json.dumps(payload), capture_output=True, text=True, timeout=7200,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"WSL driver failed: {proc.stderr[-2000:]}")
    out = {}
    for line in proc.stdout.splitlines():
        if line.strip():
            d = json.loads(line)
            out[d["global_index"]] = d
    return [out.get(it["global_index"], {"global_index": it["global_index"],
                                         "score": None,
                                         "error": "no result line"})
            for it in items]


def quote_arg(s: str) -> str:
    return "'" + s.replace("'", "'\"'\"'") + "'"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--limit", type=int, default=0, help="grade first N only")
    args = ap.parse_args()

    todo = find_ungraded()
    # Never re-append a cell the corpus already covers.
    have = set()
    if CORPUS.exists():
        for line in CORPUS.read_text(encoding="utf-8").splitlines():
            if line.strip():
                r = json.loads(line)
                have.add((r["model"], r["global_index"], r["dataset"]))
    dup = [t for t in todo if (t["model"], t["global_index"], t["dataset"]) in have]
    todo = [t for t in todo if (t["model"], t["global_index"], t["dataset"]) not in have]
    print(f"ungraded rows with a non-empty answer: {len(todo)} "
          f"(skipped {len(dup)} already present in the corpus)")
    if args.limit:
        todo = todo[:args.limit]

    # Ground truth per dataset.
    by_ds = collections.defaultdict(set)
    for t in todo:
        by_ds[t["dataset"]].add(t["global_index"])
    gt = {}
    for ds, gis in by_ds.items():
        print(f"loading {ds} ground truth for {len(gis)} rows ...", flush=True)
        gt.update(load_ground_truth(ds, gis))
    for t in todo:
        t["gt"] = gt.get(t["global_index"])

    missing = [t for t in todo if not t["gt"]]
    gradable = [t for t in todo if t["gt"]]
    print(f"gradable={len(gradable)} missing_gt={len(missing)}")
    for t in missing[:10]:
        print("  MISSING GT:", t["model"], t["global_index"])

    # Grade per dataset; results are keyed by (model, global_index) because
    # several models share a global_index on the same prompt.
    results = {}
    for ds in ("AIDER", "LCBv5", "LiveCodeBench"):
        sub = [t for t in gradable if t["dataset"] == ds]
        if not sub:
            continue
        print(f"\n=== grading {ds}: {len(sub)} rows ===", flush=True)
        for t, r in zip(sub, grade_in_wsl(sub, args.dry_run)):
            results[(t["model"], t["global_index"])] = r
            print(f"  {t['model']:34s} {r['global_index']:22s} score={r['score']}",
                  flush=True)

    clusters = json.loads(CLUSTERS.read_text(encoding="utf-8"))
    out_rows = []
    errors = []
    for t in gradable:
        res = results.get((t["model"], t["global_index"]), {})
        if res.get("score") is None:
            errors.append((t["model"], t["global_index"], res.get("error")
                           or "no result"))
            continue
        tu = t["row"].get("token_usage") or {}
        out_rows.append({
            "model": t["model"],
            "global_index": t["global_index"],
            "dataset": t["dataset"],
            "score": res["score"],
            "metric": METRIC[t["dataset"]],
            "cost": cost_for(t["model"], tu),
            "question": t["row"].get("question", ""),
            "token_usage": {
                "input_tokens": tu.get("input_tokens"),
                "output_tokens": tu.get("output_tokens"),
                "total_tokens": tu.get("total_tokens"),
            },
            "_cluster": clusters.get(t["global_index"]),
            "_detail": res.get("detail"),
            "_error": res.get("error"),
        })

    # Step 4: grade the lone success=true silent-null row with bfcl_ast.
    for model, gi in SILENT_NULLS:
        if (model, gi, "BFCLv4") in have:
            print(f"  {model} {gi}: already in corpus, skipped")
            continue
        cache = load_cache(CR / f"{model.replace('/', '__')}.jsonl")
        row = cache.get(gi)
        if row is None:
            print(f"  {model} {gi}: not in cache, skipped")
            continue
        gt = {}
        for line in BFCL_GT.read_text(encoding="utf-8").splitlines():
            if line.strip():
                r = json.loads(line)
                if r["Global Index"] == gi:
                    gt = r
        if not gt:
            print(f"  {model} {gi}: no ground truth, skipped")
            continue
        print(f"\n=== grading {model} {gi} (success=true, empty answer) ===",
              flush=True)
        res = grade_in_wsl([{"model": model, "global_index": gi,
                             "dataset": "BFCLv4",
                             "row": dict(row, generated_answer=""),
                             "gt": gt}], False)[0]
        print(f"  {gi} score={res['score']} detail={res.get('detail')}")
        if res.get("score") is None:
            print(f"  {gi}: not appendable ({res.get('error') or 'no result'})")
            continue
        tu = row.get("token_usage") or {}
        out_rows.append({
            "model": model,
            "global_index": gi,
            "dataset": "BFCLv4",
            "score": res["score"],
            "metric": "bfcl_ast",
            "cost": cost_for(model, tu),
            "question": row.get("question", ""),
            "token_usage": {
                "input_tokens": tu.get("input_tokens"),
                "output_tokens": tu.get("output_tokens"),
                "total_tokens": tu.get("total_tokens"),
            },
            "_cluster": clusters.get(gi),
            "_detail": res.get("detail"),
        })

    print(f"\nappended-ready rows: {len(out_rows)}")
    if errors:
        print(f"rows that failed to grade ({len(errors)}):")
        for m, gi, err in errors:
            print(f"  {m} {gi}: {err}")
    per = collections.Counter((r["dataset"], r["model"]) for r in out_rows)
    for k in sorted(per):
        print(f"  {k} -> {per[k]}")

    if args.dry_run:
        Path("D:/router/.context/label-runs/ticket90-preview.json").write_text(
            json.dumps(out_rows, indent=1), encoding="utf-8")
        return 0

    with open(CORPUS, "a", encoding="utf-8") as f:
        for r in out_rows:
            f.write(json.dumps({k: v for k, v in r.items()
                                if not k.startswith("_")},
                               ensure_ascii=False) + "\n")
    print(f"appended {len(out_rows)} rows to {CORPUS}")

    # Cluster placement report (no embedding: assignment is a lookup).
    placed = collections.Counter()
    unplaced = []
    for r in out_rows:
        if r["dataset"] == "AIDER":
            c = r["_cluster"]
            (placed.update([c]) if c is not None else
             unplaced.append((r["model"], r["global_index"])))
    print(f"\nAIDER prompts placed in clusters: {sum(placed.values())}")
    for c in sorted(placed):
        print(f"  c{c + 1}: {placed[c]} new cells")
    if unplaced:
        print(f"AIDER cells on unclustered prompts ({len(unplaced)}):")
        for m, gi in sorted(unplaced):
            print(f"  {m} {gi}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
