# Runtime: The room workbench primitive

**Status:** Shipped (S1 of the `usable-kitsoki.md` epic). All tasks below
are landed on `s1/room-workbench`; full design, model, desugaring contract,
invariants, and the deterministic-seam rule now live in
[`docs/architecture/room-workbench.md`](../architecture/room-workbench.md)
(narrative home going forward — this file is a shipped-record summary only).
**Follow-up not covered by this proposal:** S2's never-silent runtime
(`docs/proposals/never-silent-runtime.md`) lands a `near_miss` routing
verdict with a `nearMissWorkbenchEnabled` stub that still needs to be wired
to this workbench's entry point (the synthesized `<room>_capture` intent /
`free_form_fallback`) so a near-miss confidence band routes into the
governed floor rather than the nearest authored intent — tracked as its own
task, not part of S1.
**Kind:**   runtime (story spillover — see Impact)
**Epic:**   usable-kitsoki.md

**Supersedes** [`ad-hoc-workbench.md`](ad-hoc-workbench.md) (deleted by this
proposal). That epic's Slice 1 open question — "is the free-form landing a
`mode: conversational` room, a full-tool `host.agent.task` room, or just
`main` + off-ramp?" — is answered here: **a full-tool task room, governed by
WS** (effect taxonomy + toolbox enforcement + sandbox), not a new permission
system. `ad-hoc-workbench.md` Slices 2–4 (the ambient miner, `/mine`, blank
project roots) are a separate future proposal — see Non-goals.

## Why (summary)

`stories/dev-story/rooms/landing.yaml` was a governed, work-capable
free-form floor built entirely by hand: `write_mode: read_only`, an
`agent_off_ramp`, an `on_enter host.agent.task` dispatch, and a free-text
capture arc — ~150 lines of YAML any second story wanting the same floor
would have had to copy by hand, using the legacy `tools:`/`bash_profile:`/
`external_side_effect:` agent vocabulary instead of the shipped WS
`toolboxes:`/`effect:` vocabulary. `workbench:` is that recipe generalized
into one declarative state block, desugared at load time into those same
four already-shipped primitives. Full rationale, the model diagram, the
desugaring contract, load-time invariants, and the deterministic-seam rule
(route-outs are always a hand-authored `on:` arc's `set:`/`emit_intent:`
effects, never an agent-constructed call) are in
[`docs/architecture/room-workbench.md`](../architecture/room-workbench.md).

## Impact (as shipped)

- **Code:** `internal/app/types.go` (`WorkbenchDecl`, `State.Workbench`),
  `internal/app/workbench.go` (`expandWorkbenches` desugaring pass + the
  `plan: true` contract check), `internal/app/imports_rewriter.go` (rewrites
  `Workbench.Agent`/`OffRampAgent` on import-fold), `internal/host/agents.go`
  (`enforceToolbox` — unchanged, the synthesized dispatch rides it),
  `internal/orchestrator/offpath.go` (`maybeOffRamp` — unchanged),
  `internal/orchestrator/workbench_gate_signal.go` (the S6
  `usable_kitsoki_gate` producer signal on `turn.end`).
- **Stories:** `stories/dev-story/rooms/landing.yaml` migrated onto the
  primitive (Task 3.1); `stories/pets-dev/app.yaml`, `stories/slidey-dev/app.yaml`,
  and `.kitsoki/stories/kitsoki-dev/app.yaml` now declare
  `routing.free_form_fallback` explicitly (the loader's auto-detect only
  ever matched the bare, un-aliased `work` name, which the migration renamed
  to `landing_capture`).
- **Backward compat:** fully additive — a state with no `workbench:` block
  is byte-for-byte unaffected.
