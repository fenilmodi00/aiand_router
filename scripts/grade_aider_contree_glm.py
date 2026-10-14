"""ConTree rust/js grading for the GLM AIDER fair-regeneration cells.

The on-disk AIDER prompts split by language: go and python grade locally in
WSL (go test / pytest exist), but rust and javascript need cargo and jest, which
are absent fleet-wide. Locally those cells only ever produce rc=127 zeroes, so
they are graded here in a ConTree sandbox carrying the real toolchains, using
the arena's own metrics.aider_accuracy — the same scorer, not a reimplementation.

This reads the fair-regeneration caches
(D:/router-arena/retry-out/aiderfair-<model>.jsonl, written by
.context/label-runs/aider_glm_regen.py) rather than cached_results/, because
that is where the GLM fair-budget generations live.

Run from D:/router:
    python scripts/grade_aider_contree_glm.py --report
    python scripts/grade_aider_contree_glm.py --run            # grades, writes jsonl
    python scripts/grade_aider_contree_glm.py --run --append   # also appends to corpus
"""
from __future__ import annotations

import argparse
import collections
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(r"D:/router")
ARENA = REPO.parent / "router-arena"
HERE = REPO / ".context" / "label-runs"
CORPUS = HERE / "v082-expanded-labels.jsonl"
GT = ARENA / "dataset" / "aider_polyglot.jsonl"
RETRY_OUT = ARENA / "retry-out"
OUT = HERE / "aider-glm-regen-rustjs.jsonl"

SESSION = "aider-grade"
IMAGE_TAG = "aider-grade:rustjs-v1"
SANDBOX_STAGE = Path(os.environ.get("TEMP", "/tmp")) / "aider-grade-stage-glm"

MODELS = ["zai-org/glm-5.3", "zai-org/glm-5.3-flash"]
PRICE = {
    "zai-org/glm-5.3": (0.00100, 0.00400),
    "zai-org/glm-5.3-flash": (0.00015, 0.00050),
}


def sh(cmd: str) -> str:
    out = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=1800)
    if out.returncode != 0:
        raise RuntimeError(f"{cmd}\n{out.stderr[-2000:]}")
    return out.stdout


def load_gt() -> dict[str, dict]:
    rows = []
    with open(GT, encoding="utf-8") as f:
        for l in f:
            if 'AIDER_' in l[:150]:
                try:
                    rows.append(json.loads(l))
                except Exception:
                    pass
    return {r["Global Index"]: r for r in rows}


def load_corpus_keys() -> set[tuple[str, str]]:
    if not CORPUS.exists():
        return set()
    out = set()
    with open(CORPUS, encoding="utf-8") as f:
        for l in f:
            if l.strip():
                r = json.loads(l)
                out.add((r["model"], r["global_index"]))
    return out


def select() -> tuple[list[dict], collections.Counter]:
    gt = load_gt()
    have = load_corpus_keys()
    todo, audit = [], collections.Counter()
    for model in MODELS:
        p = RETRY_OUT / f"aiderfair-{model.replace('/', '__')}.jsonl"
        if not p.exists():
            print(f"no generation cache for {model}")
            continue
        seen = {}
        for l in p.read_text(encoding="utf-8", errors="replace").splitlines():
            if not l.strip():
                continue
            try:
                r = json.loads(l)
            except Exception:
                continue
            ans = str(r.get("generated_answer") or "").strip()
            if not ans:
                continue
            prev = seen.get(r["global_index"])
            if prev is None or len(ans) > len(str(prev.get("generated_answer") or "")):
                seen[r["global_index"]] = r
        for gi, r in sorted(seen.items()):
            g = gt.get(gi)
            if not g or g.get("language") not in ("rust", "javascript"):
                continue
            audit[(g["language"], gi in {k[1] for k in have},
                   (model, gi) in have)] += 1
            todo.append({"model": model, "global_index": gi, "language": g["language"],
                         "answer": r["generated_answer"], "gt": g,
                         "token_usage": r.get("token_usage") or {}})
    return todo, audit


def cost_for(model: str, tu: dict) -> float:
    pi, po = PRICE[model]
    ti = float((tu or {}).get("input_tokens") or 0)
    to = float((tu or {}).get("output_tokens") or 0)
    return round(ti * pi / 1000.0 + to * po / 1000.0, 8)


def corpus_row(t: dict, score) -> dict:
    tu = t.get("token_usage") or {}
    return {
        "model": t["model"],
        "global_index": t["global_index"],
        "dataset": "AIDER",
        "score": score,
        "metric": "aider_accuracy",
        "cost": cost_for(t["model"], tu),
        "question": (t.get("gt") or {}).get("Question", ""),
        "token_usage": tu,
    }


