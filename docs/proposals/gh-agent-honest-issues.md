# Runtime: Honest gh-agent issue dispatch

**Status:** Draft v1. Task 0 (interim honesty fix) shipped at `13d0fa91` — see
`internal/ghagent/dispatch.go` (`RunResult.Stubbed`/`StubReason`,
`Dispatcher.dispatchRouted`'s honest prose branch) and
`internal/ghagent/dispatch_e2e_test.go` (`TestDispatchStubbedRunNeverSaysDone`
+ updated fixtures). Task 1 (per-job `KITSOKI_APP_DIR`) shipped at `257b5c3c`
— see `internal/testrunner/flows.go`'s `appDirLoadMu`/`loadAppForRun`. Task 2
(real pipeline dispatch) has now landed for **stories/bugfix only** — see
`internal/ghagent/realdispatch.go`'s doc comment for the honest scope
correction (stories/dev-story has no recorded cassette yet and still runs the
task-0/1 stub path). Tasks 3-4 (drain-outside-poll-loop, adopt+document)
remain unstarted.
**Kind:**   runtime (ghagent)
**Epic:**   usable-kitsoki.md

## Why

`@kitsoki` on a `bug`/`feature`-labeled issue is supposed to run the real
bugfix/dev-story pipeline. Today it runs `stories/bugfix` against a **beat
fixture that stubs every agent call** — `internal/ghagent/testdata/bugfix.beat.yaml:24-30`
seeds `host.agent.task`/`ask`/`decide` to return `{ok: true, submitted:
{summary_title: "stub", ...}}"`, asserts the `start` intent advanced the
story to `reproducing`, and stops. `RunStorySession`
(`internal/ghagent/dispatch.go:207-244`) treats that as a completed run: it
sets `finalState = jobs.GHDone` and posts **"Done — `stories/bugfix` finished
in state `reproducing` (N turn(s))."** back to the issue
(`internal/ghagent/dispatch.go:223`). No LLM ran, no code changed, no bug was
fixed — and the public comment says the opposite. This is the single most
visible violation of the product's own moat (honesty over theater) and it
lands on the first issue anyone files.

Two structural blockers stand behind the honesty gap:

1. **Single-process concurrency ceiling.** `testrunner.RunFlows` publishes
   `KITSOKI_APP_DIR` as a process global (`internal/ghagent/doc.go:31-36`), so
   dispatching two issue mentions in one process cross-contaminates. The serve
   loop dispatches synchronously today specifically to avoid this — real
   pipeline runs (which take longer than a stub turn) need concurrency, so the
   global has to go before real dispatch can land.
2. **Retry drain only reachable from the poll loop.** `drainQueuedGHAgentJobs`
   (`cmd/kitsoki/gh_agent_serve.go:296-309`) — which re-queues stuck jobs and
   attaches retries — is only ever invoked from `runGHAgentPollOnce`
   (`cmd/kitsoki/gh_agent_serve.go:277-280`). The webhook handler
   (`ghAgentWebhookHandler`, `cmd/kitsoki/gh_agent_serve.go:393-...`) dispatches
   the incoming mention directly and never calls drain. A webhook-only
   deployment (poll disabled) strands every retry indefinitely.

This proposal is scoped to the honesty and concurrency gap in **issue**
dispatch specifically (R4 in the parent epic's gap table). It does not touch
PR autopilot, viewer auth, or multi-tenant install — those stay in the
existing [`kitsoki-github-agent.md`](kitsoki-github-agent.md) epic.

## What changes

1. **Interim honesty fix (ships first, independent of everything else).**
   While an issue route still runs the stub beat fixture, the dispatcher must
   never say "Done." `RunResult` gains a field the spawn function sets when
   the run was a stub, and the comment substrate renders **"acknowledged —
   pipeline not yet enabled for this route"** instead of the synthesized
   "Done — …" prose whenever that field is set. This requires no worktree
   plumbing and no concurrency fix — it is a one-line lie removed from a
   public surface and lands alone.
2. **Real pipeline dispatch.** Issue routes drive the actual `stories/bugfix`
   / `stories/dev-story` pipeline — no stub beat fixture, no fabricated
   `initial_world` shortcut — in a **per-job worktree**
   (`.worktrees/gh-job-<id>`, per AGENTS.md) with a **live-or-replay harness**
   selected the same way `kitsoki turn`/`web` already select harnesses (so CI
   keeps using cassettes and only an operator-invoked live run spends money).
   The stub beat fixture becomes the flow-test fixture that exercises dispatch
   plumbing, not the thing that runs in production.
3. **Per-job `KITSOKI_APP_DIR`.** Fix the process-global so each job's worktree
   gets its own app-dir scope, mirroring the fix already applied for
   `parallel-live-drivers-schema-bleed` in the driver-session case. Concurrent
   issue jobs stop cross-contaminating.
4. **Drain outside the poll loop.** Move `drainQueuedGHAgentJobs` so it also
   runs on a lightweight ticker independent of `runGHAgentPollOnce` (or is
   invoked from the webhook handler after every dispatch), so a webhook-only
   deployment with poll disabled still drains stuck/queued jobs.

## Impact

- **Code seams:** `internal/ghagent/dispatch.go` (`RunStorySession`,
  `Dispatcher.runJob` comment-prose branch), `internal/ghagent/doc.go`
  (concurrency note — becomes stale once fixed), `internal/ghagent/testdata/*.beat.yaml`
  (stub fixtures become flow-test-only), `cmd/kitsoki/gh_agent_serve.go`
  (`runGHAgentPollLoop`, `drainQueuedGHAgentJobs`, `ghAgentWebhookHandler`).
- **Vocabulary:** no new host calls; `RunResult` (internal/ghagent) gains a
  `Stubbed bool` (or equivalent) field consumed only by comment rendering.
- **Stories affected:** none directly — `stories/bugfix` and
  `stories/dev-story` run unchanged; what changes is how `ghagent` invokes
  them (real harness + per-job worktree instead of the beat-fixture shortcut).
- **Backward compat:** the interim fix (task 1) is a pure behavior
  improvement with no flag. The real-dispatch change (tasks 2-3) is gated
  behind **S1 (room workbench)** landing first — an issue job's "do the work"
  loop *is* a workbench turn, so this slice has nothing to drive until S1
  ships — and behind **S3 (context floor)** so a live issue-triggered pipeline
  run is affordable.
- **Docs on ship:** `docs/architecture/github-agent.md` (dispatch section —
  create if absent, per the parent epic's Impact), amend
  [`kitsoki-github-agent.md`](kitsoki-github-agent.md)'s "Real and well-built"
  claim once issue dispatch is actually real.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| host call | *(none new)* | — | Real dispatch reuses `host.git_worktree`, `host.agent.*`, `host.local` exactly as `stories/bugfix` already declares them — no new vocabulary, just a real harness behind it. |

**Correction (task 2 landed):** the `gh_job_worktree` world key originally
proposed here was never added — see task 2's vocabulary-correction note
below. The per-job worktree identity is threaded through the sanctioned
`workdir`/`workspace_id`/`feature_branch`/`workspace_prepared` keys
`stories/bugfix/rooms/idle.yaml` already declares, not a bespoke one.

## The model

```
issue mention ──▶ Dispatcher.claim ──▶ route (label→story)
                                          │
                          stub (today)  ◄─┴─► real (this proposal)
                          beat fixture       per-job worktree +
                          host.agent.* stub  live-or-replay harness
                          → "Done" (WRONG)   → real turns, real diff
                                          │
                                          ▼
                          comment substrate renders:
                            - "acknowledged — pipeline not yet
                              enabled" while stubbed (task 1)
                            - real summary/diff link once real (task 2)
```

The interim fix is a **rendering-time** decision (does this `RunResult` carry
evidence of real work?), not a routing decision — it needs no gate/decider,
just an honest predicate on `RunResult`.

## Decision recording

`RunResult` already flows into `jobs.GHJob.RunURL`/comment prose
(`internal/ghagent/dispatch.go:197-234`). Add `Stubbed bool` +
`StubReason string` to that struct; the comment-render step and the job's
trace record both carry the field, so a reviewer inspecting a run's trace can
see "this was a stub" without reading the source. Once real dispatch lands,
each job additionally records which harness (`live` / `replay`) ran and the
worktree path, so honesty is auditable end to end.

## Engine seams & invariants

- Load-time: none new — this is ghagent-package-local, not a story-YAML
  concept. No `app.yaml` invariant changes.
- Runtime invariant this proposal adds: **no gh-agent comment may contain
  "Done" unless the spawned run reports `Stubbed == false` and at least one
  real host call (not the stub map) executed.** Enforce this with a unit test
  on `Dispatcher.runJob`'s prose-selection branch, not just documentation.

## Backward compatibility / migration

The interim fix (task 1) changes comment text only — no flag, no migration,
ships immediately. Real dispatch (tasks 2-4) is new plumbing behind the
existing `RunStorySession` seam; the default route table
(`internal/ghagent/router.go`) is unchanged, so no story or fixture migration
is required. Existing e2e tests in `internal/ghagent/dispatch_e2e_test.go`
that assert `jobs.GHDone` continue to pass against the stub path until it is
replaced; each task below updates or replaces the fixtures it makes stale.

## Tasks

```
## 0. Interim honesty fix (ships first, standalone) — SHIPPED
- [x] 0.1 Add `Stubbed bool` / `StubReason string` to `RunResult`; set it true
      wherever a beat fixture with `host.agent.*` stub handlers is the spawn
      path (internal/ghagent/testdata/*.beat.yaml)
- [x] 0.2 `Dispatcher.runJob`'s prose branch (dispatch.go:220-226) renders
      "acknowledged — pipeline not yet enabled for this route" whenever
      `Stubbed == true`, never the synthesized "Done — …" string
- [x] 0.3 Unit test asserting no "Done" substring reaches the comment
      substrate for a stubbed run; existing e2e fixtures updated to assert
      the new prose

## 1. Per-job KITSOKI_APP_DIR — SHIPPED
- [x] 1.1 Scope KITSOKI_APP_DIR per job. Corrected from the proposal's
      original framing (there is no general per-session app-dir fix to
      mirror — the schema-bleed fix only solved `host.agent.*` prompt/schema
      resolution via a per-orchestrator `render.AppRenderer`). The actually
      racy window is the synchronous `publishAppDirForTestrunner` →
      `app.Load` span inside `RunFlows`/`RunIntents`/`RunFlowCoverage`; that
      span is now serialized behind a package mutex (`appDirLoadMu` /
      `loadAppForRun` in internal/testrunner/flows.go), narrowing the fix to
      exactly the unsafe window instead of serializing whole flow/turn runs.
      doc.go's stale "fixing that generally is out of scope" /
      process-global concurrency notes are corrected to describe the actual
      (narrowed) scope and the one residual gap (starlark_run.go's
      inspector-root env fallback when `world.workdir` is unset).
- [x] 1.2 Concurrency test: `TestConcurrentDispatch_NoAppDirCrossContamination`
      (internal/ghagent/dispatch_e2e_test.go) dispatches two different
      stories' `RunStorySession` calls concurrently in one process (`go test
      -race`) and asserts neither job's `RunURL`/turn count crossed streams.

## 2. Real pipeline dispatch — SHIPPED for stories/bugfix; stories/dev-story deferred
- [x] 2.1 Per-job worktree identity (`.worktrees/gh-job-<id>`) seeded at
      claim time via `internal/ghagent/realdispatch.go`'s `runRealDispatch` —
      corrected from the proposal's `gh_job_worktree` vocabulary entry
      (deleted; see below): the worktree is CREATED by the story's own
      sanctioned `iface.workspace.create` (rooms/idle.yaml Step 2) once
      `world.workdir`/`feature_branch`/`workspace_id`/`workspace_prepared` are
      seeded, not by a bespoke checkout here. `cleanupJobWorktree` deletes on
      success and keeps (bounded 7-day retention, `PruneStaleFailedWorktrees`)
      on failure, per open-question 2's lean.
- [x] 2.2 Live-or-replay harness selection (`resolveHarnessMode`,
      `Dispatcher.HarnessMode` / `KITSOKI_GHAGENT_HARNESS`) wired into
      `RunStorySession` via context, defaulting to `replay` always — NOT
      `cmd/kitsoki`'s `autoSelectHarness` (which sniffs ambient
      credentials/PATH and would silently go live in CI); `live` requires an
      explicit operator override, never auto-detected.
- [x] 2.3 `internal/ghagent/testdata/bugfix.beat.yaml` is now flow-test-only
      (dispatch-plumbing coverage via `runStubBeatFixture`, exercised
      directly by `TestRunStubBeatFixture_BugfixPlumbingStillValid`) —
      unreachable from `RunStorySession`'s production spawn path for the
      "bug" route, which now resolves through `realDispatchPlans["stories/
      bugfix"]` instead.
- [x] 2.4 `Stubbed` flips to `false` end-to-end for `stories/bugfix`
      (`TestRunStorySession_RealDispatch_BugfixReplay`); comment prose carries
      a real summary extracted from the run's `HostReturned` events (agent
      submit `summary_markdown` + `host.git` diff). The no-"Done"-without-
      real-work invariant (task 0's `TestDispatchStubbedRunNeverSaysDone`) is
      extended by `TestDispatchRealDispatchOnlyDoneWithRealHostCalls`: a
      real-dispatch result may only render as complete when `Stubbed ==
      false` AND `RealHostCalls > 0`.
- **Scope correction:** stories/dev-story has no recorded, arg-matched host
  cassette walking its full pipeline yet (only `inspect_cassette` fixtures for
  Starlark calls exist under `stories/dev-story/cassettes`), so its route
  still runs the task-0/1 stub path (`runStubBeatFixture`) — honestly
  reported via `Stubbed == true`, never "Done". Extending `realDispatchPlans`
  to dev-story needs its own recorded cassette, authored the same way
  `stories/bugfix/cassettes/happy_human.cassette.yaml` was (a real Claude CLI
  run captured via `KITSOKI_CASSETTE_RECORD`), which is follow-up work, not
  bundled into this slice.
- **Vocabulary correction:** the `gh_job_worktree` world key this proposal's
  Vocabulary changes table originally specified was never added. Seeding an
  un-declared world key would have bypassed `rooms/idle.yaml`'s Step-0w guard
  (which only recognizes `world.workspace_prepared` and a `.worktrees/`-
  prefixed `world.workdir` as legitimate caller-supplied identity); the
  per-job worktree path is instead threaded directly into the sanctioned
  `workdir`/`workspace_id`/`feature_branch`/`workspace_prepared` keys that
  path already declares. No new vocabulary was needed.

## 3. Drain outside the poll loop
- [ ] 3.1 Run `drainQueuedGHAgentJobs` on its own ticker independent of
      `runGHAgentPollLoop`, or invoke it after every webhook dispatch
- [ ] 3.2 Test: webhook-only deployment (poll disabled) still drains a
      stuck/queued job within one retry interval

## 4. Adopt + document
- [ ] 4.1 Correct the Status lines of the gh-agent proposals against actual
      build state (see below) and amend kitsoki-github-agent.md's "Real and
      well-built" list to include or exclude issue dispatch honestly
- [ ] 4.2 Update `docs/architecture/github-agent.md`; migrate shipped content
      to docs/ and trim/delete this proposal
```

### Status-line correction (checked against this worktree)

`internal/ghagent/doc.go`, `dispatch.go`, and `cmd/kitsoki/gh_agent_serve.go`
show the webhook/poll listener, HMAC verification, idempotent claim, rolling
comment substrate, and App-JWT→installation-token auth are implemented and
under test (`internal/ghagent/dispatch_e2e_test.go`). That means
[`gh-event-ingress.md`](gh-event-ingress.md) and
[`gh-job-dispatch.md`](gh-job-dispatch.md) — and the parent epic
[`kitsoki-github-agent.md`](kitsoki-github-agent.md) itself — currently carry
a **"Nothing implemented yet" Status line that is false**. Task 4.1 corrects
those three files' Status lines to describe what is actually live (ingress +
dispatch substrate, sans real issue-pipeline execution) rather than
re-litigating their design. `pr-autopilot-story.md`,
`trace-artifact-service.md`, `gh-web-operator-viewer.md`, and
`kitsoki-github-demo.md` were not deeply re-audited here — task 4.1 includes a
pass over those four's Status lines too, correcting any found stale, since a
demo/webviewer test surface already exists
(`tools/runstatus/tests/playwright/github-demo-*.spec.ts`,
`tools/runstatus/src/tour/github-demo-manifest.ts`) that the current "Nothing
implemented yet" lines don't account for.

## Verification

- `kitsoki turn` / `RunStorySession` unit tests exercise the `Stubbed` branch
  without any LLM (task 0.3).
- Flow fixtures cover both the stub-fixture path (still exists as a
  dispatch-plumbing test) and, once task 2 lands, a replay-harness real-story
  fixture recorded from an actual bugfix run.
- Multi-system test for task 1.2 (concurrent jobs, shared process) — I/O and
  concurrency in scope per CLAUDE.md.
- No test in this proposal requires a live LLM; the live-harness path (2.2)
  is operator-invoked only, same as `kitsoki web --harness live` today.

## Open questions

1. Should the interim honesty fix (task 0) delete the stub beat fixture's
   ability to reach production at all, or just neuter its prose? *Lean:
   neuter now (task 0), delete the reachability path once task 2 replaces it
   (task 2.3) — keeps task 0 a same-day, low-risk fix.*
2. Per-job worktree cleanup policy on failure — keep for post-mortem, or
   delete and rely on the trace + incident record? *Lean: keep failed-job
   worktrees for a bounded retention window (mirrors dogfood-marathon
   practice), delete on success.*

## Non-goals

- PR autopilot, viewer auth, multi-tenant install, prod GitHub App — these
  remain in [`kitsoki-github-agent.md`](kitsoki-github-agent.md) slices 3, 5,
  and the round-1 deferred items; not re-scoped here.
- Collaborator-membership driving authority (still owner-only per the parent
  epic's shared decision #1) — unaffected by this slice.
- Cost-meter accuracy for live issue-triggered runs — existing P0 work
  (R10 in the parent epic), not this slice.
