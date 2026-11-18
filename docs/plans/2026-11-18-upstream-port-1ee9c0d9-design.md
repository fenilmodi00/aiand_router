# Upstream port 2026-11-18: inherit 1ee9c0d9..051efc1cd (38 commits)

## Intent

Bring the aiand-only v0 deployment branch up to date with upstream main
(38 new commits since the last inherit, `df77637fd`, which covered
0125c2b5..1ee9c0d9). Port upstream code **verbatim**; only mechanical
fork renames (aiand provider, surviving catalog rows, deleted
subscription/pool surfaces) are applied. No rewrites.

## Why

v0 serves the Aiand-provider audience and must keep receiving upstream's
bug fixes and improvements; it deliberately diverges by having removed
third-party providers, subscription pools/OAuth, and non-Aiand catalog rows.

## Ground rules (from fork owner)

- Work on branch `v0.01` cut from `v0`; merge to `v0` only after the suite
  passes and behavior of the running deployment is preserved.
- Do not break the running deployment; if a port would break it, adapt or
  defer, and record why.
- Copy upstream code as-is wherever the touched files exist here.

## Verdict per commit (38 total)

### A. Port as-is (13)
| Upstream | Subject |
|---|---|
| 56cb3e478 | terminal signals for translated turns; Cursor agent ids from paths with spaces |
| c901587db | close response bodies; replace deprecated HTTP/2 APIs |
| 69316d62f | policyclient: stop using deprecated CredentialsJSON |
| a3eb9e365 | place Anthropic mid-conversation system messages where upstream accepts them |
| 5e7dcd0ea | keep relocated Anthropic system runs in original order |
| 4f7f90549 | failed fallback candidates no longer stack routing markers |
| e87155312 | sessionpin: keep the pinned provider when a different model served the turn |
| a56420c47 | bill upstream usage on streams the client abandons |
| 77b007929 | router_on experiment arm routes client-picked models |
| 03eaaba1c | serve catalog token prices on model listings |
| 6d9bcc591 | strip prior-turn router directives from replayed history |
| 13a4ba58b | installer: request the catalog scope for the models fallback listing |
| 051efc1cd | analytics when connecting Copilot/Claude Desktop |
| aa914931d | Pi harness separate model — port with aiand-roster substitution |

### B. Port chained, in this order (6)
40272112b (sentinels/roster types out of cluster) → 5150b0bfd (utility
hard pins from catalog) → 1509de351 (compaction on session pin) →
904c0d588 (admin universe from HMM roster) → a2e3fd619 (managed default
listing from catalog). d641f4aa4 (telemetry inner-ring port) is
independent of the chain; its fastmode.go hunks are dropped (fast-mode
was cut from this fork).

### C. Partial ports (3)
- a3a1d3652 unused-code removal: only hunks in files that exist here.
- eda2d889f depleted-credits warning: billing + marker hunks;
  usage_bypass.go does not exist here.
- 9d829a9d2 skills: all except golden-traces (that harness is not ported).

### D. Tooling (5)
f57f9eb5e (race detector CI), 777b0c213 (hot-path benchmarks),
d55e4e02e (lint dup-code gate), f65b514b1 (arch ratchet), 67491bb5d
(bounded-contexts docs). CI workflow hunks adapted to fork lanes.

### E. Skipped (11)
- e02a0e1ff: migration renumber — fork already carries it as 0145.
- d88b6558d, 8dd156911, 5d735088c, 09c7094d7: subscription pool/OAuth
  stack deleted from fork; only no-op seam remains.
- f0627a7c2: smoke cassette refresh — synthetic, no value here.
- 2de2623b1: Mythos 5.2 catalog rows — not served by this fork.
- 2b06c4791 + 441387dfc: golden decision-trace harness assumes the
  hmm_embedding default this fork does not adopt (see F).

### F. Adapted (1)
- 9b318c721 "remove cluster as default and fallback routing strategy":
  the fork's production deployment routes via the cluster scorer (v1.0
  bundle) and has no HMM classifier wired; flipping the default to
  hmm_embedding would fail production boot. Adopt the fail-closed
  strategy-registry semantics and cleanup hunks that do not flip the
  fork's default; keep cluster as the fork default. Revisit after the
  HMM sidecar is a real production option.

Post-port disposition (2026-11-18): the fail-closed registry semantics this
commit brings are already enforced by the fork (main.go panics when
ROUTER_DEFAULT_STRATEGY names an unwired strategy; the readiness checker
validates it at boot). The remaining hunks flip the deployment default to
hmm_embedding and rebuild the smoke stack on an HMM stub — rejected for this
fork per the owner's decision (production v0 serves via the cluster scorer).
The ROUTER_EXPLORE_ENABLED wrapper is retained because the fork's default
router IS the cluster scorer it wraps. Verdict: absorbed, no code change.

## Non-goals

- No behavior changes beyond what the ported commits carry.
- No rewrite of fork cuts (subscriptions, non-Aiand providers stay out).
- No merge to v0 in this task; v0.01 is reviewed first.

## Verification

- `go build ./...` and the repo test suite green on v0.01 after each batch.
- `make generate` if any sqlc/queries change (only 5d735088c touches SQL and
  it is skipped; expected none).
- Final report: per-commit disposition, adaptation notes, suite result.
