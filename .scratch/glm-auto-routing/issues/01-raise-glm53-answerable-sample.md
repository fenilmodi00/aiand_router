# 01: Raise glm-5.3's answerable AIDER sample past the 16k reasoning-truncation ceiling

**What to build:** The corpus's AIDER labels for glm-5.3 are capped at n=54 because **101 of 106 cells returned `truncated(length)` with an empty answer** — glm-5.3 spends the entire 16,384-token budget on hidden reasoning and never emits the solution. This ticket makes glm-5.3 produce a gradeable answer on the same prompts, so its measured AIDER accuracy stops being sample-starved.

This is a tooling change, not a bundle change. The measured blocker: a 32k budget was already tested and made accuracy *worse* (0.250 vs 0.312 on the same cells), and a single 32k request timed out server-side after 400s — so raising the cap is not the fix. The fix is to get the reasoning out of the generation path.

Two candidate approaches, to be chosen at implementation time after measuring which works: (a) pass a reasoning-effort control on the request if the provider exposes one (verified to work on this endpoint: `reasoning_effort: low` cut a trivial call from 67 to 18 completion tokens with 0 reasoning tokens on glm-5.3-flash); (b) request a structured/JSON response that constrains output to the artifact, so reasoning tokens cannot consume the budget.

End-to-end result: a regenerated glm-5.3 AIDER arm of n>=100 graded cells, produced by the same generation path every other lane uses, with the per-cell token usage recorded so the arm is auditable.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] glm-5.3 answers (non-empty, closed code fence) on >=100 of the 150 AIDER prompts at a budget that does not exceed the existing per-request timeout, with per-cell `token_usage` recorded.
- [ ] Those answers are graded by the arena's own `metrics.aider_accuracy` (which runs each task's real `test_command`), for go/python in WSL and rust/javascript in the ConTree sandbox — zero cells left at rc=127 or ungraded.
- [ ] The measured accuracy on the regenerated arm is reported with n and the Wilson 95% interval, and compared against the existing 16k arm (0.370, n=54) on the SAME cells, so the change's effect is attributable.
- [ ] No cluster artifact, corpus row, or Go source is modified; the campaign writes only to the generation caches and its own graded output.
- [ ] The truncation rate before/after is stated as a number (currently 101/106 truncated), and the mechanism chosen (reasoning-effort control vs structured output) is named with the evidence that decided it.
