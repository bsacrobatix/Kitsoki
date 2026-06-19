# Runtime: MCP authoring tools — read / write / validate / graph / test

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   [mcp-studio.md](mcp-studio.md) (slice 6)

## Why

An external agent authoring a story today edits YAML, then shells out to
discover whether it broke anything — and the feedback is scattered: `app.Load`
errors come from a `kitsoki turn` invocation, the room graph from the web
editor's RPC, flow results from `kitsoki test flows`. There's no single,
**no-LLM** authoring surface that turns "I wrote this YAML" into "here are the
load-time invariants you violated, here's the room graph, here's which flow
fixtures still pass." The pieces all exist as Go APIs — they're just not exposed
to the agent doing the editing.

This slice gives the MCP studio (slice 5) the **direct file primitives** an
author needs: read, write, validate, inspect the graph, and run the deterministic
flow gate — all wrapping shipped functions, all free of LLM cost.

## What changes

**One sentence:** register five `story.*` tools on the studio server that wrap
`app.Load`/`LoadBytes` (validate, `loader.go:80`), `graph.RoomList`/`Detail`/
`OracleContracts` (the read-only story graph already behind the web editor,
`internal/app/graph/`), and `testrunner.RunFlows` (the no-LLM flow harness,
`internal/testrunner/flows.go`), plus plain workspace-scoped file read/write.

## Impact

- **Code seams:** new tools in `internal/mcp/studio/` against the workspace
  handle (slice 5). Wraps `app.Load` (`loader.go:80`), `app.LoadBytes`
  (`loader.go:678`), `graph.RoomList` (`graph/graph.go:45`), `graph.Detail`,
  `graph.OracleContracts`, `testrunner.RunFlows` (`flows.go`). The web editor's
  dispatch (`internal/runstatus/server/editor.go:73-136`) is the prior art — same
  graph functions, exposed as MCP tools instead of JSON-RPC.
- **Vocabulary:** tool namespace only (table below). No engine vocabulary.
- **Stories affected:** none — read/validate/test are non-mutating to the engine;
  `story.write` mutates files the agent already owns.
