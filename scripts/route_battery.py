#!/usr/bin/env python3
"""Post-deploy route battery: probe /v1/route with synthetic prompts and
check the chosen model lands in the expected quality/cost tier.

This is the committed, dependency-free counterpart to the replay harness's
per-register rubric: it fires authored, generic probes at a running router's
read-only /v1/route dry-run endpoint and tallies, per conversation register,
how often the routing decision lands on a model in the expected tier. Use it
after flipping ROUTER_CLUSTER_VERSION (or policy gates) to confirm the
serving stack actually reproduces the intended per-register winners before
calling a rollout done.

Tier rationale tracks the v0.89 hybrid policy (trivial gate -> glm-5.3-flash,
difficulty gate -> glm-5.3); see the gates section of docs/CONFIGURATION.md
for the current tier definitions and gate wiring.

Usage:
    # Local docker compose stack (default base URL, seeded local key):
    python3 scripts/route_battery.py --api-key rk_...

    # Staging, comparing a candidate cluster version against the default:
    python3 scripts/route_battery.py \
        --base-url https://staging.example.internal \
        --api-key rk_... --version-header v0.89

    # Machine-readable output for CI gating:
    python3 scripts/route_battery.py --api-key rk_... --json --min-overall 0.75

Exit codes: 0 = overall pass rate >= --min-overall; 1 = below threshold
(either overall or any per-register floor); 2 = could not reach the router
(connection failure). Configuration: --base-url / ROUTER_BATTERY_BASE_URL,
--api-key / ROUTER_BATTERY_API_KEY.

All probes below are authored synthetic prompts; they contain no customer
data and must stay that way. The response contract is {model, provider,
reason}; the internal RoutingMetadata.Gate ("trivial"/"difficulty") is NOT
surfaced by /v1/route, so gate attribution here is inferred from the chosen
model, not read from the payload.
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

REGISTER_TRIVIAL_NL = "trivial_nl"
REGISTER_CONVERSATIONAL = "conversational"
REGISTER_EASY_CODE = "easy_code"
REGISTER_KNOWLEDGE_QA = "knowledge_qa"
REGISTER_HARD_CODE = "hard_code"
REGISTER_AGENTIC_TOOL = "agentic_tool"

# Registers whose probes should land on the CHEAP tier.
CHEAP_REGISTERS = (REGISTER_TRIVIAL_NL, REGISTER_CONVERSATIONAL)
# Registers whose probes should land on the MID tier.
MID_REGISTERS = (REGISTER_EASY_CODE, REGISTER_KNOWLEDGE_QA)
# Registers whose probes should land on the HIGH tier.
HARD_REGISTERS = (REGISTER_HARD_CODE, REGISTER_AGENTIC_TOOL)

TIER_CHEAP = "cheap"
TIER_MID = "mid"
TIER_HIGH = "high"
TIERS = (TIER_CHEAP, TIER_MID, TIER_HIGH)

# Catalog model ids per tier, as returned in the decision's "model" field.
TIER_MODELS = {
    TIER_CHEAP: {"z-ai/glm-5.3-flash", "deepseek/deepseek-v4.1-flash"},
    TIER_MID: {
        "deepseek/deepseek-v4.1-flash",
        "qwen/qwen3.8-27b",
        "motif/motif-3",
    },
    TIER_HIGH: {
        "z-ai/glm-5.3",
        "moonshotai/kimi-k3",
        "deepseek/deepseek-v4-pro",
    },
}

# Each probe: (register, prompt). Authored synthetic text only.
PROBES = [
    # --- trivial_nl: one-line everyday asks ---------------------------------
    (REGISTER_TRIVIAL_NL, "Summarize this in five words: a cat slept all afternoon on the windowsill."),
    (REGISTER_TRIVIAL_NL, "What is a polite way to decline a meeting invite?"),
    (REGISTER_TRIVIAL_NL, "Rewrite: 'send me the file when you can' to sound friendlier."),
    (REGISTER_TRIVIAL_NL, "Give me a one-sentence thank-you note for a neighbor who watered my plants."),
    (REGISTER_TRIVIAL_NL, "How many days are in a leap year?"),
    (REGISTER_TRIVIAL_NL, "Suggest a short subject line for an email rescheduling lunch to Friday."),
    (REGISTER_TRIVIAL_NL, "Convert 72 Fahrenheit to Celsius."),
    (REGISTER_TRIVIAL_NL, "Fix the grammar: 'me and him goes to the store yesterday'."),
    (REGISTER_TRIVIAL_NL, "What should I type when I want to acknowledge a message quickly?"),

    # --- conversational: chatty multi-turn style ---------------------------
    (REGISTER_CONVERSATIONAL, "I've been thinking about learning to bake bread on weekends. Is sourdough a good starting point, or should I begin with something simpler like focaccia?"),
    (REGISTER_CONVERSATIONAL, "My friend keeps rescheduling our plans at the last minute. I don't want to make it a big deal, but it's starting to feel one-sided. How would you bring it up?"),
    (REGISTER_CONVERSATIONAL, "We're planning a small weekend trip with two kids under eight. Any ideas for activities that won't exhaust everyone by day two?"),
    (REGISTER_CONVERSATIONAL, "I started running last month and can now jog twenty minutes without stopping. What's a realistic next milestone to aim for?"),
    (REGISTER_CONVERSATIONAL, "Our team meeting always runs over because one person talks a lot. As the organizer, what's a tactful way to keep things moving?"),
    (REGISTER_CONVERSATIONAL, "I keep buying vegetables with good intentions and throwing them out a week later. What habits actually work for reducing food waste at home?"),
    (REGISTER_CONVERSATIONAL, "A coworker gave me a genuinely helpful review on my project. Beyond saying thanks, is there a good way to return the favor professionally?"),
    (REGISTER_CONVERSATIONAL, "I'm torn between keeping my old car another two years or replacing it now. What factors matter most for that kind of decision?"),
    (REGISTER_CONVERSATIONAL, "Moving into a smaller apartment soon and I have too much stuff. What's a sensible order of operations for downsizing without regretting it?"),

    # --- easy_code: straightforward code asks -------------------------------
    (REGISTER_EASY_CODE, "Write a Python function that takes a list of integers and returns the second-largest value, or None if the list has fewer than two distinct values."),
    (REGISTER_EASY_CODE, "In JavaScript, what's the simplest way to deep-copy a plain object, and when is structuredClone better than JSON round-tripping?"),
    (REGISTER_EASY_CODE, "Here's a SQL question: how do I select the five most recent rows per customer from an orders table? Sketch the query with a window function.\n\n```sql\nSELECT * FROM orders;\n```"),
    (REGISTER_EASY_CODE, "Write a bash one-liner that counts the number of lines containing the word ERROR in all .log files in the current directory."),
    (REGISTER_EASY_CODE, "Explain what this snippet prints and why:\n\n```python\nx = [1, 2, 3]\ny = x\ny.append(4)\nprint(x)\n```"),
    (REGISTER_EASY_CODE, "Write a Go function that reverses a string, handling multi-byte runes correctly."),
    (REGISTER_EASY_CODE, "Add input validation to this function so it rejects empty strings and strings longer than 100 characters:\n\n```python\ndef greet(name):\n    return f\"Hello, {name}!\"\n```"),
    (REGISTER_EASY_CODE, "What's the difference between == and === in TypeScript, with a short example where they disagree?"),
    (REGISTER_EASY_CODE, "Write a regular expression that matches a date in YYYY-MM-DD format, and show a tiny test in Python confirming it accepts 2026-01-31 but rejects 2026-13-01."),

    # --- knowledge_qa: factual/explanatory ----------------------------------
    (REGISTER_KNOWLEDGE_QA, "Explain the difference between TCP and UDP, and give one example application where each is the right choice."),
    (REGISTER_KNOWLEDGE_QA, "Why do vaccines require cold chains, and what happens to typical mRNA vaccines if storage temperatures drift too warm?"),
    (REGISTER_KNOWLEDGE_QA, "How does compound interest differ from simple interest, and why does the gap widen dramatically over multi-decade horizons?"),
    (REGISTER_KNOWLEDGE_QA, "In economics, what does it mean for a market to be efficient, and what are the main conditions that fail in practice?"),
    (REGISTER_KNOWLEDGE_QA, "Explain how a hash map achieves average O(1) lookup, what collision resolution strategies exist, and when a hash map degrades toward O(n)."),
    (REGISTER_KNOWLEDGE_QA, "What is the difference between weather and climate, and why do both short-term forecasts and long-term climate projections come from models that are validated differently?"),
    (REGISTER_KNOWLEDGE_QA, "Summarize how public-key cryptography enables both encryption and digital signatures, and why the two operations use the key pairs in opposite directions."),
    (REGISTER_KNOWLEDGE_QA, "Why do seasons occur, and what common misconception about Earth's distance from the sun does the southern-hemisphere summer disprove?"),
    (REGISTER_KNOWLEDGE_QA, "Explain the CAP theorem in distributed systems, including why a network partition forces a choice between consistency and availability."),

    # --- hard_code: long, complex engineering prompts -----------------------
    (REGISTER_HARD_CODE, "I have a Go service whose p99 latency spikes every few minutes. The service fans out to three upstreams, uses a shared http.Client with default transport settings, and holds a global mutex around a metrics map. Draft a diagnostic plan that distinguishes connection-pool exhaustion, lock contention, and GC pauses, then sketch the fix for the most likely culprit.\n\n```go\nvar mu sync.Mutex\nmetrics := map[string]int{}\nfunc record(k string) { mu.Lock(); metrics[k]++; mu.Unlock() }\n```\n\nInclude which runtime metrics and log fields you'd check first and why, and how you'd confirm the fix with a load test rather than by eyeballing dashboards."),
    (REGISTER_HARD_CODE, "Refactor this callback-heavy Node.js module into an async/await version with proper error propagation, cancellation on timeout, and bounded concurrency. Preserve the retry semantics: only idempotent GETs may retry, up to three times with exponential backoff.\n\n```javascript\nfunction fetchAll(urls, done) {\n  let pending = urls.length;\n  const out = [];\n  urls.forEach((u, i) => {\n    http.get(u, (res) => {\n      let body = '';\n      res.on('data', (c) => (body += c));\n      res.on('end', () => { out[i] = body; if (--pending === 0) done(null, out); });\n    }).on('error', (e) => done(e));\n  });\n}\n```\n\nPoint out the failure modes of the original (silent partial results, unbounded parallelism, no timeout) and how each is addressed in your version."),
    (REGISTER_HARD_CODE, "Design a rate limiter for a multi-tenant API gateway that must enforce both a per-tenant token bucket and a global ceiling, survive instance restarts without losing more than a second of state, and stay correct when gateway instances scale horizontally. Compare a Redis-backed sliding window against local token buckets with periodic sync, recommend one, and specify the data layout, the fail-open vs fail-closed decision on Redis unavailability, and the tests you'd write to prove burst behavior matches the spec under clock skew between instances."),
    (REGISTER_HARD_CODE, "This Python data pipeline deadlocks under load and I can't see why. It spawns one thread per source file, each thread writes to a shared sqlite connection guarded by a lock, and a coordinator thread joins them with a queue that has no maxsize. Explain the interaction between the unbounded queue, the GIL, the sqlite lock, and join ordering that produces the hang, then rewrite the module with a worker pool, chunked transactions, and a hard per-file timeout.\n\n```python\nfiles = os.listdir('incoming')\nresults = queue.Queue()\nthreads = [threading.Thread(target=load, args=(f, results)) for f in files]\n[t.start() for t in threads]\nwhile any(t.is_alive() for t in threads):\n    row = results.get()\n    with db_lock:\n        conn.execute('INSERT INTO rows VALUES (?)', (row,))\n```\n\nThe rewrite must also make partial failures resumable: state which file offsets have been committed so a crash mid-batch doesn't duplicate rows."),
    (REGISTER_HARD_CODE, "Our microservice deployment leaks goroutines only when a downstream returns 503: each retry schedules a timer that outlives its request context, and the escalation path re-schedules the same work twice when the leader lease flips mid-flight. Walk through how a context deadline, a time.After inside a select, and a lease handoff interact to produce this, then propose the smallest structural change (per-attempt contexts, a single owner goroutine, or a work queue with idempotent claims) and defend the choice. Include the race-detector and soak-test plan you'd run before trusting the fix, and which runtime/pprof profiles would have surfaced it in staging."),
    (REGISTER_HARD_CODE, "We need to migrate a 400GB Postgres table from a single integer PK to a UUIDv7 PK with zero downtime, on a version where logical replication of the new column is available but NOT rewrite-in-place. Design the full migration: shadow table with dual writes behind a feature flag, backfill in bounded batches keyed by the old PK with a progress ledger, verification via checksums per range, then a cutover window under a write freeze measured in seconds. Specify the rollback story at each phase, the exact index build strategy that avoids long lock waits, and how the application's ORM layer must change so both key shapes coexist during the transition."),
    (REGISTER_HARD_CODE, "Review this authentication middleware for correctness and security, then fix what you find. Requirements: constant-time token comparison, per-key rate limiting that can't be bypassed by concurrent requests, and no token material in logs.\n\n```go\nfunc Auth(next http.Handler) http.Handler {\n    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n        token := r.Header.Get(\"X-Api-Key\")\n        row := db.QueryRow(\"SELECT id FROM keys WHERE token = ?\", token)\n        var id string\n        if err := row.Scan(&id); err != nil {\n            log.Printf(\"auth failed for token \" + token)\n            http.Error(w, \"unauthorized\", 401)\n            return\n        }\n        r.Header.Set(\"X-User-Id\", id)\n        next.ServeHTTP(w, r)\n    })\n}\n```\n\nIdentify the injection-adjacent risk, the timing and logging problems, the missing rate limit, and why mutating request headers for identity is fragile, and produce the corrected version with tests covering each defect."),

    (REGISTER_HARD_CODE, "I need a memory-efficient top-k frequent-elements service over a stream of ~100M events per day on a single 16GB box, where the answer must be exact for the top 100 keys but the key space is effectively unbounded. Evaluate count-min sketch plus a heavy-hitter exact map against space-saving with a bounded heap, derive the memory bounds for each at your chosen error/epsilon, and write the core update and query functions in Python. Then describe how you'd validate the approximation error offline against a ground-truth batch job and what alerting threshold you'd set before the service starts returning stale answers."),

    # --- agentic_tool: tool-use / agentic workflows --------------------------
    (REGISTER_AGENTIC_TOOL, "You have tools: read_file, list_dir, write_file, run_tests. Task: find the failing test in this repository, fix the minimal code needed to make the suite green, and run the tests to confirm. Start by listing the top-level directory. Do not modify test files; fix production code only."),
    (REGISTER_AGENTIC_TOOL, "Using your web_search and calculator tools, produce a comparison table of three cloud storage providers' current per-GB prices, compute the monthly cost for a 2TB workload for each, and cite the source URL you read for every price. If a provider's pricing page is unreachable, mark it unavailable rather than guessing."),
    (REGISTER_AGENTIC_TOOL, "You have a shell tool and a git tool. The branch feature/payment-retry has five commits that need cleanup before review: squash commits 2 and 3 (both fix typos from commit 1), drop commit 4 (it reverts commit 1 then reinstates it), and reword commit 5's message to describe the actual change. Show the exact git commands you'd run interactively, and stop to confirm before rewriting history that has been pushed."),
    (REGISTER_AGENTIC_TOOL, "You have tools: sql_query (read-only), chart_render, and report_write. Task: investigate why reported daily active users dropped 12% last Tuesday. Query the events table for the days around the drop, segment by platform and signup cohort, render one chart showing the clearest segmentation, and write a short report distinguishing a real user decline from a tracking artifact. State explicitly what additional query you would run if the data were inconclusive."),
    (REGISTER_AGENTIC_TOOL, "You have tools: read_file, grep, edit_file, run_tests. Task: a customer-reported crash happens when a request contains 100+ nested tool definitions. Locate where tool schemas are validated and translated, reproduce the failure with a synthetic oversized request, apply the smallest fix that either rejects oversize input cleanly at the boundary or handles arbitrary depth, and add a regression test. Prefer an explicit depth or size limit with a clear error over unbounded recursion."),
    (REGISTER_AGENTIC_TOOL, "You have tools: read_file, edit_file, run_tests, and docker_exec. Task: a scheduled job intermittently writes duplicate rows into the orders export. Reproduce the duplication locally with docker_exec running two overlapping job invocations, read the job's locking and idempotency logic, apply the smallest fix (an explicit uniqueness constraint plus idempotent upsert, or a proper advisory lock), and add a regression test that fails before the fix by launching the two invocations concurrently. Report which approach you chose and why the alternative was rejected."),
    (REGISTER_AGENTIC_TOOL, "You have tools: read_file, grep, plan_submit, edit_file, run_tests. Task: prepare this repository for an async runtime upgrade. Grep for every direct use of the deprecated timer and spawn APIs, group the call sites by module, submit a migration plan listing each module with its risk level before touching code, then migrate only the lowest-risk module and run the full test suite. If any test fails for reasons unrelated to your change, stop and report the failure rather than editing unrelated code."),

    (REGISTER_AGENTIC_TOOL, "You have a python_repl tool with pandas available. Here is a CSV of server response times by hour. Task: identify the top three incident windows (consecutive hours where p99 exceeded 2x the trailing 24h median), compute their durations, and emit a JSON summary the on-call tool can ingest. Show your intermediate dataframe transformations so the analysis is auditable.\n\n```csv\nhour,p99_ms\n2026-01-01T00,180\n2026-01-01T01,195\n```\n\n(Assume the full dataset follows this schema with 90 days of rows.)"),
]


def parse_args(argv):
    p = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument(
        "--base-url",
        default=os.environ.get("ROUTER_BATTERY_BASE_URL", "http://127.0.0.1:8080"),
        help="Router base URL (default: ROUTER_BATTERY_BASE_URL or http://127.0.0.1:8080)",
    )
    p.add_argument(
        "--api-key",
        default=os.environ.get("ROUTER_BATTERY_API_KEY", ""),
        help="Router API key, e.g. rk_... (default: ROUTER_BATTERY_API_KEY)",
    )
    p.add_argument(
        "--version-header",
        default="",
        help="Optional value for the x-aiand-cluster-version header (A/B a candidate cluster version)",
    )
    p.add_argument(
        "--timeout",
        type=float,
        default=30.0,
        help="Per-request timeout in seconds (default: 30)",
    )
    p.add_argument(
        "--min-overall",
        type=float,
        default=0.75,
        help="Exit 0 requires overall tier accuracy >= this fraction (default: 0.75)",
    )
    p.add_argument(
        "--min-cheap", type=float, default=0.7,
        help="Per-register floor for cheap registers (trivial_nl, conversational; default: 0.7)",
    )
    p.add_argument(
        "--min-mid", type=float, default=0.5,
        help="Per-register floor for mid registers (easy_code, knowledge_qa; default: 0.5)",
    )
    p.add_argument(
        "--min-hard", type=float, default=0.5,
        help="Per-register floor for hard registers (hard_code, agentic_tool; default: 0.5)",
    )
    p.add_argument(
        "--json", action="store_true",
        help="Emit machine-readable JSON instead of the human-readable table",
    )
    p.add_argument(
        "--out",
        default="",
        help="Also write the JSON report to this file (combined with --json stdout)",
    )
    return p.parse_args(argv)


def tier_for_model(model):
    for tier, models in TIER_MODELS.items():
        if model in models:
            return tier
    return "unknown"


def expected_tier(register):
    if register in CHEAP_REGISTERS:
        return TIER_CHEAP
    if register in MID_REGISTERS:
        return TIER_MID
    return TIER_HIGH


def floor_for_register(args, register):
    if register in CHEAP_REGISTERS:
        return args.min_cheap
    if register in MID_REGISTERS:
        return args.min_mid
    return args.min_hard


def route_one(base_url, api_key, version_header, timeout, prompt):
    body = json.dumps({
        "model": "auto",
        "max_tokens": 64,
        "messages": [{"role": "user", "content": prompt}],
    }).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        # extractToken accepts Authorization: Bearer, x-api-key, or the
        # dedicated router-key header; Bearer is the most portable.
        "Authorization": "Bearer " + api_key,
    }
    if version_header:
        headers["x-aiand-cluster-version"] = version_header
    req = urllib.request.Request(
        base_url.rstrip("/") + "/v1/route",
        data=body,
        headers=headers,
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    return {
        "model": payload.get("model", ""),
        "provider": payload.get("provider", ""),
        "reason": payload.get("reason", ""),
    }


def run_battery(args):
    results = []
    for register, prompt in PROBES:
        entry = {
            "register": register,
            "expected_tier": expected_tier(register),
            "prompt": prompt,
            "model": "",
            "provider": "",
            "reason": "",
            "tier": "unknown",
            "ok": False,
            "error": "",
        }
        try:
            decision = route_one(
                args.base_url, args.api_key, args.version_header,
                args.timeout, prompt,
            )
            entry.update(decision)
            entry["tier"] = tier_for_model(decision["model"])
            entry["ok"] = entry["tier"] == entry["expected_tier"]
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8", "replace")[:300]
            except Exception:
                pass
            entry["error"] = "HTTP %d: %s" % (e.code, detail)
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            # Connection-level failure: stop with exit code 2.
            raise ConnectionError(str(e)) from e
        results.append(entry)
    return results


def summarize(results):
    by_register = {}
    for r in results:
        by_register.setdefault(r["register"], []).append(r)
    tier_totals = {t: [0, 0] for t in TIERS}
    register_rates = {}
    for register, entries in sorted(by_register.items()):
        passed = sum(1 for e in entries if e["ok"])
        total = len(entries)
        register_rates[register] = (passed, total)
        tier_totals[entries[0]["expected_tier"]][0] += passed
        tier_totals[entries[0]["expected_tier"]][1] += total
    overall_passed = sum(1 for e in results if e["ok"])
    overall_total = len(results)
    return tier_totals, register_rates, (overall_passed, overall_total)


def print_table(results, register_rates, tier_totals, overall):
    registers = []
    for r in results:
        if r["register"] not in registers:
            registers.append(r["register"])
    for register in registers:
        entries = [r for r in results if r["register"] == register]
        print("== %s (expect %s) ==" % (register, entries[0]["expected_tier"]))
        for e in entries:
            status = "PASS" if e["ok"] else "FAIL"
            if e["error"]:
                detail = "  ERROR: %s" % e["error"]
            else:
                detail = "  -> %s (%s)" % (e["model"], e["tier"])
            preview = e["prompt"][:60].replace("\n", " ")
            if len(e["prompt"]) > 60:
                preview += "..."
            print("  [%s] %s%s" % (status, preview, detail))
        passed, total = register_rates[register]
        print("  %d/%d in tier\n" % (passed, total))
    cheap, mid, hard = (tier_totals[t] for t in TIERS)
    print(
        "cheap %d/%d  mid %d/%d  hard %d/%d  overall %d/%d"
        % (cheap[0], cheap[1], mid[0], mid[1], hard[0], hard[1],
           overall[0], overall[1])
    )


def main(argv=None):
    args = parse_args(argv if argv is not None else sys.argv[1:])
    if not args.api_key:
        print("error: --api-key or ROUTER_BATTERY_API_KEY is required", file=sys.stderr)
        return 2
    try:
        results = run_battery(args)
    except ConnectionError as e:
        print("error: could not reach router at %s: %s" % (args.base_url, e), file=sys.stderr)
        return 2

    tier_totals, register_rates, overall = summarize(results)
    report = {
        "tier_totals": {t: {"passed": tier_totals[t][0], "total": tier_totals[t][1]}
                        for t in TIERS},
        "registers": {reg: {"passed": v[0], "total": v[1]}
                      for reg, v in register_rates.items()},
        "overall": {"passed": overall[0], "total": overall[1]},
        "results": results,
    }
    if args.json:
        out = json.dumps(report, indent=2)
        print(out)
        if args.out:
            with open(args.out, "w", encoding="utf-8") as f:
                f.write(out + "\n")
    else:
        print_table(results, register_rates, tier_totals, overall)
        if args.out:
            with open(args.out, "w", encoding="utf-8") as f:
                f.write(json.dumps(report, indent=2) + "\n")

    failed_floor = any(
        passed / total < floor_for_register(args, reg)
        for reg, (passed, total) in register_rates.items()
    )
    if overall[0] / overall[1] < args.min_overall or failed_floor:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
