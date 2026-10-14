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
