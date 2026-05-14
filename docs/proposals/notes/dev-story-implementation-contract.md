# Dev-story / bugfix unify — implementation contract (Wave 1)

Companion to `docs/proposals/dev-story-bugfix-unify-proposal.md`. Defines the
exact shared shapes that Phase 1 (bugfix skeleton + judge polymorphism +
foundational providers + judge harness) must agree on so multiple authors can
work in parallel without drifting. **All Wave 1 agents read this first.**

Status: spec for Phase 1 only. Phase 2+ extends the contract as needed.

## 1. Repository conventions to follow

- New host handlers go in `internal/host/` (flat package, same as
  `handlers.go`, `oracle_ask.go`, `transport_post.go`). One file per logical
  surface (e.g. `localfiles_ticket.go`, `git_vcs.go`, `local_ci.go`,
  `git_worktree.go`, `append_file_transport.go`, `inbox_add.go`).
- Each new handler registers itself in `RegisterBuiltins` in `handlers.go`.
- Each handler ships with a `*_test.go` in the same package that exercises
  happy + at least one failure path with table-driven cases — same shape as
  `transport_post_test.go`.
- Story directories use the existing convention: `stories/<name>/{app.yaml,
  README.md, rooms/*.yaml, prompts/*.md, schemas/*.json, flows/*.yaml,
  scenarios/*.yaml}`. The mandatory README documents the contract per
  `docs/imports.md` "File layout."
- Use `kitsoki test flows stories/<name>/app.yaml` for story-level testing
  (works today — see `stories/oregon-trail/flows/`).

## 2. The six `host_interfaces:` — canonical operation schemas

Every story that uses these interfaces declares them with **exactly** these
op names, input shapes, and output shapes. Providers register handlers under
the names listed in §3.

### 2.1 `ticket` — issue tracker

```yaml
host_interfaces:
  ticket:
    description: "Issue tracker abstraction (file / GitHub Issues / Jira)."
    operations:
      search:
        input:  { query: string, limit: int }
        output: { tickets: list }      # [{id,title,status,priority,assignee,url}]
      get:
        input:  { id: string }
        output: { id: string, title: string, body: string, status: string,
                  priority: string, assignee: string, url: string, comments: list }
      comment:
        input:  { id: string, body: string, thread: string }
        output: { ok: bool, comment_id: string }
      transition:
        input:  { id: string, to: string }
        output: { ok: bool }
      list_mine:
        input:  { filter: string }
        output: { tickets: list }
    default: host.local_files.ticket
```

### 2.2 `vcs` — version control + PR host

```yaml
host_interfaces:
  vcs:
    description: "Branch / commit / PR abstraction (git / GitHub / Bitbucket)."
    operations:
      branch:
        input:  { workdir: string, name: string, base: string }
        output: { ok: bool, branch: string }
      diff:
        input:  { workdir: string }
        output: { diff: string, files: list }
      commit:
        input:  { workdir: string, message: string, files: list }
        output: { ok: bool, sha: string }
      push:
        input:  { workdir: string, remote: string }
        output: { ok: bool, url: string }
      open_pr:
        input:  { workdir: string, title: string, body: string, base: string }
        output: { ok: bool, url: string, pr_id: string }
      pr_status:
        input:  { pr_id: string }
        output: { state: string, checks: list, comments: list }
      pr_comment:
        input:  { pr_id: string, body: string }
        output: { ok: bool }
    default: host.git
```

### 2.3 `ci` — build & test runner

```yaml
host_interfaces:
  ci:
    description: "Build/test runner (local make/go test, GitHub Actions, Jenkins)."
    operations:
      run_tests:
        input:  { workdir: string, target: string }
        output: { ok: bool, passed: int, failed: int, log: string, junit: string }
      build:
        input:  { workdir: string, target: string }
        output: { ok: bool, log: string }
      remote_status:
        input:  { pr_id: string }
        output: { state: string, checks: list }
    default: host.local
```

### 2.4 `workspace` — per-task working tree

```yaml
host_interfaces:
  workspace:
    description: "Working-copy manager. Local: git worktree."
    operations:
      list:
        input:  {}
        output: { workspaces: list }
      get:
        input:  { id: string }
        output: { id: string, path: string, branch: string, dirty: bool }
      create:
        input:  { name: string, ticket_id: string, base: string }
        output: { ok: bool, path: string }
      sync:
        input:  { id: string }
        output: { ok: bool, log: string }
    default: host.git_worktree
```

