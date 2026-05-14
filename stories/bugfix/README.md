# bugfix — general-purpose, provider-neutral bug-fix pipeline

A reusable kitsoki story implementing the Wave 1 / Slice α scope of
`docs/proposals/dev-story-bugfix-unify-proposal.md`. The seven visible
rooms (`idle → reproducing → proposing → implementing → testing →
reviewing → validating → done`) collapse the cyber-repo's 14-phase
autonomous pipeline into one state machine while keeping every
checkpoint shape identical across `human` / `llm` / `llm_then_human`
judge modes.

Standalone:

```
kitsoki run stories/bugfix/app.yaml
```

Imported (see Wave 2's `stories/dev-story/app.yaml` or
`stories/kitsoki-dev/app.yaml`).

## Contract

### Entry state

`idle` — the operator starts the pipeline by typing `start`. Set on
import via `entry: idle`.

### Exits

| Name | Description | `requires:` keys | Typical world_out |
|---|---|---|---|
| `done` | Pipeline succeeded; hand off to pr-refinement. | `done_artifact` | Parent stories project `done_artifact` into their own `pr_id` / `pr_url` after pr-refinement runs. |
| `abandoned` | User or LLM bailed (`quit`). | (none) | Parent stories usually route to a `main` / inbox state. |

Standalone (no parent) load synthesises `__exit__done` and
`__exit__abandoned` terminals so `kitsoki run` and `kitsoki test flows`
both terminate cleanly.

### Visible rooms

| Room | Substates | Checkpoint? | On `accept` |
|---|---|---|---|
| `idle` | one atomic | n/a | `reproducing_executing` (via intent `start`) |
| `reproducing` | `_executing`, `_awaiting_reply` | yes — `reproduction_artifact` | `proposing_executing` |
| `proposing` | `_executing`, `_awaiting_reply` | yes — `propose_fix_artifact` | `implementing_executing` |
| `implementing` | `_executing` only | no | `testing_executing` (via `proceed`) |
| `testing` | `_executing`, `_awaiting_reply` | yes — `implement_review_artifact` | `reviewing_executing` |
| `reviewing` | `_executing` only | no | `validating_executing` (via `proceed`) |
| `validating` | `_executing`, `_awaiting_reply` | yes — `validate_artifact` | `done_executing` |
| `done` | `_executing`, `_awaiting_reply` | yes — `done_artifact` | `@exit:done` |

### `world_in:` keys (parent → child)

The importer projects these from its own world. All have type+default
in `app.yaml`'s `world:` block so the child loads standalone for tests.

| Key | Type | Used by | Default |
|---|---|---|---|
| `ticket_id` | string | Every checkpoint's `phase_id:` and post title. | `""` |
| `ticket_title` | string | Views / artifact prompts. | `""` |
| `ticket_url` | string | Returned to parent on completion. | `""` |
| `thread` | string | The transport's thread identifier (file path / Jira key / chat ID). | `""` |
| `workspace_id` | string | `iface.workspace.sync` arg. | `""` |
| `workdir` | string | Most `iface.{vcs,ci}.*` calls. | `""` |
| `base_branch` | string | `iface.vcs.open_pr.base`. | `""` |
| `feature_branch` | string | `iface.vcs.branch.name`. | `""` |
| `bugfix_mode` | string | `full` (walk every room) \| `quick` (Wave 2 shortcut). | `full` |
| `judge_mode` | string | `human` \| `llm` \| `llm_then_human` — see Judge polymorphism below. | `human` |
| `judge_confidence_threshold` | float | Floor for auto-firing the LLM's verdict (Wave 2 — runtime gap). | `0.8` |
| `allowed_authors` | string (CSV) | Authorisation filter for reply intents arriving over the transport. | `""` |

### `world_out:` keys (child → parent on exit)

| Key | Type | Description |
|---|---|---|
| `done_artifact` | object | Postmortem-style close-out (see `schemas/done_artifact.json`). Parent stories carry this into pr-refinement. |
| `reproduction_artifact` | object | Evidence the bug is reproducible. |
| `propose_fix_artifact` | object | The proposed fix. |
| `implement_review_artifact` | object | Test review + status. |
| `validate_artifact` | object | Full-env validation outcome. |
| `status` | string | `fixed` after `@exit:done`; left as `"open"` on `@exit:abandoned`. |
| `cycle` | int | Total refinement cycles consumed. |
| `pr_id`, `pr_url`, `ci_state` | string | Held for the pr-refinement handoff (populated by Wave 2). |

