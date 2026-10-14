# 03: Decide the AIDER-class cell ranking on statistics, not point estimates

**What to build:** The AIDER class currently has no defensible ranking. On fair labels glm-5.3 (0.370, n=54) and glm-5.3-flash (0.378, n=90) lead deepseek-v4-flash/pro/1-flash (0.295 each) by +7.5 to +8.3pp, but no pairing separates at p<0.05. This ticket runs the decisive test once tickets 01 and 02 have closed the sample gaps, and records the outcome — whatever it is.

The test must be pre-registered before the numbers are seen, so the result cannot be tuned to a conclusion: the required effect (which pairwise comparison, which lane must lead, what p-value and minimum effect size counts as separation) is written down first, then evaluated.

Two outcomes are both acceptable deliverables:

1. **GLM separates** — then the winning lane's AIDER cells are rebuilt from the fair arm and the cluster routes GLM on merit automatically, with no preference override and no cost-cap trick. This is the only path to auto-routing that is not a guess.
2. **GLM does not separate** — then the class is recorded as unresolved, the cells keep their measured values, and GLM stays available on demand rather than being promoted on an unseparated point estimate.

End-to-end result: a recorded verdict with the intervals and the pre-registered test outcome, and either a rebuilt bundle whose routing change is visible, or an explicit statement that no bundle change is justified.