- **Docs:** [`docs/architecture/room-workbench.md`](../architecture/room-workbench.md)
  (new — the narrative home), [`docs/stories/state-machine.md`
  §"Room workbenches"](../stories/state-machine.md#room-workbenches--one-block-instead-of-four-hand-rolled-primitives)
  (new, short, links back), `stories/dev-story/README.md`'s landing section
  (updated to point at the primitive).

## Vocabulary (shipped)

`workbench:` state block: `{agent, prompt, acceptance_schema, capture_slot?,
off_ramp_agent?, context_args?, plan?}`. Full field-by-field contract:
`WorkbenchDecl`'s doc comment in `internal/app/types.go`, or the "The
declaration" section of the architecture doc.

## Tasks

```
## 1. Engine
- [x] 1.1 `Workbench` struct on `State`; loader desugaring pass (write_mode,
      agent_off_ramp, on_enter host.agent.task, default_intent/free_form_fallback)
      — 4b3d124e7e ("app: add workbench: state-block desugaring")
- [x] 1.2 Load-time invariants: agent toolbox+effect required (effect ∈
      {write, external}); mutual-exclusion with hand-authored write_mode/
      agent_off_ramp/default_intent; clear error messages
      — 2e94a7684d ("app: enforce workbench: load-time invariants")
- [x] 1.3 Re-verify the claude-backend Studio MCP precondition with a live
      dispatch; file/fix if still broken before proceeding.
      Confirmed broken by a live two-run precheck (strict `--mcp-config`
      omitting vs. including a `kitsoki` server entry: the first showed
      `mcp_servers: [{"name":"kitsoki-validator","status":"failed"}]` with no
      studio tools reachable; the second, with the entry added, connected and
      the agent called `studio_ping` successfully). Root cause:
      `appendStrictMCPConfigFlag` made `--mcp-config` the only MCP source for
      every claude-backend dispatch, but no call site ever added a `kitsoki`
      studio-server entry. Fixed by `attachStudioMCPServer`
      (`internal/host/agent_studio_mcp.go`): auto-attaches a `kitsoki` MCP
      entry whenever the effective tool surface names any `mcp__kitsoki__*`
      tool, resolved the same way the project's own `.mcp.json` does; an
      explicit agent-declared entry always wins.
      — 14c54b6b511 ("host: auto-attach the kitsoki studio MCP server when a
      toolbox declares it")
- [x] 1.4 Decision recording: confirm agent.call / write_mode_granted /
      TransitionApplied provenance is unchanged and legible for a desugared
      room. Also lands the S6 usable-kitsoki-gate producer contract's
      minimal honest version (`usable_kitsoki_gate` on `turn.end`,
      `internal/orchestrator/workbench_gate_signal.go`) — not yet joined
      against S4's scenario IR (documented gap in the file's own doc
      comment).
      — 1f6389a18e6 ("app: workbench decision-recording proof + S6 gate
      producer signal")

## 2. Verification
- [x] 2.1 Stateless unit: loader desugars a minimal `workbench:` block into
      the expected State shape; invariant violations produce the expected
      load errors — landed alongside 1.1/1.2 (4b3d124e7e, 2e94a7684d)
- [x] 2.2 Flow fixture: a synthetic story with one `workbench:` room —
      capture free text (no LLM routing), dispatch (stubbed), write-mode
      hold + grant, off-ramp Q&A path, route-out to a second authored room
      with deterministic world set before its on_enter
      — d9b38d6e130 ("app: no-LLM flow fixture for workbench: desugaring +
      dev-story additivity proof")
- [x] 2.3 dev-story's existing flow-fixture suite still passes unmigrated
      before landing.yaml touches workbench: (this task doesn't touch
      landing.yaml yet — proves additivity) — same commit, d9b38d6e130

## 3. Adopt + document
- [x] 3.1 Migrate `stories/dev-story/rooms/landing.yaml` onto `workbench:`;
      the dev-story flow-fixture suite passes at its pre-existing 79/82 (3
      failures are a documented unrelated `impl.idle` regression, not caused
      by this migration). Fallout fixed along the way: import-rewriter
      didn't rewrite `Workbench.Agent`/`OffRampAgent`; the loader's
      `free_form_fallback` auto-detect only matched the bare `work` name, so
      three importing stories now declare the routing block explicitly.
      — 3f34093d429 ("app: migrate dev-story's landing.yaml onto workbench:")
- [x] 3.2 `docs/architecture/room-workbench.md`; `docs/stories/state-machine.md`
      §"Room workbenches"; dev-story README landing section repointed at the
      primitive; this proposal trimmed to a shipped-record summary.
```

## Open questions — resolved as shipped

1. **Capture-slot naming** — synthesized from the room (`<room>_capture`),
   not author-named. Shipped as leaned.
2. **`plan: true` scope** — shipped as a load-time schema-contract check
   only; the propose/accept/apply/verify sub-loop rooms stay hand-authored.
   Code-generating them is deferred until a second real consumer exists
   (still a real non-goal, see below).

## Non-goals

- **The ambient miner / `/mine` control surface / blank project roots**
  (`ad-hoc-workbench.md` Slices 2–4) — a future proposal's scope.
- **A new permission model.** WS `toolbox:`/`effect:` declarations remain
  the only mechanism `workbench:` honors.
- **LLM-mediated dispatch of `initial_world`.** Route-outs always go
  through a real transition's `set:`/`emit_intent:` effects.
- **Code-generating the propose/accept/apply/verify sub-loop rooms** from
  `plan: true` — deferred until a second consumer exists.