### Intent surface

| Intent | Slots | Description |
|---|---|---|
| `start` | — | Begin the pipeline from `idle`. |
| `proceed` | — | Advance from an `_executing` room into its `_awaiting_reply` checkpoint. |
| `accept` | (opt) `author`, `feedback` | Accept the current checkpoint artifact; advance to the next room. |
| `refine` | (opt) `feedback` | Re-execute the current room with feedback in `world.refine_feedback` and `cycle++`. |
| `restart_from` | (opt) `stage` | Reset to an earlier room (Wave 1 lands the intent on the previous-room's `_executing` with `restart_from_stage` set; full plumbing in Wave 2). |
| `quit` | — | Bail; exits via `@exit:abandoned`. |
| `look` | — | Re-render the current view. |

### `host_interfaces:` contract

The story declares six capability surfaces. Operation names and I/O
shapes are fixed by contract §2 of
`docs/proposals/notes/dev-story-implementation-contract.md`. The
`default:` value names the standalone binding (provider-neutral local
files / git); parent stories rebind via `imports.<alias>.host_bindings`.

| Iface | Ops | Default binding |
|---|---|---|
| `ticket` | `search`, `get`, `comment`, `transition`, `list_mine` | `host.local_files.ticket` |
| `vcs` | `branch`, `diff`, `commit`, `push`, `open_pr`, `pr_status`, `pr_comment` | `host.git` |
| `ci` | `run_tests`, `build`, `remote_status` | `host.local` |
| `workspace` | `list`, `get`, `create`, `sync` | `host.git_worktree` |
| `transport` | `post` | `host.append_to_file` (kitsoki-dev appends to the local bug file) |
| `inbox.add` | — | always-on bare host call, NOT an iface (per contract §2.6) |

Rebinding from an importer is straightforward — see proposal §5.1–5.3
worked examples. The cyber-repo flavor will rebind to
`{ticket: host.jira, vcs: host.bitbucket, ci: host.jenkins,
workspace: host.workspace_manager, transport: host.jira_comment}`.

### Host requirements

Standalone Wave 1 needs every iface's default handler PLUS
`host.inbox.add` and `host.oracle.ask_with_mcp`. The flow fixtures
stub them all with canned envelopes; Slice β ships the real handlers
in `internal/host/`.

| Handler | Status | File |
|---|---|---|
| `host.local_files.ticket` | Slice β (in flight) | `internal/host/localfiles_ticket.go` |
| `host.git` | Slice β (in flight) | `internal/host/git_vcs.go` |
| `host.local` | Slice β (in flight) | `internal/host/local_ci.go` |
| `host.git_worktree` | Slice β (in flight) | `internal/host/git_worktree.go` |
| `host.append_to_file` | Slice β (in flight) | `internal/host/append_file_transport.go` |
| `host.inbox.add` | Slice β (in flight) | `internal/host/inbox_add.go` |
| `host.oracle.ask_with_mcp` | already shipped | `internal/host/oracle_ask_with_mcp.go` |

The host registry's prefix-fallback lets each "default" handler back
every op on the iface; per-op handlers can be added later without
touching the YAML.

## Judge polymorphism

The defining property of this story: every `_awaiting_reply` state
runs **the same `on_enter` chain** in all three judge modes. The flag
is `world.judge_mode`:

| Mode | Behaviour at every checkpoint |
|---|---|
| `human` | Post + inbox-mirror; wait for an explicit reply intent. (No LLM call.) |
| `llm` | Post + inbox-mirror + run the LLM-judge. The verdict lands in `world.llm_verdict`; an operator-driven harness fires the verdict's intent. If verdict is uncertain in strict `llm` mode, the state holds until an operator intervenes. |
| `llm_then_human` | Post + inbox-mirror + run the LLM-judge. The verdict lands in `world.llm_verdict`; if a Wave 2 `emit_intent:` effect ships, it auto-fires the verdict's intent above the confidence threshold. Until then, a human picks it up the same way as `human` mode. |

The judge polymorphism is a single host call per checkpoint, gated by
`when:` — **not** a fork in the state graph. The seven
`_awaiting_reply` states have **identical** `on_enter` shapes
(contract §6) — only `<phase>` and the next-room target vary.

## Wave 1 limitations

What is NOT in Wave 1 (deferred to Wave 2+):

- **`emit_intent:` effect.** Contract §6 step 4 calls for an effect
  that auto-fires the LLM-judge's verdict-intent when confidence
  exceeds the threshold. The runtime does not yet recognise
  `emit_intent:` — the verdict still lands in `world.llm_verdict` for
  an operator / external harness to act on. See the Slice α report
  for the exact runtime gap.
- **`@exit:done` parent (pr-refinement import).** Standalone Wave 1
  exits at `__exit__done`. Wave 2 imports `stories/pr-refinement/`
  for the tail (CI watch, comment resolution, merge).
- **Cycle budgets.** `world.cycle` is incremented on `refine` but no
  budget caps the loop yet. Wave 2 (Phase 4 of the proposal) adds
  per-room budgets and the corresponding fallback arcs.
- **`restart_from_stage` plumbing.** Wave 1 lands the intent on the
  previous room's `_executing` with `restart_from_stage` set in world;
  Wave 2 wires the full jump table.
- **`quick_fix` / `skip_to_pr` shortcut intents.** Wave 2 (proposal
  §4.2) adds the bugfix-mode-gated shortcuts.

## File layout

```
stories/bugfix/
  app.yaml                                — manifest (this story's loadable surface)
  README.md                               — this file
  rooms/
    idle.yaml                             — pipeline parked
    reproducing.yaml                      — _executing + _awaiting_reply
    proposing.yaml                        — _executing + _awaiting_reply
    implementing.yaml                     — _executing only
    testing.yaml                          — _executing + _awaiting_reply
    reviewing.yaml                        — _executing only
    validating.yaml                       — _executing + _awaiting_reply
    done.yaml                             — _executing + _awaiting_reply
  prompts/
    reproducing_executing.md              — artifact-producing
    proposing_executing.md                — artifact-producing
    testing_executing.md                  — artifact-producing
    validating_executing.md               — artifact-producing
    done_executing.md                     — artifact-producing
    judge_reproducing.md                  — LLM-judge for reproducing_awaiting_reply
    judge_proposing.md                    — LLM-judge for proposing_awaiting_reply
    judge_testing.md                      — LLM-judge for testing_awaiting_reply
    judge_validating.md                   — LLM-judge for validating_awaiting_reply
    judge_done.md                         — LLM-judge for done_awaiting_reply
  schemas/
    judge_verdict.json                    — { verdict, intent, reason, confidence }
    reproducing_artifact.json
    proposing_artifact.json
    testing_artifact.json
    validating_artifact.json
    done_artifact.json
  flows/                                  — deterministic flow fixtures (host stubs only)
    happy_human.yaml                      — accept at every checkpoint (canonical)
    happy_llm.yaml                        — judge_mode=llm with confident verdict
    happy_llm_then_human.yaml             — judge_mode=llm_then_human, human-driven advance
    llm_uncertain_holds.yaml              — judge_mode=llm with uncertain verdict
    refine_once_then_accept.yaml          — reproducing: refine → re-execute → accept
    quit_at_proposing.yaml                — quit at proposing → @exit:abandoned
    mixed_judge_swap.yaml                 — start llm_then_human, flip to human mid-run
```

## See also

- [`docs/proposals/dev-story-bugfix-unify-proposal.md`](../../docs/proposals/dev-story-bugfix-unify-proposal.md)
  — the full design.
- [`docs/proposals/notes/dev-story-implementation-contract.md`](../../docs/proposals/notes/dev-story-implementation-contract.md)
  — Slice α / β / γ contract.
- [`docs/imports.md`](../../docs/imports.md) — the imports authoring
  reference for parent stories that wrap `bugfix`.
- [`stories/robbery/`](../robbery/) — the canonical importable
  sub-story (smaller, used by `oregon-trail` as an imports demo).