**Blocked by:** 01 (raise glm-5.3's answerable AIDER sample), 02 (fair-label arms to scale). Both must complete, because a verdict on mismatched sample sizes is exactly the error the earlier phases made.

**Status:** ready-for-agent

- [ ] The pre-registered test is written down BEFORE the comparison is run: the pairwise comparisons to be evaluated, the lane that must lead each one, the significance threshold, and the minimum effect size that counts as separation.
- [ ] The comparison is run on the fair-label arms from tickets 01 and 02, reported as accuracy with n, Wilson 95% intervals, z and p per pairwise comparison.
- [ ] The verdict is recorded plainly: separated (with which lane, on which class) or not separated, with the numbers that decide it. An unseparated result is reported as unresolved, never as a win.
- [ ] If GLM separates, the bundle's AIDER cells for the winning lane are rebuilt from the fair arm through the existing measured-cell builder, and the change is verified by driving register probes and corpus replay through the real scorer — including the mix-collapse guard, which must not regress.
- [ ] If GLM does not separate, no cell is changed and the reason is recorded in the class's evidence file.
- [ ] The evidence file for this class records the before/after sample sizes, the spend that produced them, and the final verdict.

## Pre-registration — the decisive AIDER comparison (registered 2026-10-14, before any new number is read)

This section is the pre-registration required by the checkbox above. It is written
**before** tickets 01 and 02 report any regenerated cell, so the result cannot be
tuned to a conclusion. The identical registration record is filed at
`.context/label-runs/pre_registered_test.md`; the two are the same text.

### R1. Inputs (fixed in advance)

The comparison runs exactly once, on the fair-label AIDER arms as they land:

| arm | source | requirement |
|---|---|---|
| glm-5.3 | ticket 01 regeneration, `reasoning_effort=low` arm, graded by `metrics.aider_accuracy` | answerable n >= 100 |
| glm-5.3-flash | ticket 02 fair arm | n >= 100 |
| deepseek-v4.1-flash | ticket 02 fair arm | n >= 100 |
| deepseek-v4-flash, deepseek-v4-pro | committed baseline (already separated; see R3) | not re-derived |
| kimi-k3 | ticket 02 fair arm | n >= 100 for its pairwise to be evaluable |
| motif-3 | ticket 02 fair arm or recorded reason | not part of any decisive pairwise at n=6 |

No cell is re-graded, dropped, or regenerated after the arms are frozen to change a
number. The arms as written are the evidence.

### R2. The pairwise comparisons, in priority order

**P1 (decisive) — GLM combined vs deepseek-v4.1-flash.** GLM combined is the pooled
cell set of glm-5.3 + glm-5.3-flash fair arms (this is the pairing the committed
baseline left overlapping: 0.306 (0.228, 0.397) vs 0.295 (0.223, 0.378)). The
required leader is **GLM combined**. P1 is the only comparison that can declare
"GLM separates from the deepseek field".

**P2 — glm-5.3 vs glm-5.3-flash.** Within-GLM, decides *which* lane's cells are
rebuilt if P1 separates. Either lane may lead; no direction is pre-committed.

**P3 — glm-5.3 vs kimi-k3**, evaluated **only if kimi-k3's fair arm reaches
n >= 100**. Required leader: glm-5.3. Reported for completeness; it cannot alone
declare separation.

Excluded, with reason: deepseek-v4-flash and deepseek-v4-pro are already separated
on the committed baseline (GLM combined 0.306, Wilson 95% CI (0.228, 0.397) versus
v4-flash 0.153 CI (0.102, 0.213) and v4-pro 0.160 CI (0.107, 0.224) — the GLM
lower bound sits above both upper bounds), so re-testing them cannot change the
verdict and is not run.

### R3. Significance threshold and minimum effect size

- Significance: **two-sided alpha = 0.05**, two-proportion z-test with pooled
  proportion; Fisher's exact test (two-sided) is substituted when any expected cell
  count is < 5. The test actually used and its p are reported per pairing.
- Minimum effect size (MES): **5 accuracy points (0.05) of absolute gap**, measured
  as GLM minus comparator on the fair arms.

  Justification. The committed fair-label margin is +7.5 to +8.3pp in every pairing,
  so 5pp sits below the effect this campaign has actually observed — registering at
  5pp does not pre-emptively falsify GLM's lead, and it rules out declaring
  separation on a shrunken point estimate. 5pp is also the smallest gap with any
  consequence: the flip arithmetic in the plan requires ~0.80 vs ~0.60 accuracy
  (~20pp) over the target clusters, and a sub-5pp rank ordering is not one that any
  cell-level decision can consume. Below 5pp the "winner" is a coin-flip ranking
  that cannot move a cell, so it is recorded as no separation rather than a win.

**Separation requires all three at once:** GLM leads, gap >= 5pp, and both
separation criteria of R4 hold.

### R4. The two separation criteria and their tie-break

1. **Criterion A — non-overlapping Wilson 95% score intervals** on the two fair arms
   (per-arm Wilson, not a CI on the difference).
2. **Criterion B — p < 0.05** on the two-proportion z-test (or Fisher exact per R3).

Tie-break: **Criterion A wins.** The two criteria disagree only in one direction —
B significant while the Wilson intervals still overlap, because non-overlapping 95%
intervals are a strictly stronger statement than a pooled two-sided test at
alpha = 0.05. When they disagree the pairing is recorded as **not separated**, with
both the intervals and the p reported so the disagreement is visible. A significant
p never upgrades an overlapping-interval pairing.

### R5. Consequence of each outcome (verbatim from this ticket)

- **GLM separates** — the winning lane's AIDER cells are rebuilt from the fair arm
  through **`scripts/build_v084_measured_cells.py`** (the measured-cell builder),
  and the change is verified by driving **register probes and corpus replay
  through the real scorer, including the mix-collapse guard, which must not
  regress**. The winning lane is the P2 leader if P2 separates; if P2 does not
  separate while P1 does, **both** GLM lanes' AIDER cells are rebuilt (the combined
  arm is what separated and its components are co-leaders) and the record states
  that the lane split was not resolvable on these numbers. This remains subject to
  ticket 04's structural finding — a rebuild that the cluster cannot express is
  recorded, not shipped.
- **GLM does not separate** — **no cell is changed**, the class is recorded as
  **unresolved**, and GLM stays available on demand rather than being promoted on
  an unseparated point estimate. An unseparated result is reported as unresolved,
  never as a win, and never as a loss for GLM either.

### R6. Inconclusive rule (not-evaluable is not a win)

A lane that cannot reach **n >= 100** makes its pairwise **not-evaluable**.
Not-evaluable cannot count as a win for either side: it is never read as GLM
separating, and it is never read as GLM losing. Consequences:

- P1 not-evaluable (either GLM component n < 100, or the deepseek-v4.1-flash fair
  arm n < 100) -> the verdict is **unresolved**, no cell change, reason recorded.
- P3 is skipped entirely unless kimi-k3 reaches n >= 100; a skipped P3 is reported
  as "not evaluated" and never as "GLM wins vs kimi" or "GLM does not win".
- P2 not-evaluable -> only P1 is reported; the lane split is deferred and recorded
  as unresolved, which does not block a P1-driven rebuild of both lanes.
- If the P1 arms evaluate but **neither** criterion of R4 holds at gap >= 5pp, that
  is a measured non-separation (unresolved), distinct from not-evaluable.

### R7. Reporting

One run, reported once: per pairwise, n, accuracy, Wilson 95% CI, gap in accuracy
points, test used, z (where applicable) and p, separated yes/no, and the resulting
verdict line. The verdict line is one of: `SEPARATED (lane)` or `UNRESOLVED` or
`NOT EVALUABLE`. Nothing in this section may be rewritten after the arms land.

---

## Resolution (2026-10-14, evaluated after ticket 01 closed)

### Full-corpus evaluation (after ticket 02's fair arms completed, 2026-10-14)

Ticket 02 raised the non-GLM lanes to fair-arm scale on the same 16,384-token
budget and the same scorer, so P1 can now be evaluated against a correctly-sized
opponent rather than the stale 132-cell arm. Every number below is full-corpus
(go + python + rust + javascript graded together, n = graded cells):

| lane | n | acc | Wilson 95% |
|---|---|---|---|
| GLM combined (elo arm) | 299 | 0.2709 | (0.2237, 0.3239) |
| deepseek-v4.1-flash | 133 | **0.4135** | (0.3334, 0.4985) |
| deepseek-v4-pro | 149 | 0.2886 | (0.2219, 0.3659) |
| deepseek-v4-flash | 149 | 0.2282 | (0.1681, 0.3019) |
| kimi-k3 | 101 | **0.6238** | (0.5264, 0.7121) |
| motif-3 | 116 | 0.0776 | (0.0414, 0.1409) |

Pairwise against GLM combined, both R4 criteria:

| opponent | gap | z | p | Wilson overlap | criterion A | separated |
|---|---|---|---|---|---|---|
| deepseek-v4.1-flash | **-14.26pp** | -2.946 | 0.0032 | no | **PASS** | **yes, against GLM** |
| deepseek-v4-pro | -1.77pp | -0.394 | 0.6934 | yes | FAIL | no |
| deepseek-v4-flash | +4.27pp | +0.975 | 0.3295 | yes | FAIL | no |
| kimi-k3 (P3) | **-35.29pp** | -6.387 | <1e-9 | no | **PASS** | **yes, against GLM** |
| motif-3 | +19.33pp | +4.288 | <1e-9 | no | PASS | yes, against GLM |

**Verdict, unchanged in direction and now decisive: `UNRESOLVED` for GLM.**
GLM never led any correctly-sized opponent: the only two pairings that separate
do so *against* GLM (v4.1-flash by 14.3pp at p=0.003, kimi-k3 by 35.3pp at
p<1e-9). GLM's lead over motif-3 (+19.3pp) is real but motif is the weakest lane
on the roster, and the remaining pairings do not separate.

**P3 is evaluable and lands against GLM.** kimi-k3 reached n=101 graded / 102
answered, so R6's not-evaluable rule no longer applies. kimi-k3 measures 0.624
against GLM's 0.271 — a 35.3pp gap with non-overlapping intervals. The working
hypothesis that motivated this campaign, that glm-5.3 is as strong as kimi-k3 at
a third of the price, is **falsified on this corpus**: kimi-k3 is the strongest
lane measured, and it is not close.

**Why the deepseek arms improved so much.** Every deepseek lane rose sharply
against its old corpus labels (v4.1-flash 0.235 -> 0.414, v4-pro 0.160 -> 0.289,
v4-flash 0.153 -> 0.228) because the fair 16k regeneration supersedes the
broken-template labels that understated them by ~3.5x. GLM's arm was already
measured under the correct prompt template, so it had no such correction to gain
— the earlier +7-8pp lead was GLM being measured fairly while it was not.

