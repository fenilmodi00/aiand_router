"""Grade the remaining AIDER rust/javascript cells in a ConTree sandbox.

Why this exists
---------------
`cargo` and `jest` are absent from the dev box (and WSL), so 228 AIDER cells can
only ever return rc=127 zeroes. This driver grades the cells that DO have a
generation available, inside a ConTree sandbox that actually carries the cargo
and jest toolchains, using the arena's own scorer (`metrics.aider_accuracy`) —
not a reimplementation.

Cell selection (identical to `grade_aider_remainder.py`'s definition)
-------------------------------------------------------------------
A cell is gradeable iff, across the model caches:
  * `generated_answer` is non-empty,
  * `evaluation_result` carries no score,
  * and (model, global_index) is NOT already in the label corpus.
Where the same (model, gi) appears in both the main cache and the Contree aux
cache, the main cache wins (same tiebreak `grade_aider_remainder.py` uses).

Result: there are ZERO such rust/js cells. Every rust/js cell that has a
generation is already in the corpus as score 0.0, so nothing can be appended
without breaking append-only. The toolchain block only ever affected cells that
have no generation at all (206 of the 632 rust/js cells: 89 rust + 117 js have
no generation and no corpus row). See `--report` for the full audit.

The 11 cells that DO have a generation and no cache score, but which the corpus
already records as 0.0, are still graded with the real scorer to confirm those
0.0 rows are earned rather than toolchain artefacts. Run with `--run`: it grades
them in the sandbox and reports the outcome. It does NOT append (nothing new to
append), so the corpus stays byte-identical.

Status (this run, image `aider-grade:rustjs-v1`): all 11 cells were graded with
the real cargo/jest toolchains and all 11 failed on the merits — every answer is
truncated mid-code-block (single unclosed "```" fence), which `_last_code_block`
passes through whole, so rust fails to compile (rc=101) and jest fails to parse
(rc=1). No cell passed. The corpus's existing 0.0 rows are therefore correct.

Sandbox
-------
    contree -S aider-grade use ubuntu:jammy
    contree -S aider-grade cd /root
    # one import (cargo + node + python from the Debian rust:1-slim base)
    contree images import rust:1-slim
    contree -S aider-grade run -- apt-get update -qq
    contree -S aider-grade run -- apt-get install -y -qq curl python3 python3-pip \
        python3-venv ca-certificates nodejs npm
    contree -S aider-grade run -- python3 -m venv /opt/venv && /opt/venv/bin/pip install \
        jieba fuzzywuzzy rouge regex latex2sympy2 sympy
    # jest/babel pinned: jest@29 needs @babel/core@^7 (Babel 8 breaks loadPartialConfig)
    contree -S aider-grade run -s -- 'mkdir -p /opt/jsdeps && cd /opt/jsdeps && \
        npm init -y && npm install --no-audit --no-fund --legacy-peer-deps \
        jest@29 babel-jest@29 @babel/core@^7.25 @babel/preset-env@^7.25 \
        @exercism/babel-preset-javascript core-js'
    # cargo: replace the rustup dispatcher with the real toolchain binary,
    # otherwise the child env (no RUSTUP_HOME) cannot pick a toolchain.
    contree -S aider-grade run -s -- 'TC=/usr/local/rustup/toolchains/1.93.1-x86_64-unknown-linux-gnu/bin; \
        ln -sf $TC/cargo /usr/local/cargo/bin/cargo; ln -sf $TC/rustc /usr/local/cargo/bin/rustc'
    # jest resolves node_modules by walking up from the scorer's tmpdir
    contree -S aider-grade run -- ln -sfn /opt/jsdeps/node_modules /node_modules
    contree -S aider-grade tag aider-grade:rustjs-v1

Rerun
-----
    python scripts/grade_aider_contree.py --report
    python scripts/grade_aider_contree.py --run [--append]
"""
from __future__ import annotations

