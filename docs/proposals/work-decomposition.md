# Story: Work decomposition — turn a proposal into a coordinated build

**Status:** Draft v2 (revised after adversarial review). Nothing implemented yet.
**Kind:**   story
**Epic:**   — standalone

## Why

The proposal pipeline (`stories/dev-story/rooms/proposal*.yaml`) ends at
**publish**: a focused proposal lands in `docs/proposals/<slug>.md` and the
operator is on their own to break it into work and drive each piece. For an
epic (`docs/proposals/templates/epic.md` — a Slices table over linked
children) that gap is widest: the hard part isn't writing the epic, it's
**decomposing it into right-sized agent briefs, proving the decomposition is
feasible and complete, and then shepherding each brief through build + test
without dropping a dependency.** Today that is hand-driven in chat — exactly
the un-recorded, un-replayable interpretive work kitsoki exists to make a
deterministic graph (`docs/proposals/process-design.md` §6, the meta-story).

We want the operator to hand the hub an accepted proposal (or an epic +
children) and get back a **validated, reviewed decomposition YAML** of agent
briefs — then coordinate the build one brief at a time, reusing the per-brief
pipeline (`stories/implementation/`) that already ships.

> "in dev-story we need some way to take an existing proposal (or epic+children)
> and decompose the work, then coordinate the implementation and testing. there
> should be an interactive discovery phase and it should produce a yaml with the
> decomposition of agent briefs — structurally verified by the mcp submit
> validator and then reviewed by an adversarial review agent for feasibility and
> completeness."

## What changes

A new importable sub-story **`stories/decompose/`**, pipeline-shaped and
structurally identical to `stories/bugfix/` / `stories/implementation/`,
imported into `stories/dev-story/` as alias `decomp` and reached from `main`
once a proposal is selected. Its shape in one sentence: **a discovery
conversation distils scope, an `oracle.decide` emits a brief manifest the MCP
submit validator structurally enforces, a deterministic `host.run` renders +
lints it to `decomposition.yaml`, an adversarial `oracle.decide` judges
feasibility + completeness, and a coordination board — for each brief in
dependency order — mints a per-brief ticket + worktree, resets the import's
world, and dispatches it into the `impl` import, one at a time with a human
gate between briefs.**

The interpretive decisions (how to slice the work, is it feasible, is it
complete) are `decide`/`task` operators; the structural truth (unique ids,
acyclic dependency DAG, every brief has acceptance + a test plan, scoped paths
exist) is a **deterministic** `host.run` whose exit code is the gate — the
moat (`feedback_kitsoki_moat_is_architecture`): separate interpretation from
deterministic execution, record every decision.

## Impact

Story-layer composition only. No new effects, host calls, or widgets — the MCP
submit validator (`docs/architecture/hosts.md` § `host.oracle.decide`: a
`schema:` forces a schema-valid `submit()`), `host.oracle.decide/task`,
`host.artifacts_dir`, the proposal-workspace minting script, and the
dispatch-into-an-import edge (`stories/implementation/rooms/handoff.yaml`,
`stories/dev-story/app.yaml:768` impl import) all exist.

The one non-obvious cost: **the `impl` pipeline is ticket-driven** — it reads
the task body via `iface.ticket.get` against `world.ticket_id`
(`stories/implementation/rooms/review_task.yaml`) and has no free-text
`objective` world key. So a brief can't be projected straight in via
`world_in:` — the loader rejects an undeclared child key
(`internal/app/imports.go:384`). Each brief must first be **materialised as a
ticket** the default `iface.ticket.get` can read, and because an import is
folded once at load time (one shared set of `impl__*` keys —
`internal/app/imports.go:6`), the board must **reset the `impl__*` carry keys
between dispatches** or brief N inherits brief N-1's artifacts (and impl's
`once: true` guards skip regeneration). All three seams — ticket mint,
workspace mint, world reset — are story-layer (`host.run` + `set:`), so:

- **Net-new:** `stories/decompose/` — ~7 rooms, ~5 prompts, 3 schemas, an
  agents table, 4 `scripts/` (`decompose_load`, `decompose_validate`,
  `decompose_board`, `decompose_brief_ticket`), 4 flow fixtures, README.
  Plus a thin `decomp` import block + a `decompose` launch arc in
  `stories/dev-story/rooms/main.yaml` (guarded on a selected proposal).
- **Engine/host changes:** none — but the import-loop seams above
  (per-brief ticket mint, workspace mint, `impl__*` reset, `decomposition`
  re-pinned on the `@exit:done` arc) are explicit story steps, not free
  composition.