- **Backward compat:** additive.
- **Docs on ship:** `docs/architecture/mcp-studio.md` (the authoring tools);
  cross-link `docs/embedded/app-schema.md` (the YAML reference the agent validates
  against).

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| tool | `story.read` | `{path} → {content}` | workspace-scoped file read (rooms/*.yaml, prompts, schemas, flows) |
| tool | `story.write` | `{path, content} → {written, validation}` | write then **auto-validate**; returns the same `ValidationError` list as `story.validate` |
| tool | `story.validate` | `{dir?} → {ok, errors[]}` | `app.Load(dir)` → `[]ValidationError{File, Line, Column, Message}` (`loader.go:26`) |
| tool | `story.graph` | `{dir?, room?} → {rooms[] \| detail \| oracles[]}` | `graph.RoomList` (BFS) / `Detail` (on_enter, world_keys, intents, transitions, view) / `OracleContracts` |
| tool | `story.test` | `{dir?, flows?} → {report}` | `testrunner.RunFlows` (no-LLM; `--recording`/`--host-cassette` honored) → per-fixture pass/fail |

`dir`/`path` default to the bound workspace handle (slice 5, shared decision 1).

## The model

Every tool here is **deterministic and LLM-free** — exactly the half of
authoring that should never cost a token (shared decision 3). The agent is the
author; these tools are its compiler, linter, and test runner:

```
story.write rooms/debug.yaml ──▶ app.Load(workspace) ──▶ ValidationError[]   (file:line:col)
                                                       └─▶ {ok:false, errors:[{File:"rooms/debug.yaml", Line:14, Message:"intent 'go_fix' not declared"}]}

story.test                  ──▶ testrunner.RunFlows ──▶ {fixtures:[{name, pass, ...}]}   (no LLM, replay/cassette)
```

`story.validate` surfaces the full load-time invariant set the loader already
enforces (`validateDef`, `loader.go:733`): undeclared intents, dangling state
targets, unknown host calls, missing agents/providers, off-ramp placement,
expression compile errors, meta-mode rules, Starlark script existence (the
ten labelled passes at `loader.go:741-900`). The agent gets the *same* errors a
human would on `kitsoki run`, at edit time, as structured data.

## Decision recording

None — these tools make no interpretive decision and record no trace events.
`story.test` runs the existing flow harness, which records its own trace if
`--trace-out` is set; the tool surfaces the pass/fail report, not new events.

## Engine seams & invariants

- **Validation is the existing loader, unwrapped.** `story.validate` returns
  `app.Load`'s `ValidationError` slice directly (`loader.go:26-50`) — no new
  validation logic, so the MCP surface can never disagree with `kitsoki run`.
- **Graph is the existing pure functions.** `graph.RoomList`/`Detail`/
  `OracleContracts` are already I/O-free and already feed the web editor
  (`editor.go:73`); reusing them means the agent's graph view and the human's
  `/editor` view are the same computation.
- **`story.write` validates on every write** so a malformed edit is caught
  immediately, not on the next `session.new`. Writes are confined to the
  workspace handle's directory (path-escape rejected) — principle of least
  surprise: an authoring tool can't write outside the story it's authoring.
- **No load-time invariant is added here** — this slice consumes them.

## Backward compatibility / migration

Additive. The flow-fixture format (`FlowFixture`, `flows.go:55`) and cassette
shapes are unchanged; `story.test` is `kitsoki test flows` reached over MCP.

## Tasks

```
## 1. Engine
- [ ] 1.1 story.read / story.write (workspace-scoped, path-escape guard)
- [ ] 1.2 story.validate: wrap app.Load → ValidationError[] as structured tool result
- [ ] 1.3 story.graph: wrap graph.RoomList / Detail / OracleContracts (mode by params)
- [ ] 1.4 story.test: wrap testrunner.RunFlows (honor recording/host-cassette; no LLM)
- [ ] 1.5 story.write auto-validates and returns the validation in one round-trip

## 2. Verification
- [ ] 2.1 Validate a known-bad story (undeclared intent) → exact File:Line:Message
- [ ] 2.2 Validate a known-good story (stories/bugfix) → {ok:true}
- [ ] 2.3 Graph: RoomList of bugfix matches the web editor's runstatus.editor.rooms output
- [ ] 2.4 Test: RunFlows over stories/bugfix/flows reproduces `kitsoki test flows` (no LLM)
- [ ] 2.5 Path-escape: story.write ../../etc/x is rejected

## 3. Adopt + document
- [ ] 3.1 Author one real room change end-to-end through the tools (write → validate → test)
- [ ] 3.2 docs/architecture/mcp-studio.md authoring section; update the epic slice row
```

## Verification

Entirely no-LLM: validate against a known-bad and a known-good story, diff the
graph output against the web editor's `runstatus.editor.rooms`
(`editor.go:73`), and run `story.test` over `stories/bugfix/flows/*` confirming
it matches `kitsoki test flows`. The whole slice is deterministic by
construction.

## Open questions

1. **Delegate-to-meta-mode tool?** The epic chose direct primitives; kitsoki's
   meta-mode story-author (`metamode.Controller.Send`) already edits + commits.
   *Lean: ship direct primitives only (the external LLM is the author); a
   `story.author` delegate that drives meta-mode is a clean future addition if
   agents want a "write me a whole room" macro — note it, don't build it.*
2. **Should `story.write` stage a diff for human review, or write straight?**
   *Lean: write straight (the agent owns the workspace); the human reviews via
   git / `/open` / the IDE — surfacing a diff is the
   [`review-externally`](review-externally.md) concern, not this tool's.*

## Non-goals

- A meta-mode delegate tool (open Q1).
- VCS / commit handling — the agent or human owns git.
- Oracle-output cassette authoring (converse/decide/task bodies) —
  [`oracle-contract-eval.md`](oracle-contract-eval.md).
- Schema-validating individual artifacts (that's [`artifact-format.md`](artifact-format.md)).
