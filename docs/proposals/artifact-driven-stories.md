# Epic: persistent artifact jobs (durable runs, shareable artifacts)

**Status:** Draft v2. Consolidates the earlier artifact-driven story lifecycle
and the trace/artifact service into one implementation epic. Nothing in this
unified shape is implemented yet, though it deliberately builds on shipped
background jobs, operation handles, journal artifacts, and runstatus surfaces.
**Kind:**   epic
**Slices:** 6 (0/6 shipped)

## Why

The user-facing unit we keep wanting is broader than "a workspace" and broader
than "a trace": it is a **job of work** that can run for a while, survive the
operator leaving, be reopened from another session, and be shared with the
artifacts that explain what happened.

Today those pieces are split:

1. Background jobs and operation handles persist current state for one session,
   but they do not create a durable, shareable run surface with a complete
   artifact index.
2. The dev-story design pipeline writes useful files into
   `docs/proposals/.workspace/<slug>/`, but the workspace is hand-rolled,
   one-shot, and hard to rejoin after a new session.
3. The trace/artifact service proposal solves stable run URLs for GitHub jobs,
   but it is scoped as a GitHub-agent slice rather than the substrate every
   artifact-producing story can use.
4. TUI and web surfaces can show current operations and open terminal artifact
   handles, but there is no durable "these are my artifact jobs, resume/share
   one" console.

For dev-story work this means a design, implementation, QA pass, or generated
demo can produce the right files and still be awkward to pick back up tomorrow
or hand to someone else. This epic makes the durable thing explicit.

## What changes

Kitsoki gains a first-class **artifact job**: a durable record tying together
the job identity, session/run, trace, artifact handles, optional workspace
instance, terminal summary, share state, and cleanup policy.

Once every slice ships:

- A long-running story job has a stable `job_id` and `run_url`. Reopening the
  job from another TUI/web session attaches to the existing run record instead
  of minting a new scratch context.
- The trace JSONL remains immutable on disk, while a queryable run/artifact
  index lets web/TUI list jobs, stream live runs, replay completed runs, and
  serve artifacts by handle.
- Artifact-driven stories declare their workspace artifacts in `artifacts:`.
  The workspace is a keyed instance that can be discovered, rejoined, revised,
  shared, published, archived, or reclaimed.
- Dev-story uses that substrate for design and delivery jobs rather than
  bespoke workspace scripts. The artifacts a job emits are the handoff surface:
  PRDs, proposals, plans, QA reports, screenshots, decks, videos, and terminal
  completion-state JSON.
- A unified operator console in TUI and web lists artifact jobs across sessions,
  shows status and artifacts, resumes work, opens the run URL, promotes a draft
  to a shared workspace, publishes canonical docs, and warns on reclaimable
  archives.
- GitHub-agent jobs consume the same run/artifact substrate. The GitHub epic
  remains the front door and auth/comment loop; this epic owns the durable run
  and artifact service it links to.

## Impact

- **Spans:** runtime (job registry, workspace instances, promotion/disposition),
  tracing (run/artifact store and replay), story (dev-story adoption), and
  TUI/web (job console, artifact gallery, resume/share controls).
- **Net surface:** a shared artifact-job registry, local and hosted storage
  backends, stable `/run/<job-id>` URLs, an artifact index over existing
  `artifact.emitted` events, `artifacts:` story declarations, `iface.instance.*`
  host calls, and one operator console.
- **Docs on ship:** `docs/stories/artifact-driven-stories.md`,
  `docs/stories/dev-story-onboarding.md`, `docs/stories/state-machine.md`,
  `docs/stories/background-jobs/runtime.md`,
  `docs/tracing/trace-artifact-service.md`, and `docs/tui/web-ui.md`. As
  slices ship, detail moves there and the proposal files are trimmed/deleted per
  lifecycle guidance.

## Slices

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | Artifact job registry | runtime | Durable job/run identity, session attachment, local/hosted stores, restart semantics, and stable run URL minting | - | Draft | [`artifact-job-registry.md`](artifact-job-registry.md) |
| 2 | Trace + artifact service | tracing | Persist/index many runs by `job_id`, replay completed traces, and serve artifacts by handle for local and hosted jobs | 1 | Draft | [`trace-artifact-service.md`](trace-artifact-service.md) |
| 3 | Workspace instances + resume | runtime | `artifacts:` story spec, keyed workspaces, discover/rejoin, back-step, and update-mode stale handling | 1 | Draft | [`artifact-instances.md`](artifact-instances.md) |
| 4 | Share/publish lifecycle | runtime | Promote draft workspaces, publish canonical docs, apply disposition, and enumerate/reclaim archives | 1, 3 | Draft | [`artifact-publish-lifecycle.md`](artifact-publish-lifecycle.md) |
| 5 | Dev-story adoption | story | Make dev-story design/delivery jobs use artifact jobs, persisted workspaces, terminal artifacts, and no-LLM resume/share flows | 1-4 | Draft | [`dev-story-artifact-jobs.md`](dev-story-artifact-jobs.md) |
| 6 | Artifact job console | tui | TUI/web list, resume, share, publish, artifact gallery, run URL, and archive cleanup controls | 1-5 | Draft | [`artifact-instance-console.md`](artifact-instance-console.md) |