import argparse
import collections
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path("D:/router")
ARENA = REPO.parent / "router-arena"
CR = ARENA / "cached_results"
CORPUS = REPO / ".context" / "label-runs" / "v082-expanded-labels.jsonl"
GT = ARENA / "dataset" / "aider_polyglot.jsonl"

SESSION = "aider-grade"
IMAGE_TAG = "aider-grade:rustjs-v1"

# canon_c5()'s price table, $/1M tokens -> $/1k. Copied verbatim from
# scripts/grade_aider_remainder.py so appended rows price identically.
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
AUX_CACHES = {"qwen__qwen3.8-27b.contree-aider.jsonl": "qwen/qwen3.8-27b"}

# Staged files uploaded to the sandbox at /work. Kept outside both repos so a
# run leaves no untracked files behind.
SANDBOX_STAGE = Path(os.environ.get("TEMP", "/tmp")) / "aider-grade-stage"
SANDBOX_GRADER = "llm_evaluation"
SANDBOX_RUNNER = "grade_rust_js.py"


# ----------------------------------------------------------------------------
# corpus / ground truth
# ----------------------------------------------------------------------------
def load_corpus() -> list[dict]:
    if not CORPUS.exists():
        raise SystemExit(f"corpus missing: {CORPUS}")
    return [json.loads(line) for line in CORPUS.read_text(encoding="utf-8").splitlines() if line.strip()]


def load_gt() -> dict[str, dict]:
    return {r["Global Index"]: r for r in map(json.loads, open(GT, encoding="utf-8"))}


def cost_for(model: str, tu: dict | None) -> float:
    """Verbatim from grade_aider_remainder.py."""
    ti = float((tu or {}).get("input_tokens") or 0)
    to = float((tu or {}).get("output_tokens") or 0)
    pi, po = PRICE[model]
    return round(ti * pi / 1000.0 + to * po / 1000.0, 8)


def load_cache(path: Path) -> dict[str, dict]:
    out = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            r = json.loads(line)
            out[r["global_index"]] = r
    return out


# ----------------------------------------------------------------------------
# cell selection
# ----------------------------------------------------------------------------
def caches() -> list[tuple[Path, str]]:
    """One authoritative cache per model (the flat one; the org-dir copies are
    byte-identical duplicates), plus the Contree aux cache."""
    out = []
    seen: set[str] = set()
    for model in PRICE:
        p = CR / f"{model.replace('/', '__')}.jsonl"
        if p.exists() and str(p) not in seen:
            seen.add(str(p))
            out.append((p, model))
    for aux, model in sorted(AUX_CACHES.items()):
        p = CR / aux
        if p.exists() and str(p) not in seen:
            seen.add(str(p))
            out.append((p, model))
    return out


def cells(langs: set[str] = {"rust", "javascript"}) -> tuple[list[dict], collections.Counter]:
    """Gradeable + audit counters for the chosen languages."""
    gt = load_gt()
    corpus = {(r["global_index"], r["model"]) for r in load_corpus()}
    wanted = {gi for gi, r in gt.items() if r["language"] in langs}

    best: dict[tuple[str, str], dict] = {}
    for path, model in sorted(caches()):
        aux = path.name.endswith(".contree-aider.jsonl")
        for gi, row in load_cache(path).items():
            if gi not in wanted:
                continue
            k = (model, gi)
            prev = best.get(k)
            cand = {"cache": path.name, "aux": aux, "row": row}
            if prev is None:
                best[k] = cand
            elif prev["aux"] and not aux:
                best[k] = cand  # main cache beats the Contree aux
            elif not aux and prev["aux"] is False and prev["cache"] != cand["cache"]:
                # duplicate flat/dir copies carry identical answers; keep first
                continue

    todo: list[dict] = []
    audit: collections.Counter = collections.Counter()
    for (model, gi), entry in sorted(best.items()):
        row = entry["row"]
        lang = gt[gi]["language"]
        ans = bool((row.get("generated_answer") or "").strip())
        er = row.get("evaluation_result")
        scored = er is not None and (not isinstance(er, dict) or bool(er))
        inc = (gi, model) in corpus
        audit[(lang, ans, scored, inc)] += 1
        if ans and not scored and not inc:
            todo.append(_cell(model, gi, lang, entry["cache"], row, gt))
        elif ans and not scored and inc:
            # has a generation, never scored in the cache, but the corpus
            # already carries a row (recorded 0.0). Re-grade to confirm.
            todo.append(_cell(model, gi, lang, entry["cache"], row, gt,
                              regrade=True))
    return todo, audit