- **Docs on ship:** `docs/stories/decompose.md`, this folder's `README.md`
  entry, and a row in `stories/dev-story/README.md`'s Rooms/Composition table.

## Reuse inventory

| Pipeline step | Mechanism | Reference |
|---|---|---|
| Load proposal / epic + children | `host.run` reader script (reads `docs/proposals/<slug>.md`, parses an epic Slices table) | mirror `stories/dev-story/scripts/proposal_workspace.py` |
| Mint per-session workspace + editable scope note | `host.run` workspace mint + `host.artifacts_dir` scaffold + `host.ide.open_file` | `stories/dev-story/rooms/proposal.yaml` (001-brief mint arm) |
| Interactive discovery | `mode: conversational` + `host.oracle.converse` + a scope-note writer `host.oracle.task` | `stories/dev-story/rooms/proposal.yaml` discuss arc |
| **Structural verification of the brief manifest** | `host.oracle.decide` with `schema:` → MCP submit validator forces a schema-valid `submit()` | `docs/architecture/hosts.md` § `host.oracle.decide`; `stories/dev-story/rooms/proposal_idea_completeness.yaml` |
| Render YAML + deep structural lint (DAG, ids, coverage) | deterministic `host.run` → exit code is the gate | the proposal pipeline's slug/uniquify "validation sandwich" (`proposal.yaml` `uniquify`) |
| **Adversarial feasibility + completeness review** | `host.oracle.decide` skeptic agent → `{verdict, reason, questions}` | `stories/code-review/rooms/review_pr.yaml`; `brief-decision.json` verdict shape |
| Checkpoint / iterate on a failing gate | `accept` / `revise(feedback)` + cycle budget → `@exit:abandoned` | `stories/bugfix/rooms/proposing.yaml`; `stories/dev-story/rooms/proposal_draft.yaml` refine arc |
| **Materialise a brief as a ticket** the pipeline can read | `host.run` writes a local ticket (id/title/body = brief) the default `iface.ticket.get` resolves | `stories/implementation/rooms/review_task.yaml` (`iface.ticket.get`); `host.local_files.ticket` default |
| Mint a fresh worktree/branch per brief | `host.run` workspace + branch mint | `stories/dev-story/scripts/proposal_workspace.py`; `stories/dev-story/rooms/workspace_manager.yaml` |
| Dispatch one brief into build+test | reset `impl__*` keys (`set:`), then enter the `impl` import; its `@exit:done` returns to the board | `stories/dev-story/app.yaml:768` (impl import, `entry: idle`, `world_in:`); `internal/app/imports.go:6` (single fold) |

## Story graph

```
idle ── start ──▶ discovery ── ready ──▶ decompose ── built ──▶ validate ──┬─pass─▶ review ──┬─accept─▶ board ⇄ dispatch
 │ (load proposal/  (converse:           (decide →      (host.run:          │  (adversarial   │          │   (per ready brief →
 │  epic+children,   sharpen scope,       brief          schema-valid       │   feasibility + │          │    impl import; on its
 │  mint workspace)  surface unknowns,    manifest)      YAML + DAG +        │   completeness) │          │    @exit:done mark done,
 │                   write 002-scope.md)                 coverage lint)      │                 │          │    return to board)
 │                                                       └─fail─▶ decompose  └─revise─▶ decompose         │
 └─ quit ─▶ @exit:abandoned                                (refine, budgeted)  (refine, budgeted)         │
                                                                                                          │
                                                          board ── all briefs done ──▶ @exit:done ◀───────┘
```

The `validate` → `decompose` fail edge and the `review` → `decompose` revise
edge are the two budgeted refine loops. `board` is the coordination room; it is
the only room the operator dwells in across multiple briefs. The `dispatch`
room (board ⇄ dispatch) mints a per-brief ticket + worktree and resets the
`impl__*` keys before each entry into the import, so every brief builds from a
clean slate.

## World schema (sketch)

