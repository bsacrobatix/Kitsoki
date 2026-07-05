# The `@kitsoki` GitHub agent: dispatch architecture

This is the authoritative write-up of how a GitHub `@kitsoki` mention
becomes a real, honestly-reported kitsoki run. It covers the `ghagent`
package end to end: ingress, the honesty contract, per-job worktree
isolation, live-or-replay harness selection, and the drain loop that keeps
queued jobs moving. For standing up the GitHub App itself (manifest,
webhook URL, systemd service, live proof flow), see
[`github-app-setup.md`](github-app-setup.md).

Package: [`internal/ghagent`](../../internal/ghagent/doc.go). Serve command:
[`cmd/kitsoki/gh_agent_serve.go`](../../cmd/kitsoki/gh_agent_serve.go).

## The loop

```
@kitsoki mention (webhook or poll)
        │
        ▼
Dispatcher.claim ──▶ idempotent job row (jobs.GHJobStore, Postgres/SQLite)
        │
        ▼
router.go: label → story (bug/feature → stories/bugfix|dev-story; PR → stub)
        │
        ├── route has a realDispatchPlan (stories/bugfix only) ──▶ REAL DISPATCH
        │       • per-job worktree: .worktrees/gh-job-<id>
        │       • harness: replay (default) or live (explicit opt-in only)
        │       • drives the actual story through testrunner, real turns
        │
        └── no plan yet (stories/dev-story, all PR routes) ──▶ HONEST STUB
                • runs the beat fixture (host.agent.* stubbed)
                • RunResult.Stubbed = true, never "Done"
        │
        ▼
comment substrate: one rolling status comment, edited in place
        │
        ▼
drain loop (own ticker, independent of poll) re-queues stuck/queued jobs
```

## The honesty contract

`RunResult` (`internal/ghagent/dispatch.go`) carries `Stubbed bool`,
`StubReason string`, `Harness string`, and `RealHostCalls int`. The
comment-render branch in `Dispatcher.runJob` may only claim the run is
complete ("Done — …") when:

```go
realWorkProven := !result.Stubbed && (result.Harness == "" || result.RealHostCalls > 0)
```

