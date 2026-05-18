# Dogfood regression — orchestrator on-error redirect loop

Author: Brad Smith. Triggered by the `go_bugfix` hang observed in
`stories/kitsoki-dev/app.yaml` after commit `9b58dc4`. Trace evidence:
`/tmp/kitsoki-dogfood-trace.log` shows `emit_intent: start` firing
repeatedly at `core.bf.idle` with `host.git_worktree.create` requeued
each cycle, 200k+ trace lines, never reaching `turn.done`.

## 1. Root cause

The `bf.idle` room's new `on_enter` chain (`stories/bugfix/rooms/idle.yaml:48-77`)
runs two steps when a ticket is loaded and no workspace is set yet —
(a) invoke `iface.workspace.create` with `on_error: idle` (line 71),
and (b) `emit_intent: start` to auto-advance. On a fresh machine both
fire; on a host with `.worktrees/bf-<id>/` left over from an aborted
run, step (a) returns `Result.Error` from
`internal/host/git_worktree.go:116-118` ("`fatal: '<path>' already
exists`"). The orchestrator's
`dispatchHostCalls` (`internal/orchestrator/orchestrator.go:1220-1240`)
honours `on_error: idle` by calling
`enterRedirectState` (`orchestrator.go:1276-1374`), which re-runs the
target room's `on_enter` chain via `RunEffectsAndState` and then
re-dispatches any host calls it produced. Because the redirect target
is the same room we just left (`bf.idle` → `bf.idle` after
import-resolution of `idle`), the create invoke and the auto-start
emit fire again, the create fails again, and the cycle repeats. There
is no recursion cap on `enterRedirectState`; the
self-redirect guard at `orchestrator.go:1300-1306` only fires when
`prior == target`, but the import rewriter resolves `on_error: idle`
to a sibling-relative path that doesn't string-equal the prior path,
so the guard misses this case.

The existing `EmitIntentMaxDepth=8` cap (`internal/machine/parallel.go:88-93`)
bounds emit_intent chains inside one `Turn`, not the orchestrator's
host-dispatch-then-redirect loop. The redirect path resets the
machine-side counter on each cycle, exactly the same shape the
`OrchestratorPostBindMaxDepth` cap (`orchestrator.go:907-919`) was
introduced to protect against in a sibling code path.

## 2. Why the existing tests missed it

Every `stories/bugfix/flows/*.yaml` fixture (42 files) registers a
catch-all stub for `host.git_worktree` returning `{ok: true, …}` — see
`happy_human.yaml:46-49` and analogous blocks in every other fixture.
The stub never errors, so the `on_error: idle` arc in
`stories/bugfix/rooms/idle.yaml:71` is never taken in any flow test,
and the loop shape never forms. Same story for `host.git`,
`host.local`, and the inbox handler: the flow-fixture model
deliberately stubs everything to `{ok: true}` so authors can write
state-machine contracts without provisioning real services.

The dogfood flows in `stories/kitsoki-dev/flows/` (5 files) sidestep
the path a different way: they seed `core__workspace_id` in
`initial_world` (`pickup_self_bug_supervised.yaml:46`,
`pickup_autonomous_then_bail.yaml:26`,
`dogfood_autonomous_smoke.yaml:26`,
`pickup_story_bug_supervised.yaml:21`), so the `world.workspace_id ==
''` guard at `idle.yaml:61` evaluates false and the create invoke is
skipped entirely.

There are no end-to-end tests anywhere in `internal/` that exercise
the orchestrator's redirect path against a real
`host.git_worktree` handler bound to a real filesystem.
`internal/host/git_worktree_test.go` looks like an integration test,
but every case uses `SetExecRunnerForTest` to fake the shell — see
`git_worktree_test.go:50, 77, 101, 117` — so the "worktree already
exists" failure mode is never observed by any test, anywhere.

## 3. Recommended fixes

**Priority 1 — Engine: cap `enterRedirectState` recursion.** Add an
`EnterRedirectMaxDepth` constant (4 is appropriate — the
`EmitIntentMaxDepth=8` budget is for legitimate emit composition;
on-error redirects rarely nest legitimately, and 4 still permits
hand-off chains like ticket-search → main → idle). Thread a `depth`
parameter through `enterRedirectState` and the nested
`dispatchHostCalls` call at `orchestrator.go:1352`. When the cap
fires, mirror the `OrchestratorPostBindMaxDepth` shape at
`orchestrator.go:953-981`: append a `store.HarnessError` event
(defined `internal/store/event.go:85`), populate
`TurnOutcome.HarnessError`, return without re-entering. The test
pattern is already in place — see
`internal/orchestrator/settle_post_bind_emits_cap_test.go` for the
identical assertion shape (`require.NotEmpty(t, out.HarnessError,
...)` and the `HarnessError` event scan at lines 141-149). A new test
in `hostdispatch_test.go` should mount an app where on_error redirects
to a state whose on_enter re-invokes the same failing host call and
assert the cap fires with `phase=enter_redirect_state`.

**Priority 2 — YAML: don't ship the autostart re-fire.** A sentinel
key like `bf__autostart_attempted` set on the first emit and gating
the emit on its absence would harden the room, but it's the wrong
primary fix. The engine cap is the right primary fix because (a) the
loop exists for any room with `on_error: <other-room>` where the
other room's on_enter can fail back, not just this one; (b) every
operator-facing failure should look the same — a clear HarnessError —
not whatever ad-hoc sentinel the room author thought to add. Apply
the sentinel as defence-in-depth only after the engine cap lands.

**Priority 3 — Host handler: make `worktreeCreate` idempotent.**
`internal/host/git_worktree.go:97-123` currently returns
`Result.Error` whenever `git worktree add` exits non-zero. For the
"directory already exists with the expected branch checked out" sub-
case the right behaviour is almost certainly "return Result.Data with
the existing path"; an operator's aborted prior run leaves exactly
that state and forcing them to `rm -rf` is hostile. The tradeoff is
that we'd hide a real "wrong branch checked out at this path"
configuration error — mitigate by probing `git worktree list
--porcelain` first, matching path AND branch, and only returning ok
when both match. This is a behaviour change worth its own proposal
but is independently useful for resumability.

## 4. Testing strategy gap

The flow-fixture model is structurally incapable of catching
host-failure-induced redirect loops: stubs are how flows stay fast
and deterministic, but the cost is that any code path predicated on a
host returning `Result.Error` is invisible to the fixture suite.

Propose a new dogfood smoke test under
`internal/orchestrator/dogfood_smoke_test.go` (or, if the orchestrator
package becomes crowded, a new `internal/integration/` package that
imports orchestrator + host without circular pain). The shape:

- `t.TempDir()` for the repo root; `exec.Command("git", "init")` plus
  one commit on `main` so worktree-add has a base to root at. This is
  the pattern absent from `internal/host/git_worktree_test.go` today;
  adding it is part of the fix.
- Real host registry: `host.NewRegistry()` then
  `host.RegisterBuiltins(reg)`. No stubs for `host.git_worktree` /
  `host.git`. The `cli_exec` runner stays at its real default.
- Drive `core__go_bugfix` from a seeded ticket and assert
  `out.NewState` reaches `reproducing_executing` within a 30s
  `context.WithTimeout`. A loop would either trip the cap (PASS once
  the engine fix lands — assert `HarnessError` instead) or hang the
  context (FAIL fast, no CI hang).
- A companion case pre-creates `.worktrees/bf-<id>/` on disk before
  the turn and re-runs: today this is the trigger for the regression,
  post-fix it should either succeed (priority 3) or surface a
  `HarnessError` (priority 1 only). Either is a valid contract.

The fast-tests mandate (per the user's memory file) holds: the test
budget should be sub-second per case — `git init` + one commit is
~50ms, the turn is ms. Use `t.Parallel()` on the sub-cases.

## 5. Adjacent gaps

- Every checkpoint room in `stories/bugfix/rooms/` has `on_error:
  idle` (see `testing.yaml:31,44`, `done.yaml:33`, `validating.yaml:27,39`,
  `reviewing.yaml:26`, `proposing.yaml:34`, `reproducing.yaml:49`,
  `implementing.yaml:46,53`). Any host failure inside those rooms now
  bounces to `bf.idle.on_enter`, which re-fires the create + auto-
  start. Same loop, different entry. The engine cap covers all of
  them; without it, this regression replays from any checkpoint room.
- Dev-story rooms (`main.yaml:30`, `oracle.yaml:46`,
  `workspace_manager.yaml:23`, `ticket_search.yaml:35,110`,
  `standup.yaml:44`) all use self-redirect `on_error`, which the
  existing guard at `orchestrator.go:1300-1306` handles correctly —
  not at risk.
- `internal/orchestrator/oncomplete.go` walks `on_complete` arcs but
  doesn't appear to cap recursion if a state's `on_complete` points
  back to a state whose `on_complete` re-triggers it; worth a closer
  look as a follow-up.
- `timeout.go` looked clean on a skim — timeouts are wall-clock-
  bounded by definition. No cap needed.