```yaml
world:
  # ── Source ─────────────────────────────────────────────────────────
  decomp_source_path:   { type: string, default: "" }   # docs/proposals/<slug>.md
  decomp_source_kind:   { type: string, default: "" }   # "proposal" | "epic"
  decomp_source_text:   { type: string, default: "" }   # proposal body (+ children, if epic)
  decomp_children:      { type: list,   default: [] }    # epic child file paths

  # ── Workspace / discovery ────────────────────────────────────────────
  decomp_workspace:     { type: string, default: "" }   # docs/proposals/.workspace/<slug>-decomp/
  decomp_scope_path:    { type: string, default: "" }   # 002-scope.md (operator-editable)
  decomp_chat_id:       { type: string, default: "" }
  decomp_scope_answer:  { type: string, default: "" }

  # ── Manifest + gates ──────────────────────────────────────────────────
  decomposition:        { type: object, default: {} }   # MCP-validated submit() payload (briefs[])
  decomp_yaml_path:     { type: string, default: "" }   # decomposition.yaml on disk
  decomp_validation:    { type: object, default: {} }   # { ok: bool, errors: [] } from host.run
  decomp_review:        { type: object, default: {} }   # { verdict, reason, questions[] }

  # ── Coordination board ────────────────────────────────────────────────
  # decomp_briefs is rewritten each board entry by decompose_board.py (status
  # per brief); set:/increment can't mutate list elements, so the recompute is
  # a deterministic host.run, not pongo (feedback_kitsoki_moat_is_architecture).
  decomp_briefs:        { type: list,   default: [] }    # each: { ...brief, status: ready|blocked|done }
  decomp_done_ids:      { type: list,   default: [] }    # ids that reached impl @exit:done
  decomp_last_done_id:  { type: string, default: "" }    # set by the impl exit projection; folded in by the board
  decomp_next_ready:    { type: object, default: {} }    # next dispatchable brief (board recompute)
  decomp_current_brief: { type: object, default: {} }    # the brief being dispatched (seeds its ticket + workspace)
  decomp_done_count:    { type: int,    default: 0 }     # len(decomp_done_ids); guards all_done

  # ── Refine budgets ────────────────────────────────────────────────────
  refine_feedback:      { type: string, default: "" }
  decompose_cycle:      { type: int,    default: 0 }
  decompose_budget:     { type: int,    default: 5 }
  abandon_reason:       { type: string, default: "" }
```

`exits:` — `done: { requires: [decomposition] }`, `abandoned: {}`.

### The brief manifest (`schemas/decomposition.json`) — the heart

This is the schema the MCP submit validator enforces, so a `decompose` agent
**cannot exit without a structurally complete manifest**. The deterministic
`validate` step then checks the cross-brief properties a per-object schema
can't (acyclic deps, id uniqueness, coverage).

```jsonc
{
  "type": "object",
  "required": ["briefs", "coverage_note"],
  "properties": {
    "coverage_note": { "type": "string", "minLength": 40,
      "description": "How the briefs together fully cover the proposal — the completeness claim the reviewer will attack." },
    "briefs": {
      "type": "array", "minItems": 1,
      "items": {
        "type": "object",
        "required": ["id","title","kind","goal","scope","acceptance","test_plan","agent_brief","depends_on"],
        "properties": {
          "id":          { "type": "string", "pattern": "^[a-z0-9]+(-[a-z0-9]+)*$" },
          "title":       { "type": "string", "minLength": 4 },
          "kind":        { "type": "string", "enum": ["story","runtime","tui","tracing","test","docs"] },
          "goal":        { "type": "string", "minLength": 20, "description": "What 'done' looks like, in the reviewer's terms." },
          "scope":       { "type": "array", "items": { "type": "string" },
                           "description": "File/dir globs this brief may touch — the write boundary." },
          "depends_on":  { "type": "array", "items": { "type": "string" }, "description": "ids of briefs that must land first." },
          "acceptance":  { "type": "array", "minItems": 1, "items": { "type": "string" } },
          "test_plan":   { "type": "string", "minLength": 20, "description": "How it's verified: which flow fixture / go test / manual check." },
          "agent_brief": { "type": "string", "minLength": 80, "description": "The self-contained prompt handed to the implementer agent." },
          "risk":        { "type": "string", "enum": ["low","medium","high"] }
        }
      }
    }
  }
}
```

## Per-room detail

### `idle` — load the source

- **`on_enter`:** `host.run decompose_load.py <slug>` — resolves
  `docs/proposals/<slug>.md`, detects epic vs. focused (presence of a Slices
  table / `**Kind:** epic`), reads the body and (for an epic) every linked
  child, mints `docs/proposals/.workspace/<slug>-decomp/`. Binds
  `decomp_source_text`, `decomp_source_kind`, `decomp_children`,
  `decomp_workspace`. `once: true` (reload-safe).
- **Intents:** `start` → `discovery`; `quit` → `@exit:abandoned`. (Reached
  from the hub with `decomp_source_path` already projected via `world_in:`.)
