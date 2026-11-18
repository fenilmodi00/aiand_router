# Next upstream port: triage of 051efc1cd..a1d169725 (150 commits)

Branch: v0.01 (post-port tip). Upstream moved 051efc1cd → a1d169725
(150 new commits). Five parallel triage agents classified every commit
against the fork's actual surfaces (file + symbol existence checks on
v0.01). This doc is the porting plan; nothing has been ported yet.

## Category counts

| Category | Count |
|---|---|
| PORT (clean) | 17 |
| PORT-PARTIAL (surviving-file hunks) | 24 |
| ADAPT (needs fork adaptation) | 18 |
| SKIP (fork-deleted surfaces) | 85 |
| DECISION (fork-default teardown; held for owner) | 3 |
| Net-zero pair | 3 (weave-random add+revert+cassette) |

## DECISION items — need your call before anything else

Upstream is dismantling the cluster scorer — the fork's production default
router (v1.0 bundle on the build.io dyno):

- ea51401c2 #2105 stop building the cluster scorer (build/registry wiring)
- 237b0cefb #2121 drop cluster-routing CI gates + docs
- 6540891c9 #2122 delete the cluster scorer, routing-report, ONNX build

All three reverse the fork's standing decision ("keep cluster as the fork
default" — recorded in the 2026-11-18 design doc). Porting them means the
fork must adopt `hmm_embedding` (or another registered strategy) as its
production default and lose the semantic cache + ONNX embed path. Until the
HMM sidecar is a real production option for this deployment, the
recommendation is **do not port**, as before.

## Recommended port waves (dependency order)

### Wave 1 — independent clean fixes (no interdeps)
| Upstream | What |
|---|---|
| 08ebf0e49 #2124 | turntype: auto-mode security monitor by system prompt |
| 83bfa675a #2158 | translate: name argument keys on malformed custom-tool wrapper |
| 749cfa460 #2163 | blind-experiment client picks labeled by experiment arm |
| d0f40c0ae #2207 | billing: no BYOK fee when org cannot pay |
| 9a874bccb #2251 | skip API-key cache on BYOK key fetch failure |
| 8f9930f91 #2263 | Anthropic thinking → Chat reasoning_content |
| 569fa8b3a #2264 | close each Anthropic tool_use block before the next (parallel tools) |
| 2871b05bb #2290 | HMM stay pins on effort-qualified history |
| b33ad3f30 #2293 | stop tagging eligibility failures as policy outage |
| f4de6bd15 #2287 | CI: block changes to merged migrations |
| 2a3c9daef #2212 | CI: full Test run per main commit (needs deploy-verify workflow per-SHA too) |
| bd4a1166b #2262 (partial) | DispatchErrorNoEligibleArm (400) + classifier-output retryable (routing half only) |
| 25a7edfcb #2252 (partial) | Content-Encoding kept only while body is upstream's bytes (2 fork flush paths) |

### Wave 2 — small ADAPTs (independent of each other)
| Upstream | What | Adaptation |
|---|---|---|
| b1f70281b #2133 | org inference allowlist middleware | trim session-token arm |
| 624badb19 #2146 | custom-tool input-wrapper capability gate | exclusion set starts empty (no MiMo rows) |
| c4becc6ac #2128 + 6858824a2 #2131 | Codex skill-turn force-model, typed directives | wire into fork's command_history shapes |
| a31f1c535 #2267 | installer keeps installed identity | X-Aiand-User-Email header names |
| f7b5de934 #2255 | jq 1.6+ gate + jq 1.8.1 download | drop login renames |
| 90956b39c #2301 | unversioned force-model aliases → newest release | wrap fork's aiand alias map |
| a1d169725 #2299 | routing-model canonical alias ("Weave Auto") | "aiand-auto" display alias; Cursor bypass fix applies |
| 0d05bb8e9 #2289 | x-weave-flag-overrides header | AbortWithStatusJSON per fork convention; needs no refusal pkg |
| 37a47110d #2266 (partial) | Chat reasoning_content → Responses reasoning item | needs RewindOutput + helper files |
| 8e65ea87b #2126 (partial) | force-model alias map split | aliases from aiand catalog |
| a197bc7b8 #2288 (partial) | classifier calls forwarded verbatim | verbatim prepare + pin exemption; no usage-bypass lane |
| 2405d0df3 #2202 | sidecar binding test | single-provider rewrite |