def _cell(model, gi, lang, cache, row, gt, regrade: bool = False) -> dict:
    return {
        "model": model, "global_index": gi, "language": lang,
        "source": cache, "regrade": regrade,
        "question": row.get("question"),
        "token_usage": row.get("token_usage"),
        "generated_answer": row["generated_answer"],
        "gt": {"test_command": gt[gi]["test_command"],
               "test_files": gt[gi]["test_files"],
               "target_file": gt[gi]["target_file"]},
    }


# ----------------------------------------------------------------------------
# corpus writing
# ----------------------------------------------------------------------------
def corpus_row(cell: dict, score: float) -> dict:
    return {
        "model": cell["model"],
        "global_index": cell["global_index"],
        "dataset": "AIDER",
        "score": float(score),
        "metric": "aider_accuracy",
        "cost": cost_for(cell["model"], cell.get("token_usage")),
        "question": cell["question"],
        "token_usage": cell.get("token_usage"),
    }


def append_rows(rows: list[dict]) -> None:
    with open(CORPUS, "a", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r, ensure_ascii=False) + "\n")


def verify_append_only(backup: Path) -> tuple[int, int]:
    prefix = backup.read_bytes()
    total = 0
    with open(CORPUS, "rb") as fh:
        head = fh.read(len(prefix))
        if head != prefix:
            raise SystemExit("PREFIX MISMATCH: corpus was modified in place")
        for i, line in enumerate(fh, start=0):
            if line.strip():
                total += 1
    return len(prefix.splitlines()), total


# ----------------------------------------------------------------------------
# sandbox runner
# ----------------------------------------------------------------------------
SANDBOX_RUNNER_SRC = r'''
"""Run inside the ConTree sandbox; calls the real arena scorer per cell."""
import json, os, sys, time

# Pin the toolchain even if a caller forgets the bootstrap symlinks: the child
# env built by aider_accuracy is dict(os.environ), so it inherits these.
os.environ.setdefault("RUSTUP_HOME", "/usr/local/rustup")
os.environ.setdefault("CARGO_HOME", "/usr/local/cargo")
os.environ.setdefault("RUSTUP_TOOLCHAIN", "1.93.1-x86_64-unknown-linux-gnu")

sys.path.insert(0, "/work/llm_evaluation")
from metrics import aider_accuracy

items = json.load(open(sys.argv[1], encoding="utf-8"))
with open(sys.argv[2], "w", encoding="utf-8") as fh:
    for it in items:
        rec = {"model": it["model"], "global_index": it["global_index"],
               "language": it["language"], "score": 0.0, "returncode": None,
               "timeout": False, "failure_marker": False, "reason": "",
               "elapsed_s": None}
        t0 = time.time()
        try:
            gt_row = {"test_command": it["gt"]["test_command"],
                      "test_files": it["gt"]["test_files"],
                      "target_file": it["gt"]["target_file"]}
            _, raw = aider_accuracy([it["generated_answer"]], [gt_row])
            d = raw[0]
            rec.update(score=float(d["score"]), returncode=d.get("returncode"),
                        timeout=bool(d.get("timeout")),
                        failure_marker=bool(d.get("failure_marker")))
            if rec["score"] == 1.0:
                rec["reason"] = "passed"
            elif rec["timeout"]:
                rec["reason"] = "timeout (60s)"
            elif rec["returncode"] == 127:
                rec["reason"] = "command not found (rc=127)"
            elif rec["returncode"] == 101:
                rec["reason"] = "cargo panic / compile error (rc=101)"
            elif rec["returncode"] == 0:
                rec["reason"] = "test runner printed FAILED marker"
            else:
                rec["reason"] = f"nonzero exit {rec['returncode']}"
            rec["output_tail"] = (d.get("output_tail") or "")[-1200:]
        except Exception as exc:
            rec["reason"] = f"grader error: {type(exc).__name__}: {exc}"[:400]
            rec["output_tail"] = str(exc)[:1200]
        rec["elapsed_s"] = round(time.time() - t0, 2)
        fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
        fh.flush()
'''