**Consequence applied (R5):** no cluster cell changes, `build_v084_measured_cells.py`
is not run, GLM stays available on demand. Ticket 04's structural finding stays
open but has nothing to act on.

**Verdict: `UNRESOLVED`** — the P1 pairing does not separate on either criterion,
and the point estimate now favours the deepseek lane.

Ticket 01 removed the truncation blocker (106/150 -> 2/150 empty cells on
glm-5.3, 70/150 -> 0/150 on glm-5.3-flash, median 387 output tokens per answered
cell against the plain arm's 16,384-token burns), so GLM's arm finally reaches a
sample size the test can act on. Graded by the arena's own
`metrics.aider_accuracy` over all 150 prompts, go/python in WSL plus rust/js in
the ConTree sandbox, zero ungraded cells:

| lane | n | acc | Wilson 95% |
|---|---|---|---|
| glm-5.3 | 150 | 0.3067 | (0.2385, 0.3845) |
| glm-5.3-flash | 149 | 0.2349 | (0.1740, 0.3091) |
| GLM combined | 299 | 0.2709 | (0.2237, 0.3239) |

P1 = GLM combined vs deepseek-v4.1-flash, both separation criteria of R4 required:

| opponent arm | gap | z | p | Wilson overlap | criterion A | separated |
|---|---|---|---|---|---|---|
| v4.1-flash fair, 0.295 (n=129) | -2.37pp | -0.502 | 0.616 | yes | FAIL | no |
| v4.1-flash merit arm, 0.235 (n=132) | +3.61pp | 0.787 | 0.431 | yes | FAIL | no |

The pre-registered minimum effect size was 5 accuracy points; neither pairing
reaches it, and the fair-arm gap is negative. The earlier +7-8pp point lead was
sample selection: it was computed on GLM's 111 answerable cells while 205 cells
were lost to the reasoning-token bug, and those recovered cells pass at 26.4%,
below the already-answered rate. With the blocker fixed the combined estimate
moves from 0.306 (n=111) to 0.271 (n=299).

Per R5 the consequence is applied: **no cluster cell changes.** The bundle keeps
the measured cells, GLM stays available on demand (`RequestedModel` /
`PreferredModels` / pins, with `premium_worthy.go`'s 6.0x cap relaxation), and no
`scripts/build_v084_measured_cells.py` rebuild is run. Full evidence and the
before/after truncation numbers are in `.context/glm-merit-evidence.md` section 8.

**P3 (glm-5.3 vs kimi-k3) is not evaluable.** kimi-k3's fair arm has not reached
n>=100, so per R6 it cannot be read as a win or a loss either way.

**Ticket 04 remains open and is still the structural question:** even a decisive
AIDER win could not be expressed, because AIDER and LCBv5 prompts share cluster
cells (fair LCBv5: deepseek-v4-pro 0.897 vs glm-5.3 0.783). The UNRESOLVED verdict
does not close that finding; it means 04 does not need to act on it yet.

### Data-integrity note on kimi-k3's arm (recorded after verification)

kimi-k3's graded output contains 155 rows for 101 cells; 54 cells carry duplicate
rows because the generation lane was still appending while grading ran. Two dedup
rules give 63/101 (last row per cell) and 62/101 (first row per cell).

The difference is a single cell, AIDER_29, graded twice with opposite results
(rc=1 "FAIL TestPushAndPop", then rc=0 "ok linkedlist"). Re-grading AIDER_29
against its current cache row is deterministic and returns score=1.0, rc=0, so
the rc=1 grade was made against a superseded generation of the same answer. The
report's last-row-per-cell rule (0.624) is therefore the correct basis.

The verdict does not depend on the choice: kimi-k3 beats GLM combined by 35.29pp
(p=1.7e-10) on the last-row basis and 34.30pp (p=5.0e-10) on the first-row basis,
with non-overlapping Wilson intervals in both. P3's conclusion is
dedup-basis-invariant.
