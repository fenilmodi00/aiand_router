# GLM auto-routing — ticket breakdown

**Source:** the GLM merit campaign recorded in `.context/glm-merit-evidence.md` (§1–§7) and the preceding conversation.

**The question these tickets answer:** can the cluster choose `zai-org/glm-5.3` and `zai-org/glm-5.3-flash` automatically, on measured merit, for coding-agent traffic?

**Current measured position (all reproducible from the committed scripts and corpora):**

| Evidence | Value |
|---|---|
| glm-5.3 AIDER (agentic repair, fair labels) | 0.370 (n=54) |
| glm-5.3-flash AIDER, fair labels | 0.378 (n=90) |
| deepseek-v4-flash / v4-pro / v4.1-flash AIDER, fair labels | 0.295 each (n=149/149/129) |
| Pairwise separation | none — z 1.01–1.32, p 0.19–0.32 |
| glm-5.3 LCBv5 vs deepseek-v4-pro (fair labels) | 0.783 vs 0.897 |
| Smallest blend gap GLM faces on any of 180 register probes | 1.366 (preference bonus is 0.15) |
| glm-5.3 cells won at any cost-cap rung, any alpha | 0 of 16 |
| glm-5.3-flash cost multiple | 1.29x (cheaper than deepseek-v4.1-flash at 2.04x) |

**Closed levers, with their measured outcomes** (so they are not retried):

- *More AIDER labels* — GLM's lead holds (0.364 → 0.370) but does not grow; the opponents' fair labels rose 3.5x, which is what removed the earlier apparent advantage.
- *A wider generation budget* — 32k scored 0.250 against 16k's 0.312 on the same 32 graded cells. The change made accuracy worse.
- *A different traffic class (Terminal-Bench 4.0)* — no ~20pp margin; 2 of 66 tasks are locally gradable, and one showed glm-5.3 breaking its own working fix on retry while glm-5.3-flash passed all 7 tests.
- *A tighter cost cap to favour the cheap lane* — cap 2.0x/1.5x/1.3x all scored 72.82% against 3.0x's 74.90%: −2.08pp for +7% cost, because deepseek-v4.1-flash legitimately holds 46.8% of traffic.
- *The quality-dial tiers (low/mid/high)* — simulated at alpha 0.20/0.40/0.60/0.80/0.96/1.00: GLM wins 0 clusters at every setting. At pure quality weight (1.00) the winners are still the deepseek lanes.

**Deliberately excluded:** hand-setting GLM's quality cells from published benchmarks or intuition. That would override measured evidence and price every request at GLM's premium rate on the strength of a guess. Four of this campaign's intuitions were corrected by measurement; the cells must not encode them.

---

## Evidence provenance

The measurements behind this breakdown live in `.context/glm-merit-evidence.md`
(private, gitignored). It carries no customer or organisational data — only model
accuracy, cost and token measurements — so the tables above are reproduced here
so this plan stands alone. The scripts that regenerate every number are committed
under `scripts/` (the bake-off, the measured-cell builders, the fair-label
graders, the TB4 gate) and the corpora under `.context/label-runs/`.