- **View:** the resolved title/kind + child count, so the operator confirms
  they pointed at the right thing before talking.

### `discovery` — conversational scope sharpening

- **`mode: conversational`**, `default_intent: discuss` — verbatim shape from
  `proposal.yaml`. First message scaffolds an editable `002-scope.md` (slicing
  constraints: what must be sequential vs. parallel, the test strategy, risk
  areas, explicit non-goals); each turn `host.oracle.converse` (a
  `decomp_interviewer`) sharpens and a `decomp_scope_writer` `host.oracle.task`
  folds the exchange into `002-scope.md`.
- **Intents:** `discuss` (self-loop), `ready` → `decompose`, `quit`.
- **View:** the editable `002-scope.md` path + the latest interviewer reply
  (mirrors `proposal.yaml`).

### `decompose` — emit the brief manifest (structurally verified)

- **`on_enter`:** `host.oracle.decide` with `agent: decomposer`,
  `schema: schemas/decomposition.json`, `working_dir: "."` (read-only repo
  inspection), args = `{source_text, scope_path, children}`. The
  **MCP submit validator** forces a schema-valid `submit()`; `bind:
  decomposition: submitted`. `once: true`, keyed on `decomposition` so
  `revise`/`fail` clear it to re-arm (the `proposal_draft.yaml` pattern).
- **Intents:** `built` → `validate`; `quit`. `built` is fired by an
  `emit_intent: built` effect in `on_enter`, guarded
  `when: "len(world.decomposition.briefs) > 0"`, so it advances whether or not
  the (`once: true`) decide actually ran this turn — reload / `look` re-advance
  rather than stall (`stories/dev-story/rooms/proposal_draft.yaml` pattern).
- **View:** a `kv` of brief count + kinds + the `coverage_note`; a `code`
  block listing `id — title (deps)` per brief.

### `validate` — deterministic DAG + coverage lint (the gate)

- **`on_enter`:** `host.run decompose_validate.py` fed the bound
  `decomposition` JSON. It (1) **renders `decomposition.yaml`** into the
  workspace, (2) asserts unique ids, (3) topo-sorts `depends_on` and **fails
  on a cycle or a dangling id**, (4) checks every `scope` path is **inside the repo
  and its parent dir exists** — new-file briefs have globs that intentionally
  don't match an existing file yet, so it bounds the path rather than requiring
  a match — (5) checks every brief has non-empty `acceptance` + `test_plan`.
  Binds `decomp_validation = { ok, errors[] }`, `decomp_yaml_path`.
- **Intents (post-bind emit on `decomp_validation.ok`):** `pass` → `review`;
  `fail` → `decompose` with `refine_feedback` = the joined `errors[]` and the
  cycle-budget gate → `@exit:abandoned` when exhausted.
- **View:** the YAML path + a `code` list of `errors[]` when `!ok`.

### `review` — adversarial feasibility + completeness

- **`on_enter`:** `host.oracle.decide` with `agent: decomp_adversary`,
  `schema: schemas/review-decision.json` (`{verdict: accept|revise, reason,
  questions[]}`). The prompt frames a **skeptic**: per brief, *is this
  actually buildable as scoped, are its deps right, is anything impossible or
  hand-wavy?*; across briefs, *do they fully cover the proposal (attack the
  `coverage_note`), and is there overlap/double-ownership of files?* Default
  to `revise` when uncertain (the adversarial-verify discipline). `once: true`,
  re-armed by clearing `decomp_review`.
- **Intents (post-bind emit):** `accept` → `board`; `revise` → `decompose`
  with the `questions[]` as `refine_feedback`, budgeted.
- **View:** verdict + reason + a `code` list of open questions (mirrors
  `proposal.yaml`'s quality-review block).

### `board` — coordinate the build, one brief at a time

- **`on_enter`:** `host.run decompose_board.py` fed `decomp_briefs`,
  `decomp_done_ids`, `decomp_last_done_id`. It folds `last_done_id` into the
  done set and **recomputes each brief's status** (`done` if in the set,
  `ready` when every `depends_on` is done, else `blocked`) and the next
  dispatchable brief. Binds `decomp_briefs` (status-stamped), `decomp_done_ids`,
  `decomp_done_count`, `decomp_next_ready`. Deterministic — `set:`/`increment`
  can't mutate list elements, so board state is a script, not pongo (the moat).
  **Not** `once:` — it must re-run on every return from a brief.