i.e. never for a stubbed run, and for a real-dispatch run only once at
least one real `host.*` call actually executed (not just the beat
fixture's stub map). `TestDispatchStubbedRunNeverSaysDone` and
`TestDispatchRealDispatchOnlyDoneWithRealHostCalls`
(`internal/ghagent/dispatch_e2e_test.go`) pin this as a regression gate, not
just documentation. A stubbed run instead renders **"acknowledged —
pipeline not yet enabled for this route."**

Today: `stories/bugfix` issue routes report real work; `stories/dev-story`
issue routes and every PR route are honestly stubbed.

## Real dispatch: per-job worktree

Real dispatch (`internal/ghagent/realdispatch.go`) drives the actual
`stories/bugfix` machine — no beat-fixture `host.agent.*` stub map — inside
an isolated worktree at `.worktrees/gh-job-<id>` (per AGENTS.md's worktree
convention). Rather than inventing a new world key, the worktree identity is
threaded through the vocabulary `stories/bugfix/rooms/idle.yaml` already
declares and its Step-0w autostart guard already recognizes:

- `world.workdir` — set to the `.worktrees/`-prefixed job path
- `world.workspace_id`, `world.feature_branch`
- `world.workspace_prepared: true` — the flag that tells Step-0w's guard
  this is a deliberately-supplied identity, not stale seed data to clear

This also closes a residual gap in `internal/host/starlark_run.go`'s
Starlark-inspector root, which otherwise falls back to the process-global
`KITSOKI_APP_DIR` when `world.workdir` is unset — every real-dispatch job
always seeds `workdir`, so it never needs that fallback.

`cleanupJobWorktree` deletes the worktree on success. On failure it is kept
for a bounded retention window (`PruneStaleFailedWorktrees`, 7 days) for
post-mortem, mirroring dogfood-marathon practice.

Only `stories/bugfix` has a registered `realDispatchPlan` today, because
it's the only story with a recorded, arg-matched host cassette that walks
the *full* pipeline to `done`
(`stories/bugfix/cassettes/happy_human.cassette.yaml`, matched on
`{handler, phase}` — not prompt content, so substituting a job's real
`ticket_id`/thread/repo into `initial_world` doesn't break cassette
matching). `stories/dev-story` has no equivalent cassette yet, so its
issue route still runs the honest stub until one is recorded the same way
(a real Claude CLI run captured via `KITSOKI_CASSETTE_RECORD`).

## Harness selection: replay by default, live only on explicit opt-in

Real dispatch selects its harness via `resolveHarnessMode`
(`internal/ghagent/dispatch.go`):

```
explicit (Dispatcher.HarnessMode, an operator CLI flag)
  › KITSOKI_GHAGENT_HARNESS env var
  › default: "replay"
```

This deliberately does **not** reuse `cmd/kitsoki`'s `autoSelectHarness` —
that helper sniffs ambient credentials/`PATH` and silently goes live, which
is unsafe for an unattended dispatcher. An unrecognized mode value is a
hard configuration error (never silently downgraded or upgraded). Going
live requires an operator to deliberately set `Dispatcher.HarnessMode` or
`KITSOKI_GHAGENT_HARNESS=live` when standing up production `gh-agent
serve` — it is never inferred. This is the concrete implementation of the
project's hard rule: automated tests and CI default to cassettes/replay;
live is operator-invoked only.

## `KITSOKI_APP_DIR` concurrency

`app.Load`'s env-var expansion still reads `KITSOKI_APP_DIR` as a process
global (it resolves `${KITSOKI_APP_DIR}` references such as
`meta_modes[*].cwd`). The actually-racy window when two jobs dispatch
concurrently in one process is the short synchronous
`publishAppDirForTestrunner` → `app.Load` span inside
`RunFlows`/`RunIntents`/`RunFlowCoverage`
(`internal/testrunner/flows.go`). That span is now serialized behind a
package mutex (`appDirLoadMu` / `loadAppForRun`) — narrowly, not the whole
flow/turn run. Two jobs' `RunStorySession` calls briefly serialize while
loading their `app.yaml`, then run their turns fully concurrently, since
post-load prompt/script resolution goes through a per-orchestrator
`def.BaseDir`-scoped `render.AppRenderer`
(`internal/host/prompt_render.go`), not the global env var.
`TestConcurrentDispatch_NoAppDirCrossContamination`
(`internal/ghagent/dispatch_e2e_test.go`) pins this under `go test -race`.

## Drain: independent of the poll loop

`drainQueuedGHAgentJobs` re-queues stuck/queued jobs (retries, re-mentions
parked while the queue starved). It used to be reachable only from
`runGHAgentPollOnce`, so a webhook-only deployment (`--poll-interval=0`)
never drained at all. It now runs on its own ticker
(`runGHAgentDrainLoop`, `--drain-interval`, default 30s, `0` disables),
independent of `--poll-interval`, so a webhook-only deployment still drains.

## Ingress & comment substrate

`kitsoki gh-agent serve` runs a GitHub-App webhook listener
(`ghAgentWebhookHandler`) with HMAC-SHA256 signature verification
(`internal/ghagent/githubapp.VerifyWebhookSignature`) plus a poll fallback
over `internal/inbox/github.go` for environments without webhook
reachability. Both authenticate via an installation token minted from an
App JWT (`internal/ghagent/githubapp.AppTokenSource`) — see
[`github-app-setup.md`](github-app-setup.md) for the manifest/setup flow.
Every dispatched job gets exactly one rolling status comment, edited in
place (never a flood of new comments) as the run progresses.

A minimal run list/detail surface is served at `/runs` and `/run/<job-id>`
(plain HTML + `/api/runs` JSON) reading `jobs.GHJobStore` directly — enough
to link a run from the issue comment today. This is **not** the persistent,
multi-run, artifact-indexed service or the operator-drive viewer scoped by
[`trace-artifact-service.md`](../proposals/trace-artifact-service.md) and
[`gh-web-operator-viewer.md`](../proposals/gh-web-operator-viewer.md) — those
remain unbuilt.

## What's real vs. what's still Draft

| Surface | State |
|---|---|
| Webhook + poll ingress, HMAC verify, installation-token auth | **Real** |
| Idempotent job claim, rolling status comment | **Real** |
| Issue routes → `stories/bugfix` real pipeline, per-job worktree, replay-default harness | **Real** |
| Drain on its own ticker (webhook-only deployments) | **Real** |
| Issue routes → `stories/dev-story` | Honest stub (no recorded cassette yet) |
| PR routes (autopilot) | Honest stub — no `stories/pr-autopilot/` exists |
| Persistent multi-run trace/artifact service | Not built (`internal/runstatus/server` is single-session) |
| Web viewer artifact gallery + GitHub-OAuth operator drive | Not built |
| Tour/demo composite deck | Capture scaffolding exists (`tools/runstatus/tests/playwright/github-demo-*.spec.ts`); no finished deck checked in |

See [`kitsoki-github-agent.md`](../proposals/kitsoki-github-agent.md) for the
full epic and its remaining slices (#3-#6).