### 2.5 `transport` — out-of-band channel for checkpoint artifacts

```yaml
host_interfaces:
  transport:
    description: "Out-of-band channel for posting proposals, checkpoints, status."
    operations:
      post:
        input:  { thread: string, body: string }
        output: { ok: bool, message_id: string }
    default: host.append_to_file   # for kitsoki-dev; cyber rebinds to host.jira_comment
```

### 2.6 `inbox` — local TUI inbox mirror (NOT an iface, registered as `host.inbox.add` directly)

The inbox is intentionally **not** an `host_interfaces:` block — it has only
one op and it's always-on. Stories invoke it as a bare host call:

```yaml
on_enter:
  - invoke: host.inbox.add
    with:
      kind:    checkpoint            # checkpoint | ack | info
      title:   "Reproduction artifact: {{ world.ticket_id }}"
      thread:  "{{ world.thread }}"
      state:   bugfix_reproduce_awaiting_reply
      body:    "{{ world.reproduction_artifact.summary_markdown }}"
```

`host.inbox.add` is always-on across modes — see proposal §4.5.

## 3. Handler names (Go side)

These are the strings passed to `Registry.Register(name, handler)`. Stories
reference them via `host_interfaces.<iface>.default` or `host_bindings`.

| Handler name | Iface op(s) it backs | File |
|---|---|---|
| `host.local_files.ticket` (prefix-fallback handler) | all `ticket.*` ops | `internal/host/localfiles_ticket.go` |
| `host.local_files.ticket.search` | optional split | — |
| `host.local_files.ticket.get` | optional split | — |
| `host.local_files.ticket.comment` | optional split | — |
| `host.local_files.ticket.transition` | optional split | — |
| `host.local_files.ticket.list_mine` | optional split | — |
| `host.git` (prefix-fallback handler) | all `vcs.*` ops | `internal/host/git_vcs.go` |
| `host.local` (prefix-fallback handler) | all `ci.*` ops | `internal/host/local_ci.go` |
| `host.git_worktree` (prefix-fallback handler) | all `workspace.*` ops | `internal/host/git_worktree.go` |
| `host.append_to_file` | `transport.post` (writes to bug file) | `internal/host/append_file_transport.go` |
| `host.inbox.add` | bare inbox call (not iface) | `internal/host/inbox_add.go` |

The proposal's `host.stdout` (a no-op transport for tests / standalone runs)
already maps to existing `host.run` patterns; if a stand-alone fallback is
needed, register `host.stdout` as a thin alias in `inbox_add.go`'s file (or
add `internal/host/stdout_transport.go`).

The runtime registry's **prefix-fallback** means a single registration of
`host.git` satisfies every `host.git.<op>` call until per-op handlers are
registered. Wave 1 ships only the prefix-fallback handlers (one per surface);
per-op handlers come later if and when an op needs distinct behaviour.

## 4. World shape — Wave 1 keys

These keys are declared in `stories/bugfix/app.yaml`'s `world:` block.
Provider handlers populate them via `bind:` projections in `on_enter`.

```yaml
world:
  # ─── Identity / ticket ──────────────────────────────────────────
  ticket_id:        { type: string, default: "" }
  ticket_title:     { type: string, default: "" }
  ticket_status:    { type: string, default: "" }
  ticket_url:       { type: string, default: "" }
  thread:           { type: string, default: "" }
  allowed_authors:  { type: list,   default: [] }

  # ─── Workspace ──────────────────────────────────────────────────
  workspace_id:     { type: string, default: "" }
  workdir:          { type: string, default: "" }
  base_branch:      { type: string, default: "" }
  feature_branch:   { type: string, default: "" }

  # ─── Pipeline control ───────────────────────────────────────────
  bugfix_mode:      { type: string, default: "full" }  # full | quick
  judge_mode:       { type: string, default: "human" } # human | llm | llm_then_human
  judge_confidence_threshold: { type: float, default: 0.8 }
  cycle:            { type: int,    default: 0 }
  last_reply_author: { type: string, default: "" }
  refine_feedback:   { type: string, default: "" }
  jump_to:           { type: string, default: "" }
  restart_from_stage: { type: string, default: "" }

  # ─── Per-room artifacts (Wave 1 ships 5; testing/reviewing collapse) ─
  reproduction_artifact:    { type: object, default: {} }
  propose_fix_artifact:     { type: object, default: {} }
  implement_review_artifact: { type: object, default: {} }
  validate_artifact:        { type: object, default: {} }
  done_artifact:            { type: object, default: {} }

  # ─── Judge state (set by judge harness, read by gate clauses) ────
  llm_verdict:      { type: object, default: {} }      # { intent, reason, confidence, verdict }

  # ─── PR (populated by pr-refinement; held here for round-trip) ───
  pr_id:            { type: string, default: "" }
  pr_url:           { type: string, default: "" }
  ci_state:         { type: string, default: "" }

  # ─── Story-level "done" sink for the standalone test mode ────────
  status:           { type: string, default: "open" }
```