- **Intents:**
  - `dispatch` (guarded `decomp_next_ready.id != ""`) → **`dispatch` room**.
    Its effects set `decomp_current_brief: "{{ world.decomp_next_ready }}"` and
    **clear the import's carry keys** so brief N never inherits brief N-1's
    state: `set: { impl__task_summary_artifact: {}, impl__code_artifact: {},
    impl__test_artifact: {}, impl__review_artifact: {}, impl__cycle: 0,
    impl__ci_log: "", impl__feature_branch: "", impl__pr_url: "",
    impl__done_artifact: {} }`.
  - `all_done` (guarded `decomp_done_count == len(decomp_briefs)`) →
    `@exit:done`, effects `set: { decomposition: "{{ world.decomposition }}" }`
    — re-pinned because `exits.done.requires: [decomposition]` is statically
    checked on the arc and an empty-object default doesn't count
    (`internal/app/imports.go:332`; mirrors implementation re-pinning
    `code_artifact` on its done arc, `stories/implementation/app.yaml:284`).
  - `skip(id)` (operator override — adds `id` to the done set via a
    `decomp_last_done_id` set + board recompute, without building), `quit`.
- **View:** a `code:` `{% for b in world.decomp_briefs %}` board — each brief
  `{{ b.status }} · {{ b.id }} — {{ b.title }}`, the next-ready brief
  highlighted, a progress count. The human gate **is** the board: after each
  brief returns, the operator chooses to `dispatch` the next.

### `dispatch` — mint the brief's ticket + worktree, enter `impl`

- **`on_enter`:** `host.run decompose_brief_ticket.py` fed
  `decomp_current_brief`. It (1) writes a local ticket (`id = brief.id`,
  `title = brief.title`, `body = agent_brief + acceptance + scope + test_plan`)
  the default `iface.ticket.get` resolves, and (2) mints a fresh worktree/branch
  under `.worktrees/`. Binds `ticket_id`, `ticket_title`, `workdir`,
  `feature_branch`. The script is **get-or-create keyed on `brief.id`** so it's
  reload-safe without `once:` (which can't be used here — `ticket_id` persists
  across briefs, so it would skip the mint for brief 2+). Then
  `emit_intent: go` advances into the import.
- **Intents:** `go` → enter the **`impl` import** (`target: impl`); `world_in:`
  projects `ticket_id` / `ticket_title` / `workdir` / `feature_branch` (impl
  fetches the body itself via `iface.ticket.get`). `impl`'s `@exit:done`
  projects back to `board` with
  `set: { decomp_last_done_id: "{{ world.decomp_current_brief.id }}" }`;
  `@exit:abandoned` returns to `board` leaving the brief re-dispatchable.
- **View:** the brief id/title + the minted ticket + workdir, so the operator
  sees what's about to build.

### Net-new files

```
stories/decompose/
├── app.yaml
├── rooms/{idle,discovery,decompose,validate,review,board,dispatch}.yaml
├── prompts/{discovery_interview,scope_distill,decompose,review_adversary}.md
├── schemas/{decomposition.json,review-decision.json,scope-distill.json}
├── scripts/{decompose_load.py,decompose_validate.py,decompose_board.py,decompose_brief_ticket.py}
├── flows/{happy_path,validate_fail_loop,review_revise_loop,budget_exhausted}.yaml
└── README.md
```

## Flow fixtures

Mode-2, intent-only, no-LLM, CI-fast — each `decide`/`task`/`run` stubbed by
per-invoke `id` (`feedback_oracle_stub_by_id`); stubs replay realistic wire
shapes (`feedback_e2e_fidelity_and_boundary`).

- `happy_path` — `idle → discovery → decompose → validate(pass) → review(accept)
  → board → dispatch (ticket/worktree mint stubbed, impl__* reset, ×N into a
  stubbed impl @exit:done; board recompute marks each brief done) → all_done →
  @exit:done`.
- `validate_fail_loop` — `decompose_validate.py` stub returns a cycle error;
  `validate` routes to `decompose`, `decompose_cycle` increments, second pass
  validates clean.
- `review_revise_loop` — adversary returns `revise`; back to `decompose`;
  second adversary pass returns `accept`.
- `budget_exhausted` — `revise`/`fail` at budget → `@exit:abandoned` with
  `abandon_reason`.