# ------------------------------------------------------------------- sandbox
SANDBOX_BOOTSTRAP = """
apt-get update -qq >/dev/null 2>&1 || true
apt-get install -y -qq curl python3 python3-pip python3-venv ca-certificates \
    nodejs npm rustc cargo >/dev/null 2>&1 || true
"""

GRADER = r'''
import json, sys
sys.path.insert(0, "/work/llm_evaluation")
from metrics import aider_accuracy
items = json.loads(sys.stdin.read())
for it in items:
    try:
        score, raw = aider_accuracy(it["answer"], it["gt"])
        d = (raw or [{}])[0]
        out = {"score": float(score),
               "returncode": d.get("returncode"),
               "reason": ((d.get("output_tail") or "")[-160:]).strip().replace("\n", " ")}
    except Exception as e:
        out = {"score": None, "error": f"{type(e).__name__}: {e}"}
    print(json.dumps({"key": it["key"], **out}), flush=True)
'''


def run_in_sandbox(todo: list[dict]) -> list[dict]:
    """Grade via contree if available, else report the block honestly."""
    if shutil.which("contree") is None:
        raise SystemExit(
            "contree CLI not on PATH — rust/js cells cannot be graded on this "
            "host. Expected: those cells stay ungraded rather than being "
            "recorded as rc=127 zeroes.")
    SANDBOX_STAGE.mkdir(parents=True, exist_ok=True)
    # stage the arena's metrics package
    src = ARENA / "llm_evaluation"
    dst = SANDBOX_STAGE / "llm_evaluation"
    if dst.exists():
        shutil.rmtree(dst)
    shutil.copytree(src, dst, ignore=shutil.ignore_patterns("__pycache__"))
    runner = SANDBOX_STAGE / "grade_glm_rustjs.py"
    runner.write_text(GRADER, encoding="utf-8")
    sh(f"contree -S {SESSION} use {IMAGE_TAG}")
    sh(f"contree -S {SESSION} mkdir -p /work")
    sh(f"contree -S {SESSION} upload {dst} /work/llm_evaluation")
    sh(f"contree -S {SESSION} upload {runner} /work/grade_glm_rustjs.py")
    payload = [{"key": t["global_index"], "answer": t["answer"], "gt": t["gt"]}
               for t in todo]
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False,
                                     encoding="utf-8") as fh:
        json.dump(payload, fh)
        p = fh.name
    outp = str(Path(p).with_suffix(".out.json"))
    sh(f"contree -S {SESSION} run -- python3 /work/grade_glm_rustjs.py < {p} > {outp}")
    return [json.loads(l) for l in Path(outp).read_text(encoding="utf-8").splitlines() if l.strip()]


def cmd_report() -> int:
    todo, audit = select()
    print(f"GLM fair-regen rust/js cells with a generation: {len(todo)}")
    for k in sorted(audit):
        print(f"  lang={k[0]:11s} in_dataset_corpus={k[1]!s:5s} in_corpus={k[2]!s:5s} -> {audit[k]}")
    print("\ncells:")
    for t in todo:
        print(f"  {t['language']:11s} {t['global_index']:9s} {t['model']}")
    return 0


def cmd_run(append: bool) -> int:
    todo, _ = select()
    if not todo:
        print("no rust/js cells with a generation")
        return 0
    results = run_in_sandbox(todo)
    by = {r["key"]: r for r in results}
    rows, failed = [], []
    for t in todo:
        r = by.get(t["global_index"])
        if r is None or r.get("score") is None:
            failed.append(t)
            continue
        row = corpus_row(t, r["score"])
        row["_language"] = t["language"]
        row["_returncode"] = r.get("returncode")
        rows.append(row)
        print(f"  {t['language']:11s} {t['global_index']:9s} {t['model']:32s} "
              f"score={r['score']} rc={r.get('returncode')}")
    if failed:
        print(f"\n{len(failed)} cell(s) failed to grade (recorded as ungraded, not zero):")
        for t in failed[:20]:
            print(f"  {t['language']} {t['global_index']} {t['model']}")
    OUT.write_text("\n".join(json.dumps(r, ensure_ascii=False) for r in rows) + "\n",
                   encoding="utf-8")
    print(f"\nwrote {len(rows)} graded rows -> {OUT}")
    if not append:
        return 0
    have = load_corpus_keys()
    new = [r for r in rows if (r["model"], r["global_index"]) not in have]
    print(f"appending {len(new)} new rows ({len(rows) - len(new)} already in corpus)")
    with CORPUS.open("a", encoding="utf-8") as f:
        for r in new:
            f.write(json.dumps({k: v for k, v in r.items() if not k.startswith("_")},
                               ensure_ascii=False) + "\n")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--report", action="store_true")
    g.add_argument("--run", action="store_true")
    ap.add_argument("--append", action="store_true")
    a = ap.parse_args()
    return cmd_report() if a.report else cmd_run(a.append)


if __name__ == "__main__":
    sys.exit(main())
