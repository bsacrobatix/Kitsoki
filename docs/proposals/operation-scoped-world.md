# Runtime: Operation-scoped world

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   standalone

## Why

Kitsoki already has a clean separation for off-path conversation: the
off-path runtime states that free-form chat does not mutate world or state
(`internal/orchestrator/offpath.go:4`), and the conversation lane diverts
before `machine.Turn` so the room and world remain unchanged
(`internal/orchestrator/offramp_conversation.go:4`).

That guarantee does not cover deterministic operation rooms. Today `set` and
`bind` write directly into durable session world (`docs/stories/state-machine.md:435`,
`docs/stories/state-machine.md:440`), and world is defined as the typed schema
for everything that survives a turn (`docs/stories/state-machine.md:553`).
That means task-local inputs, intermediate host results, and failed attempts
can outlive the task when the operator presses `back`, `quit`, retries through
an error room, or loops through a correction path.

`stories/git-ops/rooms/sync_main.yaml` is the current concrete shape: entering
`sync_main` stores operation inputs and `sync_result` in world
(`stories/git-ops/rooms/sync_main.yaml:12`, `stories/git-ops/rooms/sync_main.yaml:149`),
but `back` returns to the hub without discarding those keys
(`stories/git-ops/rooms/sync_main.yaml:158`). The hub may hide the residue,
but it remains available to later guards, prompts, room context, and trace
replay. Story authors should not have to hand-clear every scratch key on every
abandon path.

## What changes

Add an engine-owned operation scope: a temporary overlay for task-local world
changes. Operation rooms write intermediate `set` and `bind` results into the
overlay. Durable world changes only when the operation commits selected values.
Leaving the operation through `back`, `quit`, `on_error`, a guard fallback, or a
loop abandons the overlay by default.

If a task should be resumable, the story promotes the overlay to an explicit
draft handle. Drafts are visible, named artifacts with `continue` and `discard`
actions; they are not invisible residue in normal world.

The core invariant becomes:

> Durable world contains committed story facts. Operation-local state is either
> committed intentionally, abandoned automatically, or persisted explicitly as a
> draft.

This is the runtime model, not an opt-in feature flag. Directly writing task
scratch state into durable world is the legacy behavior to retire. Implementation
is complete only when every story room that models an abandonable task uses
operation scope, an explicit draft, or a deliberately committed world write.

## Impact

- **Code seams:** `internal/machine` effect application, `internal/orchestrator`
  turn persistence and replay, `internal/store` event shapes for operation
  begin/commit/abandon, loader validation in `internal/app`.
- **Vocabulary:** new operation-scope declaration on states or effects, plus
  commit/discard/draft mechanics described below.
- **Stories affected:** repo-wide migration. `stories/git-ops` should be the
  first migration because it has visible multi-step operation rooms:
  `sync_main`, `pull`, `conflict`, `staging`, `commit`, `cleanup`, and
  checkpoint/restore. The proposal is not complete until every story with
  abandonable task state has migrated.
- **Compatibility:** no compatibility mode for the old scratch-world pattern.
  The implementation must update stories, fixtures, and docs together so local
  `main` never carries both models as supported authoring paths.
- **Docs on ship:** `docs/stories/state-machine.md`,
  `docs/architecture/room-workbench.md`, and tracing docs for operation events.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| state field | `operation:` | `{scope, commit?, draft?}` | Declares an operation-local world overlay for an abandonable task room. |
| effect | `commit_operation` | `{world: {key: expr}, clear?: bool}` | Copies selected overlay values into durable world and closes the operation. |
| effect | `persist_draft` | `{id, title?, world?: [keys]}` | Saves the overlay under an explicit draft handle. |
| effect | `discard_operation` | `{reason?}` | Explicit discard; implicit on uncommitted exit. |
| trace event | `operation.started` | `{operation_id,state,parent_world_hash}` | Records the overlay boundary. |
| trace event | `operation.committed` | `{operation_id,world_patch,draft_id?}` | Records the durable patch. |
| trace event | `operation.abandoned` | `{operation_id,reason,discarded_patch}` | Records what did not enter durable world. |

The final surface may use better names, but the contract should stay stable:
operation writes are not durable until a commit effect says so.

## The model

```
committed world
      |
      v
operation room enters
      |
      v
overlay world = committed world + task-local set/bind results
      |
      +-- accept/success --> commit selected fields --> durable world
      |
      +-- continue later --> persist explicit draft handle
      |
      +-- back/quit/error --> abandon overlay, durable world unchanged
```

Interpretive work stays where it already belongs: an LLM or human may decide
whether a task is accepted, should be saved as a draft, or should be abandoned.
The overlay mechanics are deterministic and replayable.

## Decision recording

This proposal records operation boundaries, not new interpretive decisions.
The trace must show:

- the state and turn that opened an operation;
- every operation-local world patch;
- the committed durable patch, if any;
- the abandoned patch, if the operator exits without committing;
- the draft id, if the story intentionally persists a resumable operation.

If a human or LLM chooses between commit/save/abandon, that decision remains a
normal gate/agent decision event. Operation events record the deterministic
effect of that decision.

## Engine seams & invariants