# The scorer materialises test_files into a TemporaryDirectory under /tmp; jest
# resolves node_modules by walking up from there, so one link at / covers every
# JS exercise. cargo must be the real toolchain binary: the child env built by
# aider_accuracy (dict(os.environ)) loses RUSTUP_HOME, and the rustup dispatcher
# then refuses to pick a toolchain.
SANDBOX_BOOTSTRAP = r'''
set -e
if [ ! -d /opt/jsdeps/node_modules ]; then
  mkdir -p /opt/jsdeps && cd /opt/jsdeps && npm init -y >/dev/null 2>&1 && \
    npm install --no-audit --no-fund --legacy-peer-deps \
      jest@29 babel-jest@29 "@babel/core@^7.25" "@babel/preset-env@^7.25" \
      @exercism/babel-preset-javascript core-js >/dev/null 2>&1
fi
ln -sfn /opt/jsdeps/node_modules /node_modules
TC=/usr/local/rustup/toolchains/1.93.1-x86_64-unknown-linux-gnu/bin
ln -sf $TC/cargo /usr/local/cargo/bin/cargo
ln -sf $TC/rustc /usr/local/cargo/bin/rustc
'''


def sh(cmd: str, timeout: int = 3600) -> subprocess.CompletedProcess:
    proc = subprocess.run(["contree", "-S", SESSION, "run", "-s", "--", cmd],
                          capture_output=True, text=True, timeout=timeout)
    if proc.returncode != 0:
        print(proc.stdout[-4000:])
        print(proc.stderr[-4000:], file=sys.stderr)
        raise SystemExit(f"contree failed ({proc.returncode}): {cmd[:120]}")
    return proc


def run_in_sandbox(items: list[dict], out_remote: str = "/work/results.json") -> list[dict]:
    stage = SANDBOX_STAGE
    (stage / SANDBOX_GRADER).mkdir(parents=True, exist_ok=True)
    # The arena's scorer plus the sibling modules it imports.
    for mod in ("metrics.py", "metric_utils.py", "enhanced_extractor.py", "livecodebench_util.py"):
        shutil.copy(ARENA / "llm_evaluation" / mod, stage / SANDBOX_GRADER / mod)
    runner = stage / SANDBOX_RUNNER
    runner.write_text(SANDBOX_RUNNER_SRC, encoding="utf-8")
    todo = stage / "todo.json"
    todo.write_text(json.dumps(items, ensure_ascii=False), encoding="utf-8")

    sh("mkdir -p /work && /opt/venv/bin/python -c 'import jieba,rouge,regex,fuzzywuzzy,sympy;print(\"deps ok\")'")
    attachments = [
        "--file", f"{stage / SANDBOX_GRADER}:/work/llm_evaluation".replace("\\", "/"),
        "--file", f"{todo}:/work/todo.json".replace("\\", "/"),
        "--file", f"{runner}:/work/{SANDBOX_RUNNER}".replace("\\", "/"),
    ]
    proc = subprocess.run(
        ["contree", "-S", SESSION, "run", "-t", "3600"] + attachments +
        ["--", "/opt/venv/bin/python", f"/work/{SANDBOX_RUNNER}", "/work/todo.json", out_remote],
        capture_output=True, text=True, timeout=3600)
    if proc.returncode != 0:
        print(proc.stdout[-4000:]); print(proc.stderr[-4000:], file=sys.stderr)
        raise SystemExit(f"sandbox grading failed ({proc.returncode})")

    local = stage / "results.json"
    subprocess.run(["contree", "-S", SESSION, "cp", out_remote, str(local).replace("\\", "/")],
                   capture_output=True, text=True, check=True, timeout=600)
    return [json.loads(l) for l in local.read_text(encoding="utf-8").splitlines() if l.strip()]