### Wave 3 — chained refactors (order matters)
Sequence: #2227 (auth.Clock in proxy) → #2178 (RequestFacts) → #2181
(shared turn preamble) → then optionally the package extractions:

1. 109ba3110 #2227 Clock injection (also unblocks upstream-style entrypoints)
2. 254c8d18d #2178 RequestFacts (drop subscription fact keys)
3. 8a40a8fa7 #2181 shared Messages/Chat preamble (no attempt-trace/usage-observer steps)
4. 49cc0e510 #2157 + 0aa1393d9 #2169 Responses switching primitive + attempt options
5. cb6ce6d5a #2171 tool-call truncation fix (ErrToolCallTruncated)
6. Dispatch moves (highest value, lowest risk): c8dfe0997 #2160 PreludeBuffer → bc50aed9f #2165 OrderedBindings → 0d1ae64ac #2170 EarlyStopGate → 5bea7d89d #2140 ThrottleLedger
7. a681f738d #2172 + ff27b5be9 #2173 rescueChain extraction (strip subscription/gateway methods)
8. d7769b54d #2161 → 42b9f5a52 #2164 → 053d917e9 #2168 credentials.Broker + routingpolicy.Planner — **only if the fork wants these packages**; each is a real structural port; #2179/#2185/#2192 (SessionPins/HMMCostGate/AuthoritativeGuards) ride on #2168
9. 15fb48db3 #2162 + 36dea2f9d #2167 ingress.Adapter (Responses only; fork has no Gemini ingress) → b6998430b #2187 Anthropic Messages adapter
10. c68e218ad #2184 + ad154d036 #2188 internal/conversation extraction
11. eb85a6f8c #2291 org/request-overridable routing-switch flags (needs #2289 + #2290)
12. 0eaec99f7 #2215 refusal table (trimmed; prerequisite for nothing we port — standalone decision)

### Wave 4 — lint-enable wave (big but mechanical)
Apply surviving-file hunks + enable each linter in .golangci.yml, in
upstream order: #2190 → #2193 (errcheck/bodyclose/nilerr) → #2194
(exhaustive) → #2195 (noctx/thelper) → #2196 (staticcheck set) → #2197
(testifylint/forcetypeassert/unparam) → #2201 (contextcheck; thread ctx
onto fork's own call sites) → #2206 (gosec; crypto/rand, pgtls
VerifyConnection). Also lift the new invariant/contract tests riding the
comment-ban commits (responses_invariants, stream/emit invariants,
semantics tests, watchdog contracts, proxy_test BYOK alias test,
mitmproxy store test, toolcheck tests, handover rewrite test, footer
placement + notice delivery tests) — the comment strips themselves are
SKIP (no comment-ban gate in the fork).

## Skipped (85 + net-zero), by reason
- Subscription pool/seat/lane/usage-bypass/Weave-credits: ~30 commits
- Golden decision-trace harness additions: ~15
- Comment-ban migration strips (gate absent in fork): ~18
- Anthropic/OpenAI/Gemini provider-adapter fixes (adapters deleted): ~8
- Fast-mode, attempt_trace, gateway caps, Codex quota, npm bumps,
  cassette refreshes, migration 0153/0154, GPT-6.2 catalog, weave-random
  (+revert): remainder

## What the fork gets if all waves land
Real bug fixes for its live surfaces: parallel-tool SSE correctness,
Anthropic thinking passthrough, cache-skip on BYOK fetch failure,
Content-Encoding on replayed errors, no-eligible-arm no longer paging,
HMM stay-pin correctness, installer identity/jq robustness, custom-tool
wrapper capability gating, plus CI hardening (migration immutability,
full-test-per-commit, race-detector already in) and the lint gates.
No cluster-default change; production routing behavior unchanged.