## Sequencing

```
#1 job registry
    |--> #2 trace + artifact service
    `--> #3 workspace instances --> #4 share/publish lifecycle
                 `------------------------------\
#2 trace service -------------------------------+--> #5 dev-story adoption
                                                  `--> #6 console
```

#1 creates the identity and attachment model every other slice needs. #2 can
land once jobs have stable IDs and run bindings. #3 can also land after #1,
because workspaces need a job/instance handle but do not need the hosted run
server. #4 builds on #3. #5 is the dogfood consumer that proves the pieces work
together in dev-story. #6 can ship in two cuts: list/resume after #1/#3, then
artifact gallery/share/publish/archive after #2/#4/#5.

## Shared decisions

1. **Artifact job is a product identity, not a replacement for
   `internal/jobs`.** The existing background-job scheduler still owns
   goroutine lifecycle, clarification, and per-session fan-out. The artifact-job
   registry is the durable user-facing record that can point at a background
   job, an operation handle, a dev-story workflow, or a GitHub dispatch.
2. **One job gets one stable run surface.** A job has one `job_id`, one primary
   `session_id`, and one `run_url`. Reopening attaches to that record. If the
   old process died, the job is marked interrupted with a resume action rather
   than pretending the handler goroutine survived.
3. **Local first, hosted compatible.** Local dev-story usage must not require
   Postgres. The local backend stores registry/index rows beside the session
   store and blobs on the filesystem. Hosted/GitHub usage uses Postgres for the
   same logical tables and filesystem blobs under a configured root. Object
   storage is later.
4. **Trace bytes stay immutable.** The run/artifact index is derived from the
   trace JSONL and artifact events; it can be rebuilt from disk. The index never
   rewrites trace bytes or becomes the source of truth for replay.
5. **Workspace artifacts and run artifacts are different views of the same
   work.** Workspace instances hold editable, story-declared files. Run
   artifacts are journaled handles produced during execution. The console shows
   both, but the engine keeps their lifecycle rules separate.
6. **Share and publish are independent.** Sharing promotes the full draft
   workspace for collaboration; publishing emits the canonical doc/report.
   Either can happen without the other, and disposition happens only after a
   successful share or publish.
7. **No real LLM or real GitHub in tests.** Regression coverage uses flow
   fixtures, cassettes, seeded workspaces, tiny media fixtures, and throwaway
   local databases. Live model or GitHub runs remain explicit manual gates.

## Cross-cutting open questions

1. **Shared workspace location.** Committed in-repo path, separate shared tree,
   or transport-backed store. *Lean: committed/shared filesystem path for v1;
   remote sharing comes after the local model is proven.*
2. **Retention policy ownership.** Per-story `artifacts.retention`, global
   defaults, or both. *Lean: story policy with global caps, surfaced as warnings
   and never auto-delete without operator confirmation.*
3. **Redaction boundary.** Store-time redaction for public links vs. only storing
   sanitized runs. *Lean: v1 stores local/private and GitHub-auth-gated runs;
   public unauthenticated sharing requires a follow-up redaction gate.*
4. **Interrupted active jobs.** Resume by replaying a story-level operation vs.
   terminal `interrupted` with a manual "start follow-up" action. *Lean: mark
   interrupted deterministically; individual stories can offer a resume action
   when they know how to continue safely.*

## Non-goals

- **Not the artifact file format.** [`artifact-format.md`](artifact-format.md)
  owns schema validation and lossless markdown/data round-trips.
- **Not GitHub ingress/auth/commenting.** The GitHub-agent epic owns mentions,
  App/OAuth auth, PR automation, and thread comments. This epic owns the
  durable run/artifact surface that GitHub links to.
- **Not real-time co-editing.** Collaboration is over time via shared drafts and
  rejoin, not simultaneous multi-cursor editing.
- **No object storage abstraction in v1.** Filesystem blobs plus SQLite/Postgres
  indexes are enough for local and hosted round one.
- **No hidden autonomous retries.** Retry/resume semantics belong in the story
  and trace, not in a registry daemon that silently reruns work.
