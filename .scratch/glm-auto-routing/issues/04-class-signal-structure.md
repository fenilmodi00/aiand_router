# 04: Close the structural blocker — AIDER and live-coding prompts share cluster cells

**What to build:** Even if GLM wins the agentic-repair class outright, the cluster cannot express it, because AIDER and live-coding prompts land in the *same* clusters and a cluster cell averages both. On fair LCBv5 labels deepseek-v4-pro scores 0.897 against glm-5.3's 0.783, so handing GLM the AIDER clusters would also hand it live-coding traffic it measurably loses. This is the structural reason no cell-level change can deliver GLM auto-routing by itself, and it is independent of whether GLM wins AIDER.

The flip arithmetic already exists and states the requirement precisely. With the *measured* fair margin (glm-5.3 +7.5pp over deepseek-v4-flash), flipping requires approximately 200 additional fair prompts on c0, 400 on c12, and never on c10 at any feasible N — c10 holds 317 existing labels and the margin cannot overtake it. So even a decisive AIDER win lands GLM on two of three coding clusters, not all three.

This ticket investigates and records the options for expressing a per-turn class signal inside the scorer, without letting any of them override measured quality:

- a turn-shaped decode that feeds the existing rank-decaying preference path (the mechanism exists and is already wired for repair-shaped turns, but its bonus of 0.15 cannot overcome a 1.37 blend gap, so it changes 0 of 180 probe routes — the magnitude question is explicitly in scope)
- a cluster-level split that keeps the class out of the averaged cell
- a documented statement that neither is supportable, with the measured reason

End-to-end result: either a mechanism that demonstrably routes the winning class to the winning lane on measured cells, or a recorded decision that the structure cannot express it — with the measurement that decides.

**Blocked by:** 03 (decide the AIDER cell ranking). This ticket only matters if 03 finds a lane that wins the class; if 03 records "unresolved," this ticket's finding is the reason, and it should still be recorded.

**Status:** ready-for-agent

- [ ] The cluster-sharing structure is quantified: how many AIDER and LCBv5 prompts land in each shared cluster, and what each class's measured accuracy is there, so the averaging cost is a number rather than an assertion.
- [ ] The flip requirement is recomputed with the verdict's actual measured margin, per cluster, stating which clusters can and cannot move at a feasible prompt count.
- [ ] Each candidate mechanism is evaluated for whether it can express the class signal WITHOUT overriding measured quality — specifically, whether the rank-decaying preference path can be sized to the measured gap, and what that magnitude would mean for how far an operator's signal may override the blend.
- [ ] The outcome is recorded as a decision: which mechanism (if any) ships, or an explicit statement that the structure cannot express the class, with the numbers that force that conclusion.
- [ ] Whatever ships is verified by driving register probes and corpus replay through the real scorer, with the mix-collapse guard and reachability checks both passing.