The implementation should preserve these invariants:

1. A turn sees one readable world: committed world overlaid with the active
   operation patch.
2. `set`, `increment`, and `bind` inside an active operation write to the
   overlay unless the effect explicitly targets durable world.
3. `view`, guards, and host `with:` templates in the operation read the overlaid
   world, so authors do not have to write parallel `operation.foo` references.
4. Exiting an operation state without `commit_operation` or `persist_draft`
   discards the overlay automatically.
5. Discard is deterministic, trace-visible, and does not require story-authored
   cleanup arcs.
6. Off-path and conversation-lane guarantees remain unchanged: those turns still
   do not mutate state or world.

Loader validation should catch only structural mistakes at first:

- `commit_operation` outside an operation state;
- `persist_draft` without a stable draft id expression;
- operation state with background jobs whose completion would write into a
  discarded overlay unless the story declares an explicit policy.

## Migration

- abandonable task rooms use operation scope;
- resumable abandoned work is represented by explicit draft records;
- durable world writes from task rooms are reserved for committed facts,
  telemetry, and intentionally global state;
- loader validation prevents direct scratch-world writes from entering the repo.

Migration should be mechanical for one class of rooms:

1. Add `operation:` to a room whose world writes are task-local.
2. Remove hand-authored cleanup from `back`, `quit`, and error arcs.
3. Add `commit_operation` only on the success transition that should publish
   durable facts.
4. Add `persist_draft` only when continuation is a product requirement.

`stories/git-ops/rooms/sync_main.yaml` is the first adoption target. It should
show that local-ahead/prepared/conflict results do not leak back into the hub
unless the operator commits or saves a draft. After that, migrate the remaining
GitOps operation rooms, then audit the rest of `stories/` for the same pattern.

## Tasks

### 1. Engine

- [ ] 1.1 Add an internal operation overlay to the journey/world model.
- [ ] 1.2 Teach effect application to write `set`, `increment`, and `bind` into
      the overlay while an operation is active.
- [ ] 1.3 Add deterministic `commit_operation`, `persist_draft`, and
      `discard_operation` effect handling.
- [ ] 1.4 Add load-time validation and clear author errors for malformed
      operation scopes.

### 2. Tracing and replay

- [ ] 2.1 Record operation start, local patches, commit, draft persist, and
      abandon events.
- [ ] 2.2 Ensure replay reconstructs committed world exactly and does not apply
      abandoned overlays.
- [ ] 2.3 Add trace digest output that makes abandoned operation state visible
      during debugging.

### 3. Adoption

- [ ] 3.1 Migrate `stories/git-ops/rooms/sync_main.yaml` onto operation scope.
- [ ] 3.2 Add a GitOps flow proving `sync_main -> back` leaves no `sync_*`
      residue in durable world.
- [ ] 3.3 Add a draft-preservation example for a genuinely resumable task.
- [ ] 3.4 Migrate the remaining GitOps operation rooms before calling the
      runtime substrate shipped.

### 4. Document and retire

- [ ] 4.1 Update `docs/stories/state-machine.md` with committed world vs.
      operation overlay semantics.
- [ ] 4.2 Update architecture/tracing docs with operation event contracts.
- [ ] 4.3 Add authoring guidance that durable world is for committed facts, not
      abandonable task scratch state.

### 5. Repo-wide migration and old-model retirement

- [ ] 5.1 Audit `stories/` for abandonable task rooms that write scratch inputs,
      host results, partial artifacts, or retry state directly to durable world.
- [ ] 5.2 Migrate every matching room to operation scope, explicit drafts, or
      intentionally committed durable writes.
- [ ] 5.3 Add load-time validation that rejects new direct scratch-world writes
      in abandonable task rooms.
- [ ] 5.4 Remove docs and authoring examples that describe scratch-world cleanup
      as an acceptable pattern.
- [ ] 5.5 Move shipped guidance into narrative docs and delete this proposal.

## Verification

All default verification must be no-LLM:

- unit tests around world overlay read/write/commit/discard;
- replay tests showing abandoned overlays do not affect final world;
- flow fixtures for GitOps `sync_main -> back`, `sync_main -> retry`, and
  `sync_main -> explicit draft`;
- a negative loader test for malformed operation declarations.

No live LLM is needed for the runtime substrate. Any later story that combines
operation drafts with an LLM accept/reject gate should use host cassettes.

## Open questions

1. Should the first syntax live on `state.operation` or on individual effects?
   *Lean: state-level*, because abandonment is about the task room boundary, not
   one host call.
2. Should committed-world writes be possible from inside an operation?
   *Lean: yes, but explicit only*, for telemetry/cost-like keys that are
   intentionally global.
3. Are drafts stored in world, chat store, artifact store, or a new draft table?
   *Lean: store-level draft records*, with world holding only explicit draft ids
   when a story wants them visible.

## Non-goals

- Replacing off-path or room conversation lanes.
- Leaving the legacy scratch-world pattern as a supported authoring option.
- Rolling back filesystem side effects made by host calls. The proposal scopes
  world integrity; filesystem transactions need a separate sandbox/checkpoint
  design.
- Adding live LLM tests for abandon semantics.