# ----------------------------------------------------------------------------
# reporting
# ----------------------------------------------------------------------------
def cmd_report() -> int:
    todo, audit = cells()
    corpus = load_corpus()
    print(f"corpus rows now: {len(corpus)}")
    gt = load_gt()
    rj = [r for r in corpus if r["global_index"] in gt and gt[r["global_index"]]["language"] in ("rust", "javascript")]
    print(f"  rust/js corpus rows: {sum(1 for r in rj if r['global_index'] in gt and gt[r['global_index']]['language'] == 'rust')} rust / "
          f"{sum(1 for r in rj if gt[r['global_index']]['language'] == 'javascript')} javascript")
    all8 = sum(1 for gi in gt if gi.startswith("AIDER_") and
               sum(1 for r in corpus if r["global_index"] == gi) == 8)
    print(f"  AIDER prompts: {sum(1 for r in corpus if r['global_index'].startswith('AIDER_'))}")
    print(f"  AIDER prompts with all 8 models: {all8}")
    print()
    print("cache state for rust/js cells (lang, has_answer, cache_scored, in_corpus):")
    for k in sorted(audit):
        print(f"  {k[0]:11s} answer={str(k[1]):5s} scored={str(k[2]):5s} in_corpus={str(k[3]):5s} -> {audit[k]}")
    print()
    print(f"cells to (re)grade with the real scorer: {len(todo)}")
    for t in todo:
        tag = "REGRADE (already in corpus as 0.0)" if t.get("regrade") else "NEW"
        print(f"  {t['language']:11s} {t['global_index']:9s} {t['model']:32s} {tag}")
    return 0


def cmd_run(append: bool) -> int:
    todo, audit = cells()
    new = [t for t in todo if not t.get("regrade")]
    if not new:
        print(f"nothing new to grade — {len(todo)} cell(s) re-verified against the corpus")
        if not todo:
            return 0
    backup = CORPUS.parent / (CORPUS.name + ".bak-prejsrust")
    if not backup.exists():
        backup.write_bytes(CORPUS.read_bytes())
        print(f"backup: {backup}")

    sh("mkdir -p /work && " + SANDBOX_BOOTSTRAP.strip())
    results = run_in_sandbox(todo)
    by_key = {(r["global_index"], r["model"]): r for r in results}
    print(f"\ngraded {len(results)} cells [{SESSION} / {IMAGE_TAG}]")
    for t in todo:
        r = by_key.get((t["global_index"], t["model"]))
        if r is None:
            print(f"  MISSING RESULT {t['global_index']} {t['model']}")
            continue
        print(f"  {t['language']:11s} {t['global_index']:9s} {t['model']:32s} "
              f"score={r['score']} rc={r['returncode']} :: {r['reason']}")

    if not new:
        print("\nall graded cells were already in the corpus — nothing appended, "
              "corpus left byte-identical")
        return 0

    rows = [corpus_row(t, by_key[(t["global_index"], t["model"])]["score"]) for t in new]
    dups = [k for k in ((r["global_index"], r["model"]) for r in rows)
            if k in {(c["global_index"], c["model"]) for c in load_corpus()}]
    if dups:
        raise SystemExit(f"refusing to append duplicates: {dups}")
    if not append:
        print(f"\n(dry run — {len(rows)} rows would be appended; pass --append to write)")
        return 0

    before = len(load_corpus())
    append_rows(rows)
    after = len(load_corpus())
    prefix, total = verify_append_only(backup)
    seen = set()
    for r in load_corpus():
        k = (r["global_index"], r["model"])
        if k in seen:
            raise SystemExit(f"duplicate left in the corpus: {k}")
        seen.add(k)
    print(f"\nappended {len(rows)} rows; corpus {before} -> {after} "
          f"(prefix byte-identical, {total} lines)")
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