The strongest gate is free: `decompose_validate.py` is unit-tested directly on
crafted manifests (cycle, dup id, missing acceptance, dangling dep), so the
DAG/coverage logic has teeth independent of any LLM — and `decompose_board.py`'s
dep-gating recompute is unit-tested the same way (a brief is `ready` only when
every dep is in the done set).

## Tasks

```
## 1. Scaffold
- [ ] 1.1 stories/decompose/app.yaml + 7 room files, typed `extends: "base"` views, world schema, exits
- [ ] 1.2 schemas/{decomposition,review-decision,scope-distill}.json
- [ ] 1.3 scripts: decompose_load.py (proposal/epic reader), decompose_validate.py (YAML render + DAG/coverage lint + path-bounds check), decompose_board.py (deterministic status recompute), decompose_brief_ticket.py (mint ticket + worktree) — with unit tests on crafted manifests (cycle, dup id, missing acceptance, dangling dep) and a board-recompute test (dep gating)
- [ ] 1.4 stub prompts (discovery_interview, scope_distill, decompose, review_adversary) + agents table (read-only inspectors; decomposer + adversary have NO Write — feedback_task_agents_must_not_implement)

## 2. Lock the graph
- [ ] 2.1 Probe each room: `kitsoki turn stories/decompose/app.yaml --state <room> --intent <x> --world @w.json`
- [ ] 2.2 Flow fixtures pass (happy_path, validate_fail_loop, review_revise_loop, budget_exhausted)

## 3. Wire into the hub
- [ ] 3.1 dev-story: `imports.decomp` block + `decompose` launch arc in main.yaml (guarded on a selected proposal); world_in projects decomp_source_path
- [ ] 3.2 decomp `@exit:done`/`abandoned` projections back into main; flow fixture for the hub→decomp→impl chain
- [ ] 3.3 Per-brief impl dispatch: verify the `impl__*` reset clears all carry keys (brief N+1 regenerates, not reuses), the minted ticket reaches impl via `iface.ticket.get`, a fresh worktree/branch per brief, and the `all_done` arc re-pins `decomposition` (loader requires-check passes)

## 4. Live + document
- [ ] 4.1 `kitsoki run stories/decompose/app.yaml` end-to-end against a small real proposal (e.g. a 2-3 brief slice)
- [ ] 4.2 README (entry state, exits, world contract, host requirements, the impl import edge)
- [ ] 4.3 Migrate to docs/stories/decompose.md; add README.md queue entry; trim/delete this proposal
```

## Open questions

1. **Per-brief execution: `impl` import vs. ticket emission.** Because `impl`
   is ticket-driven, per-brief ticket emission is *already* the substrate of
   the impl dispatch (the `dispatch` room mints the ticket, then enters the
   import). The smaller-slice option — emit every brief as a ticket and
   **stop**, leaving the build to the existing dogfood loop — is therefore
   `dispatch`-minus-the-`go`, not a separate mechanism. *Lean: ship the full
   `impl` dispatch; expose an `export_all` intent on `board` that mints every
   brief's ticket without entering the import, for partial / hand-off runs.*
2. **Worktree-per-brief vs. one shared worktree.** Sequential dispatch with a
   gate means one worktree could suffice, but parallel-ready briefs want
   isolation (`feedback_workflow_git_guardrails`). *Lean: one fresh
   worktree/branch per brief even when sequential — clean rollback, honors the
   git guardrails — and it leaves the door open to parallel fan-out later
   (`project_execution_modes_gate_deciders`).*
3. **Does `decompose` also emit the per-brief flow-fixture stub** the brief's
   `test_plan` references, so the eventual build has a regression target? *Lean:
   no for v1 — the brief names its test plan; generating the fixture is the
   implementer's job inside `impl`.*
4. **Epic source freshness.** An epic's Slices table can drift from its
   children. Should `decompose_load.py` warn on a child file missing/renamed?
   *Lean: yes — a non-fatal warning surfaced in `idle`'s view.*

## Non-goals

- **Parallel autonomous fan-out** (spawn an implementer per brief, run them
  concurrently, auto-integrate). Deferred — needs the write/git sandboxing and
  staged-mode deciders from `project_execution_modes_gate_deciders` /
  `task-fs-sandbox.md`. v1 is sequential, gated dispatch.
- **Authoring the proposal.** That's the existing `proposal*` pipeline; this
  story starts from an *accepted* proposal/epic.
- **A new runtime primitive for "iterate over a list of sub-tasks."** v1 models
  the board as a cyclic graph over `decomp_briefs`; if a second story needs the
  same loop, extract it then (`process-design.md` §7 open-question discipline).
```