Provider handlers are allowed to set additional namespaced keys
(`ticket__<x>`, `workspace__<x>`) when surfacing implementation-specific
detail, but the keys above are the canonical lingua franca.

## 5. Judge verdict schema (canonical)

`stories/bugfix/schemas/judge_verdict.json`:

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title":   "judge_verdict",
  "type":    "object",
  "required": ["verdict", "intent", "reason", "confidence"],
  "properties": {
    "verdict":    { "type": "string", "enum": ["pass", "fail", "uncertain"] },
    "intent":     { "type": "string", "enum": ["accept", "refine", "restart_from", "quit", "uncertain"] },
    "reason":     { "type": "string", "minLength": 4 },
    "confidence": { "type": "number", "minimum": 0.0, "maximum": 1.0 }
  },
  "additionalProperties": false
}
```

Used by `judge_*.md` prompts as the `schema:` argument to
`host.oracle.ask_with_mcp`. The structured response is bound to
`world.llm_verdict` so gate clauses can read it.

## 6. Judge polymorphism — the canonical checkpoint shape

Every `*_awaiting_reply` state in `stories/bugfix/` follows this exact
pattern:

```yaml
<phase>_awaiting_reply:
  description: "<phase> artifact posted; awaiting verdict."
  relevant_world: [judge_mode, ticket_id, <phase>_artifact, llm_verdict]
  on_enter:
    # 1. Always: post the artifact to whichever transport is bound.
    - invoke: iface.transport.post
      with:
        thread: "{{ world.thread }}"
        body:   "{{ world.<phase>_artifact.summary_markdown }}"

    # 2. Always: mirror the artifact into the local inbox.
    - invoke: host.inbox.add
      with:
        kind:    checkpoint
        title:   "{{ world.<phase>_artifact.summary_title }}"
        thread:  "{{ world.thread }}"
        state:   <phase>_awaiting_reply
        body:    "{{ world.<phase>_artifact.summary_markdown }}"

    # 3. Conditionally: ask an LLM-judge.
    - when: "world.judge_mode == 'llm' || world.judge_mode == 'llm_then_human'"
      invoke: host.oracle.ask_with_mcp
      with:
        prompt:  prompts/judge_<phase>.md
        schema:  schemas/judge_verdict.json
        context: "{{ world.<phase>_artifact }}"
      bind:
        llm_verdict: "submitted"

    # 4. Conditionally: auto-fire the LLM's intent if confident.
    - when: |
        world.judge_mode != 'human' &&
        world.llm_verdict.confidence >= world.judge_confidence_threshold &&
        world.llm_verdict.verdict != 'uncertain' &&
        world.llm_verdict.intent != 'uncertain'
      effects:
        - emit_intent: "{{ world.llm_verdict.intent }}"
          slots: { feedback: "{{ world.llm_verdict.reason }}" }

  on:
    accept:        [{ target: <next-room>_executing }]
    refine:        [{ target: <phase>_executing, effects: [{ set: { refine_feedback: "{{ slots.feedback }}", cycle: "{{ world.cycle + 1 }}" }}]}]
    restart_from:  [{ target: <phase>_executing, effects: [{ set: { restart_from_stage: "{{ slots.stage }}", cycle: 0 }}]}]
    quit:          [{ target: "@exit:abandoned" }]
