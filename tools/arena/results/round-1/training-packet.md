# WB.4 round 1 — training packet

**process_version:** v1 (pre-training baseline; `dispatch_kitsoki` fix from
commits 6690d0d9/58a9182a/d315e581/63c867ef is the process under test, not a
patch produced BY this round — see §7 caveat below).

## Scope actually covered (partial — disclosed, not hidden)

The frozen training split (`docs/goals/generalized-usage` WB wall,
`tools/arena/corpus/cost-bench.manifest.yaml`) has 4 `bugfix_test_repair`
training tasks. Round 1 covers **2 of 4**:

- `query-string-qs1-bugfix-test-repair` — real live data reused from the WB.3
  pilot validation (not re-run; see `tools/arena/results/pilot/live-run/`).
- `query-string-qs2-bugfix-test-repair` — new live cells, run this round.

`kitsoki-bug9-bugfix-test-repair` and `kitsoki-bug12-bugfix-test-repair` were
**not run** — arming them inside the paired-task Docker container fails with
`fatal: not a git repository: /Users/brad/code/Kitsoki/.git/worktrees/...`.
These two tasks reference `project: kitsoki` (this repo's own history), and a
git-worktree checkout's `.git` file is a pointer to an ABSOLUTE HOST PATH
(the primary checkout's `.git/worktrees/<name>`) that doesn't exist inside a
container that only mounts the worktree directory itself. This is a real,
structural Docker/git-worktree gap — not a paired-task or model failure — and
is unresolved. Fixing it (mount the primary checkout's `.git` too, or convert
the mounted checkout to a standalone clone before handing it to Docker) is
out of scope for this round; flagging it as the next infra prerequisite.

Only the `codex-native` candidate ran (both treatments use the identical
worker model, per the WB.4 fairness requirement); `synthetic-claude` is a
zero-cost negative control already proven in the WB.3 pilot and wasn't
re-run.

## Results (2 tasks x 2 treatments, n=1 each — 4 cells, all real, no fabrication)

| task | treatment | verdict | cost_usd (cache-aware) |
|---|---|---|---|
| qs1 | kitsoki | solved | $0.44 |
| qs1 | single-briefed | solved | $0.48 |
| qs2 | kitsoki | solved | $0.52 |
| qs2 | single-briefed | solved | $0.32 |

Aggregate: kitsoki avg $0.48, single-briefed avg $0.40 — kitsoki costs
roughly **1.2x** the naive baseline on this 2-task sample, both at 100%
solve rate. Total real spend this round: **$0.84** (qs2 only; qs1 reused).

## Analysis gate (against docs/research/cost-efficiency-benchmark.md §2 criteria)

**Not evaluated against H1-H4 yet.** The frozen success criteria are defined
against the FULL training split (all archetypes, both candidates), and this
round covers 2/20 training tasks in 1 archetype with 1 candidate. Declaring a
verdict off this subset would violate the anti-overfit/fair-sample discipline
the round protocol itself requires. This round's purpose was narrower and
explicit going in: prove the round protocol can execute for real (arm → run
→ score → aggregate, live, with honest costs) — it does.

## Training pass

**None landed.** Per §7's lifecycle, a training pass (failure mining →
generic patches → ratchet → version bump) is only warranted when the
analysis gate says "not met" on real evidence. With only 2 tasks and a 100%
solve rate on both arms, there is no failure signal to mine yet, and patching
against a 2-task sample would be exactly the overfitting the round protocol
exists to prevent.

## Recommended next round

1. Fix the git-worktree-in-Docker gap (mount the primary checkout's `.git`,
   or clone-then-mount) so `kitsoki-bug9`/`kitsoki-bug12` can arm.
2. Extend to the remaining 2 archetypes' training tasks (`docs_site_release`,
   `git_ops_landing`, `ui_visual_qa` — 12 more training tasks) before any
   analysis-gate verdict is drawn; the current 2-task sample is too narrow to
   support H1-H4 either way.
3. Re-run `vscode-01-docs-site-release`'s kitsoki cell (still stale from
   before the dispatch fix, per `tools/arena/results/pilot/live-run/CAVEATS.md`).
