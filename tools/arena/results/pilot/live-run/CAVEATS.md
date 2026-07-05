# Reading this pilot's rollup honestly

`rollup.md`'s `kitsoki-codex-native` row blends **2 real, live-validated cells**
(post-fix, real dispatch through the actual kitsoki bugfix pipeline) with
**1 stale cell** (pre-fix, produced by the old broken dispatch that ran an
identical raw `codex exec` call to the `single-briefed` arm — not a real
kitsoki-pipeline result). Do not read the blended `avg cost` for
`kitsoki-codex-native` as a real number; read the two real cells directly:

| task | kitsoki (real) | single-briefed | ratio |
|---|---|---|---|
| query-string-qs1-bugfix-test-repair | $0.44 | $0.48 | ~0.9x (kitsoki cheaper) |
| nextjs-05-story-runtime | $1.57 | $0.68 | ~2.3x |
| vscode-01-docs-site-release | **STALE — not re-run** | $0.27 | n/a |

Both real cells: `verdict: solved`, real cost computed cache-aware from the
kitsoki session trace's own per-call usage (`cached_input_tokens` billed at
`pricing.py`'s `cache_read` rate, not the full input rate — see
`real_trace_metrics` in `tools/arena/lib/paired_task_runner.py`).

The initial (now-corrected) pass of this validation reported $5.24 and $35.03
for these two cells — a >20x overstatement caused by a real cost-reporting
bug (summing `input_tokens` while discarding `cached_input_tokens`, then
pricing everything at the full input rate). See goal log
`.artifacts/goal/generalized-usage-live/log.jsonl` (seq 67+) for the full
trail: the dispatch-routing fix, the cost-accounting fix, and this correction,
in that order.

`vscode-01-docs-site-release`'s `kitsoki-codex-native` cell was left
deliberately unpatched (re-running it live was in scope but the operator
chose not to spend further this cycle) — its `health` field is stamped
`infra:stale` and its notes explain why. A future cycle should re-run it with
the fixed dispatch before treating this pilot as a complete 3-task
comparison.
