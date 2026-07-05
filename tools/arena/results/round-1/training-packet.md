# WB.4 round 1 — training packet

**process_version:** v1 (pre-training baseline; `dispatch_kitsoki` fix from
commits 6690d0d9/58a9182a/d315e581/63c867ef is the process under test, not a
patch produced BY this round — see §7 caveat below).

## Scope actually covered

The frozen training split (`docs/goals/generalized-usage` WB wall,
`tools/arena/corpus/cost-bench.manifest.yaml`) has 4 `bugfix_test_repair`
training tasks. Round 1 now covers **all 4**, across two sessions:

- `query-string-qs1-bugfix-test-repair` — real live data reused from the WB.3
  pilot validation (not re-run; see `tools/arena/results/pilot/live-run/`).
- `query-string-qs2-bugfix-test-repair` — live cells from the first round-1
  session.
- `kitsoki-bug9-bugfix-test-repair` and `kitsoki-bug12-bugfix-test-repair` —
  live cells from a second session, after fixing the structural gap that
  blocked them (below).

Only the `codex-native` candidate ran (both treatments use the identical
worker model, per the WB.4 fairness requirement); `synthetic-claude` is a
zero-cost negative control already proven in the WB.3 pilot and wasn't
re-run.

### Infra fixes that unblocked bug9/bug12 (both real, both committed)

1. **Docker git-worktree mount.** `kitsoki-bug9`/`kitsoki-bug12` reference
   `project: kitsoki` (this repo's own history). A git-worktree checkout's
   `.git` is a file pointing at an absolute host path into the primary
   checkout's `.git/worktrees/<name>`, which doesn't resolve inside a
   container that only mounts the worktree directory. Fixed by bind-mounting
   the primary checkout's `.git` at the identical path, read-only
   (`tools/arena/arena.py`'s `_primary_git_dir_for_worktree`).
2. **`git clone --local` cross-device hardlinks.** Once the mount above
   worked, arming still failed with `Invalid cross-device link` — `--local`
   defaults to hardlinking objects, which fails once the source is a
   read-only bind mount on a different filesystem than the `/tmp` clone
   destination. Fixed with `--no-hardlinks` on both `--local` clone sites
   (`paired_task_runner.py`'s `materialize_baseline`, `bench.py verify`'s
   private mirror).

Both fixes were verified live (no-LLM/free arm-only checks) before any
paid cell ran.

## Results (4 tasks x 2 treatments, n=1 each — 8 cells, all real, no fabrication)

| task | treatment | verdict | cost_usd (cache-aware) | tokens |
|---|---|---|---|---|
| qs1 | kitsoki | solved | $0.44 | 931,778 |
| qs1 | single-briefed | solved | $0.48 | 85,622 |
| qs2 | kitsoki | solved | $0.52 | 1,145,873 |
| qs2 | single-briefed | solved | $0.32 | 56,990 |
| bug9 | kitsoki | **failed** | $3.44 | 15,888,760 |
| bug9 | single-briefed | **failed** | $0.85 | 150,399 |
| bug12 | kitsoki | **failed** | $1.24 | 3,493,106 |
| bug12 | single-briefed | **failed** | $1.38 | 244,732 |

Aggregate across all 8: win-rate 0.5, kitsoki avg $1.41, single-briefed avg
$0.76. Total real spend across both round-1 sessions: **~$8.15** (qs2 +
bug9 + bug12 cells; qs1 reused from the WB.3 pilot).

## ⚠️ bug9/bug12's `failed` verdicts are likely CONFOUNDED, not clean signal

Both `kitsoki`-treatment threads (`.thread.md` postmortems) self-reported
completion and claimed "Validation completed," yet the arena's independent
oracle scored both `oracle=fail`. Both threads explicitly say the same
thing: *"the local environment did not have `go` on PATH, so I could not
re-run that command"* / *"this container did not have `go` on PATH."*

Root-caused and **fixed** after these cells ran: the paired-task Docker
image's `golang:1.25-bookworm` base sets Go/Cargo paths via `ENV PATH`, which
only reaches non-login shells. The live worker's `host.Bash` tool runs
commands via `bash -lc` (a login shell), which re-sources `/etc/profile` —
Debian's default there unconditionally overwrites the inherited PATH with a
bare system path, dropping `/usr/local/go/bin` entirely. Verified: `bash -lc
'which go'` failed before the fix, succeeded after. Fixed in
`tools/arena/docker/Dockerfile.paired-task` via a `/etc/profile.d/` script
(login shells source those; `ENV` does not reach them).

**Implication:** the agent may have produced a materially correct fix for
bug9/bug12 but skipped its own `go test` self-verification because the tool
appeared unavailable, then reported success anyway without real evidence —
a real, separate finding (a live worker should not report a fix as verified
when its own verification step silently failed to run) worth carrying into
the story/prompt layer once training patches start, but NOT yet actioned
this round (see anti-overfit note below). Because this confound existed for
both treatments equally (single-briefed also runs shell commands the same
way), it does not by itself explain the *treatment* comparison, but it does
mean **bug9/bug12's absolute solve-rate numbers should not be trusted as
this task's true difficulty** until re-run under the fixed image. That
re-run has NOT happened yet — flagging it as the immediate next step rather
than re-running unprompted, given the live spend already used this session.

## Analysis gate (against docs/research/cost-efficiency-benchmark.md §2 criteria)

**Still not evaluated against H1-H4.** The training split now has real data
for 4/20 tasks (1 of 4 archetypes), and 2 of those 4 tasks have a confounded
result that needs a clean re-run before it can be trusted. Declaring a
verdict off this subset — especially with a known confound sitting in it —
would violate the anti-overfit/fair-sample discipline the round protocol
itself requires.

## Training pass

**None landed.** Per §7's lifecycle, a training pass (failure mining →
generic patches → ratchet → version bump) is warranted when the analysis
gate says "not met" on *trustworthy* evidence. The bug9/bug12 failures are
real cells but a confounded signal (see above) — patching the story/prompt
layer against them now, before a clean re-run, risks fixing a Docker-image
bug's symptom rather than a real process gap. The self-verification-silently-
skipped finding is a legitimate candidate for the eventual training pass (a
generic guard: a live worker should not report a fix verified when its
verification tool call itself errored/was unavailable), but it's flagged
here for later mining, not actioned this round.

## Recommended next round

1. Re-run `kitsoki-bug9`/`kitsoki-bug12`'s `kitsoki`-treatment cells under
   the fixed Docker image (Go now reachable from `bash -lc`) to get a clean
   read on whether they're genuinely hard or were purely blocked by the
   PATH bug. Small, bounded (~$1.20-3.50/cell based on this round's costs).
2. Extend to the remaining 3 archetypes' training tasks (`docs_site_release`,
   `git_ops_landing`, `ui_visual_qa` — 12 more training tasks) before any
   analysis-gate verdict is drawn; even a clean 4-task sample is too narrow
   to support H1-H4 either way.
3. `vscode-01-docs-site-release` (held-out, not training) was refreshed
   under the dispatch fix this round for infra-validation purposes — see
   `tools/arena/results/pilot/live-run/CAVEATS.md` for why it does NOT count
   toward the eventual WB.5 confirmation run's "held-out executed once"
   discipline.