```

This shape MUST be identical across all seven rooms (only `<phase>` and
`<next-room>` vary). Two things to flag for the bugfix-story author:

- The `emit_intent:` effect ships end-to-end (machine dispatch +
  orchestrator post-bind re-evaluation; see
  `internal/machine/machine.go::DispatchPostBindEmits` and
  `internal/orchestrator/orchestrator.go::settlePostBindEmits`).
  The dispatcher is depth-capped at `machine.EmitIntentMaxDepth` (= 8)
  so a misbehaving LLM that returns a self-cycling verdict fails loud
  rather than spinning. The author writes the shape above verbatim;
  no per-mode YAML forks.
- The `bind: { llm_verdict: "submitted" }` syntax follows the existing
  `host.oracle.ask_with_mcp` bind convention (see `internal/host/oracle_ask_with_mcp.go`).
  Because `bind:` lands at orchestrator-dispatch time (not machine-time),
  the emit_intent's `when:` guard is re-evaluated after the host call
  completes — that's what makes the LLM-judge → auto-accept hop work
  in one externally-initiated turn.
- `relevant_world` lists every key the state's view + on_enter touches so
  the TUI can subscribe properly.

## 7. Visible rooms (Wave 1 happy-path set)

Phase 1 ships **only the happy path** — no cycle budgets, no
`restart_from_stage` plumbing beyond the intent landing, only
`accept` / `refine` / `quit` plus `restart_from` as a no-op-target stub.

Each room has an `_executing` state (auto-runs the phase, binds the artifact)
and an `_awaiting_reply` state (the checkpoint per §6).

| Room | Next on `accept` |
|---|---|
| `idle` | `reproducing_executing` (via intent `start`) |
| `reproducing_executing` → `reproducing_awaiting_reply` | `proposing_executing` |
| `proposing_executing` → `proposing_awaiting_reply` | `implementing_executing` |
| `implementing_executing` (no checkpoint) | `testing_executing` |
| `testing_executing` → `testing_awaiting_reply` | `reviewing_executing` |
| `reviewing_executing` (no checkpoint) | `validating_executing` |
| `validating_executing` → `validating_awaiting_reply` | `done_executing` |
| `done_executing` → `done_awaiting_reply` | `@exit:done` |

Exits (used by parent stories that import `bugfix`):

```yaml
exits:
  done:
    description: "Pipeline succeeded; handoff to pr-refinement."
    requires: [done_artifact]
  abandoned:
    description: "User or LLM bailed."
