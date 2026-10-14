# 02: Regenerate the deepseek and kimi AIDER fair-label arms to the same scale

**What to build:** The AIDER fair-label campaign proved that the corpus's original deepseek labels were produced under a broken prompt template and understated accuracy by ~3.5x (deepseek-v4-flash read 0.083 on the corrupted labels and measures 0.295 on fair ones). This ticket brings every lane's fair arm to a scale where the comparison is decisive rather than indicative.

Current fair-arm state, measured: glm-5.3 n=54 at 0.370, glm-5.3-flash n=90 at 0.378, deepseek-v4-flash n=149 at 0.295, deepseek-v4-pro n=149 at 0.295, deepseek-v4.1-flash n=129 at 0.295, kimi-k3 n=24 at 0.333, motif-3 n=6 at 0.500. The GLM point estimates lead by +7.5 to +8.3pp in every pairing but none separates (z 1.01–1.32, p 0.19–0.32).

The gap that matters is kimi-k3 (spend-capped at $2.30 mid-campaign, leaving n=24) and motif-3 (n=6), plus glm-5.3's own n=54 which ticket 01 addresses. This ticket closes the remaining shortfalls so no lane's verdict rests on a thin sample.

End-to-end result: every roster lane has a fair-label AIDER arm of n>=100 (or an explicit recorded reason why the lane cannot reach it), graded by the arena's real scorer, with total spend reported per lane.

**Blocked by:** None (can start immediately). Independent of ticket 01 — this ticket changes other lanes' samples, not glm-5.3's.

**Status:** ready-for-agent

- [ ] kimi-k3's fair arm raised from n=24 to n>=100, generated under the correct prompt template at the 16,384-token budget, with the lane's total spend stated (its $0.003/$0.0125-per-1k pricing makes it the most expensive lane; a per-lane cost cap must be set before launching and reported).
- [ ] motif-3's fair arm raised from n=6 to n>=100 on the same basis, or an explicit recorded reason the lane cannot reach it.
- [ ] Every regenerated cell graded by the arena's own scorer — go/python in WSL, rust/javascript in the ConTree sandbox (the grader for this exists and must be reused, not rebuilt) — with zero cells left at rc=127 or ungraded.
- [ ] A per-lane accuracy table with n and Wilson 95% intervals, produced from the graded rows, so the campaign's final comparison is reproducible from data rather than prose.
- [ ] No cluster artifact, corpus row, or Go source modified; all writes confined to generation caches and the campaign's own graded output files.