```

Standalone (no parent) Wave 1 just terminates at `@exit:done`.

## 8. Flow fixtures Wave 1 ships

Under `stories/bugfix/flows/`:

| Flow | Judge mode | Expected outcome |
|---|---|---|
| `happy_human.yaml` | `human` | accept at every checkpoint → done |
| `happy_llm.yaml` | `llm` | LLM auto-accepts with confidence 0.9 → done |
| `happy_llm_then_human.yaml` | `llm_then_human` | LLM auto-accepts → done |
| `llm_uncertain_bails_to_human.yaml` | `llm_then_human` | LLM verdict.confidence=0.5 → state holds, human types accept → done |
| `refine_once_then_accept.yaml` | `human` | reproducing: refine → re-execute → accept → done |
| `quit_at_proposing.yaml` | `human` | quit → @exit:abandoned |
| `llm_strict_rejects.yaml` | `llm` | LLM uncertain → no human → flow expects timeout / loop break |
| `mixed_judge_swap.yaml` | starts `llm_then_human`, halfway flips to `human` via world mutation | demonstrates mid-run mode swap |

That's 8 flows minimum; aim for 10–12 to cover edge cases.

## 9. What Wave 1 does NOT include

These are explicit non-goals for Wave 1 (they land in Wave 2):

- `stories/pr-refinement/`, `stories/dev-story/`, `stories/kitsoki-dev/`
- `stories/implementation/`, `stories/code-review/`, `stories/cypilot/`
- `internal/host/` handlers for github / cypilot_artifacts / jira
- The `kitsoki bug create` CLI surface (lives in
  `bug-format-proposal.md`'s Phase A; consumed but not produced by Wave 1)
- `issues/bugs/README.md` and any seed bugs (Wave 2 dogfood task)
- Cycle budgets, full `restart_from_stage` semantics, `quick_fix` /
  `skip_to_pr` / `full_pipeline` intent shortcuts
- Provider sync to external trackers (`external:` frontmatter handling)
- The `bug create` integration with kitsoki-bug-reporter agent

## 10. Filesystem contract — who touches what

Wave 1 has three independent slices. They never touch the same file.

### Slice α — bugfix story author
- Creates: `stories/bugfix/{app.yaml, README.md, rooms/*.yaml, prompts/*.md, schemas/judge_verdict.json, flows/*.yaml}`
- Reads (does not modify): `stories/robbery/`, `stories/oregon-trail/`,
  `testdata/apps/dev-story/rooms/bugfix.yaml`, `docs/imports.md`,
  this contract.
- Test: `kitsoki test flows stories/bugfix/app.yaml`

### Slice β — provider host handlers author
- Creates: `internal/host/{localfiles_ticket.go, localfiles_ticket_test.go,
  git_vcs.go, git_vcs_test.go, local_ci.go, local_ci_test.go,
  git_worktree.go, git_worktree_test.go, append_file_transport.go,
  append_file_transport_test.go, inbox_add.go, inbox_add_test.go}`
- Modifies: `internal/host/handlers.go` (adds registrations in
  `RegisterBuiltins`)
- Reads: `internal/host/{handlers.go, transport_post.go, oracle_ask.go,
  host.go}` for handler conventions; `internal/inbox/inbox.go`,
  `internal/transport/transport.go`, `internal/workspace/workspace.go` for
  existing service APIs; `bug-format-proposal.md` for the bug file schema;
  this contract.
- Test: `go test ./internal/host/...`

### Slice γ — judge harness author
- Creates: `internal/judges/{judges.go, judges_test.go}` —
  provides a `RunJudge(ctx, prompt, schema, context) (Verdict, error)`
  function that wraps `host.oracle.ask_with_mcp` and returns a typed
  `Verdict` struct. The wrapper validates the structured response against
  the schema, returns a clear error on parse failure, and emits a typed
  `Verdict { Verdict, Intent, Reason string; Confidence float64 }`.
- Creates: `stories/bugfix/prompts/judge_reproducing.md` (and one per
  checkpointed room — copy/paste with the artifact name swapped). Slice α
  may choose to author the per-phase prompts instead; this is a soft
  boundary — coordinate via the prompts/ subtree (one prompt per file,
  no overlap on file names).
- Reads: `internal/host/oracle_ask_with_mcp.go`, `internal/mcp/validator.go`,
  this contract.
- Test: `go test ./internal/judges/...`

## 11. After Wave 1

The integration test that closes Phase 1 is `kitsoki test flows
stories/bugfix/app.yaml` passing all ~10 flows. Once green:

- Wave 2 fans out on Phases 2–6 (pr-refinement, dev-story, kitsoki-dev,
  implementation, code-review, cypilot, github provider).
- The runtime gaps surfaced by Slice α (e.g. `emit_intent:` /
  `when:`-on-`on_enter`) get fixed in a focused follow-up before Wave 2,
  if they are not yet supported.

See proposal §8 for the full Phase 2–8 plan.

---

# Wave 2 contract additions (Phase 2 — pr-refinement + dev-story hub)

Wave 1 shipped `stories/bugfix/` plus the six base host_interfaces and
the canonical §6 checkpoint shape. Wave 2 (this section) adds the
`stories/pr-refinement/` first-class story and the `stories/dev-story/`
engineer's-day hub that imports bf + pr. The additions below are
strictly additive — every Wave 1 contract clause still holds.

## W2.1 — New iface op: `vcs.merge`

`pr-refinement`'s `merge_executing` room calls `iface.vcs.merge` to
land the PR after CI passes. The op extends contract §2.2's `vcs`
interface:

```yaml
vcs:
  operations:
    merge:
      input:  { pr_id: string, strategy: string }   # strategy: squash | merge | rebase
      output: { ok: bool, sha: string }
```

The default `host.git` handler backs it through the registry's
prefix-fallback (one stub returning `{ok, sha}` satisfies the call in
flow fixtures). A `host.github` rebind in Wave 3 will shell out to
`gh pr merge --<strategy>`. A `host.bitbucket` rebind in cyber-repo
hits the merge endpoint.

The op is **PR-refinement-owned**: per proposal §10 question 6
(pragmatic reading), the merge lives inside pr-refinement and the
parent story consumes the `merged` exit. Stories that need a separate
merge confirmation can refine instead of accept at
`merge_awaiting_reply`, falling back to ci_monitoring.

## W2.2 — `pr-refinement` world keys

The new keys declared in `stories/pr-refinement/app.yaml`:

```yaml
world:
  # PR identity
  pr_id:            { type: string, default: "" }
  pr_url:           { type: string, default: "" }
  pr_title:         { type: string, default: "" }
  pr_body:          { type: string, default: "" }

  # CI state (poll output)
  ci_state:         { type: string, default: "" }    # pending | success | failure | error
  ci_attempts:      { type: int,    default: 0 }
  ci_log:           { type: string, default: "" }
  ci_failed_checks: { type: string, default: "" }

  # Review-comment state
  pr_comments:      { type: string, default: "" }    # JSON-ish blob from iface.vcs.pr_status
  pending_comments: { type: int,    default: 0 }

  # Pipeline control
  ci_poll_budget:   { type: int,    default: 5 }
  merge_strategy:   { type: string, default: "squash" }

  # New checkpoint artifact
  diagnose_artifact: { type: object, default: {} }

  # Close-out
  merge_sha:        { type: string, default: "" }
  status:           { type: string, default: "open" }   # open | merged | abandoned
```

The existing Wave 1 keys (`judge_mode`, `judge_confidence_threshold`,
`cycle`, `refine_feedback`, `llm_verdict`, `thread`, `ticket_id`,
`ticket_title`, `workdir`, `base_branch`, `feature_branch`) carry
straight through — pr-refinement uses the same vocabulary as bugfix.

## W2.3 — New schemas

| File | Purpose |
|---|---|
| `stories/pr-refinement/schemas/judge_verdict.json` | Identical to bugfix's `judge_verdict.json` (canonical §5 schema). Verbatim duplicate so pr-refinement can be loaded without a bugfix dependency. |
| `stories/pr-refinement/schemas/diagnose_artifact.json` | New. `{ summary_title, summary_markdown, root_cause, fix_description, affected_files, failing_checks, confidence, reasoning }`. Produced by `diagnose_executing` and judged at `diagnose_awaiting_reply`. |

## W2.4 — New exits

pr-refinement adds three named return points:

| Name | requires | Description |
|---|---|---|
| `merged` | `pr_url` | PR landed. Parent story consumes. |
| `abandoned` | (none) | User or LLM bailed. |
| `pushback_resolved` | (none) | Review pushback addressed; reserved for Wave 3 re-review loops. Wave 2's flows do not exercise it. |

## W2.5 — `exports.intents:` on bugfix and pr-refinement

Wave 1's `stories/bugfix/app.yaml` and Wave 2's
`stories/pr-refinement/app.yaml` both now declare `exports.intents:`
so importing parent stories (dev-story, kitsoki-dev) can lift
imported intents into the parent's bare intent table via
`imports.<alias>.intents.import`. This is a docs-only change —
no behaviour shifts, just unlocks a Wave 2 surface.

```yaml
# stories/bugfix/app.yaml
exports:
  intents: [start, proceed, accept, refine, restart_from, quit, look]

# stories/pr-refinement/app.yaml
exports:
  intents: [open, monitor, proceed, retry, resolve, merge_now, accept, refine, quit, look]
```

## W2.6 — Dev-story import shape

`stories/dev-story/app.yaml` ships the canonical hub composition.
The bf → pr handoff is one import edge: bf's `@exit:done` writes
`pr_title` / `pr_body` in the parent via the importer's `exits.done.set`
projection (read from `world.bf__done_artifact.summary_title` /
`summary_markdown`), then transitions into the `pr` compound (entry:
`open_pr`). pr's own `world_in:` projects those parent keys into
`pr__<key>` and the pr-refinement room chain runs.

```yaml
# stories/dev-story/app.yaml (excerpt)
imports:
  bf:
    source: ../bugfix
    entry: idle
    world_in: { ticket_id: "{{ world.ticket_id }}", … }
    intents: { import: [start] }       # only bf-unique bare names
    exits:
      done:      { to: pr,   set: { pr_title: "{{ world.bf__done_artifact.summary_title }}", pr_body: "{{ world.bf__done_artifact.summary_markdown }}" } }
      abandoned: { to: main, set: { status: "abandoned" } }

  pr:
    source: ../pr-refinement
    entry: open_pr                     # bypass pr-refinement's standalone-only idle
    world_in: { ticket_id: "{{ world.ticket_id }}", pr_title: "{{ world.pr_title }}", … }
    intents: { import: [open, monitor, retry, resolve, merge_now] }
    exits:
      merged:           { to: main, set: { status: "merged", last_pr_url: "{{ world.pr__pr_url }}" } }
      abandoned:        { to: main, set: { status: "abandoned" } }
      pushback_resolved:{ to: main }
```

Two intent-import constraints learned in Wave 2:

1. **Collision rule.** A parent's `intents.import: [X]` lifts the
   child's `X` to the parent's bare intent table. The loader rejects
   the lift if the bare name already exists in the parent OR has
   already been lifted by a previous import. dev-story imports only
   the *unique* bare names from each child (bf: `start`; pr:
   `open, monitor, retry, resolve, merge_now`). Overlapping names
   like `accept` / `refine` / `proceed` / `quit` / `look` stay
   prefixed (`bf__accept`, `pr__accept`).

2. **Dispatch via prefixed name.** Inside an imported child state,
   the on-arc was rewritten to the prefixed name (`bf__accept` for
   a child arc that authored `accept:`). The runtime dispatcher
   routes through that prefixed name; the operator types
   `bf__accept` (or `pr__accept`) when inside the imported
   compound. The `intents.import` lift is purely for parent-level
   menu surfaces (e.g. type-ahead completion); it does not change
   the on-arc dispatch path.

## W2.7 — `entry: open_pr` skips pr-refinement's standalone idle

pr-refinement ships an `idle` state as its `root:` for standalone
runs and flow fixtures (the `kitsoki test flows` seedInitialState
path bypasses on_enter; an idle entry lets the first turn fire
`open_pr.on_enter` via a real transition). When dev-story imports
pr-refinement with `entry: open_pr`, the import compound drills
straight into open_pr — the standalone idle state is unreachable
from the importer.

This pattern (a standalone-only idle entry for flow fixtures + a
real entry state for importers) is reusable for any future
sub-story whose entry runs material on_enter chains.

## W2.8 — Runtime quirks confirmed (no contract change)

The runtime quirks called out in the Wave 1 follow-up landed and
continue to behave as the contract anticipates. For Wave 2 author
reference:

- `bind:` lands at orchestrator-dispatch time, not machine-time.
  `when:` guards on a subsequent `on_enter` effect that read the
  bound value DEFER via a post-bind re-evaluation pass. The bf
  story's checkpoint shape exercises this; pr-refinement's diagnose
  checkpoint reuses the exact pattern.
- Views render BEFORE binds. Any view template referencing a bind
  target (e.g. `world.pr_id`) must use `?? "(pending)"` defaults.
- Parallel-state `emit_intent` is rejected by the runtime. Wave 2's
  pr-refinement and dev-story hub do not use parallel encoding.

## W2.9 — What Wave 2 does NOT include

Explicit non-goals for Wave 2 (deferred to Wave 3+):

- `stories/kitsoki-dev/` instance with `host.local_files.*` bindings
  (Wave 3 — Phase 3 of the proposal). The dogfood loop closes there.
- `stories/implementation/`, `stories/code-review/`, `stories/cypilot/`
  sub-stories (Wave 3 — Phases 5–6). dev-story's rooms for these are
  Wave-3 stubs that route back to `main`.
- A `pushback_resolved` exit consumer in pr-refinement's room graph
  (the exit is declared but no in-flow path produces it; Wave 3
  re-review loops will).
- Retiring `testdata/apps/dev-story/`. The stub still backs
  `internal/app/loader_metamode_test.go` and several flow tests;
  Wave 3 retires it once no test references it.

## W2.10 — Wave 2 test surface (acceptance)

| Story | Flow count | All pass? |
|---|---|---|
| `stories/bugfix/` | 10 | yes (Wave 1 preserved) |
| `stories/pr-refinement/` | 4 | yes |
| `stories/dev-story/` | 4 | yes (includes bf → pr full-chain) |
| `stories/oregon-trail/` | 28 | yes (no regression) |

Plus `go test ./...` is fully green.

