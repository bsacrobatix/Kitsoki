# prd — PRD-authoring operator story

A reusable kitsoki story that turns a free-form idea (plus any existing
upstream requirement docs) into a PRD markdown document. It is a
pipeline-shaped operator story, structurally identical to
[`stories/bugfix/`](../bugfix/) and [`stories/dev-story/`](../dev-story/);
the novelty is the **document** artifact, a **conversational idea
discovery** intake, a **multi-round clarification** step whose transcript
accumulates across rounds, and two **confirm-gated review rooms** (the
formalized `brief` and a curated doc `references` list, both persisted to
`.artifacts/`) that the operator signs off on before the PRD is written.

No engine / widget changes — everything composes existing mechanisms (a
conversational chat room, `host.oracle.*`, `host.artifacts_dir`, the
cycle-budget refine loop, named agents, tracing).

## Using it

### Launch

```sh
# Standalone, fast/cheap model (default haiku), against the current dir.
kitsoki run stories/prd/app.yaml

# Author a real PRD with a higher-quality model, rooted in another repo so
# the agents read that tree's docs and write the PRD there.
kitsoki run stories/prd/app.yaml --warp scenarios/hyperspot.yaml --claude-model opus

# Resume the session you were last in (the discovery chat persists).
kitsoki run stories/prd/app.yaml --continue
```

- **Harness** auto-selects the local `claude` binary when it's on `PATH`
  (no API key, no per-call cost); otherwise set `ANTHROPIC_API_KEY`. Pass
  `--claude-model opus` for the drafting-quality you'd want on a real PRD —
  the default `haiku` is for cheap walkthroughs.
- **Where it runs / writes** — everything is relative to `world.workdir`
  (default `.`). To author against another repo without `cd`-ing, use a
  warp basis (see `scenarios/hyperspot.yaml`) or set `workdir` /
  `upstream_paths` there. Seed `upstream_paths` (space/comma-separated
  files or dirs) so the analyst and author read your existing requirement
  docs without you having to mention them in chat.
- **Sessions + traces** persist automatically under the nearest
  `.kitsoki/sessions/`; `--db` overrides the session DB. Pretty-print a run
  with `kitsoki trace <path>`, or watch it live with `kitsoki status`.

### Walkthrough — what you type at each step

The session opens **parked at `idle`**. The flow is a five-room pipeline;
you drive it with free text (an LLM maps what you type to the room's
intents — you don't memorize commands):

1. **`idle` — talk the idea through.** Just describe what you want to
   build; the `interviewer` asks follow-ups and you reply, conversational,
   for as long as you like. When the problem/users/scope feel covered, type
   **`ready`** (or `start`) — that distills the conversation into the
   formal idea and moves on.
2. **`clarifying` — answer the questions.** The `analyst` posts a numbered
   list of the gaps that most change the PRD. **Answer one at a time by
   number** — e.g. `3 our enterprise tenants only`. The screen tracks which
   are answered. When you've covered enough, type **`submit`** (or `skip`
   to move on with what you have). `regenerate` re-asks; `quit` bails.
3. **`brief` — confirm the inputs.** A read-only review of the formalized
   idea + every clarification, written to `.artifacts/prd_brief.md`. Pick
   **`confirm`** to proceed, **`clarify`** to ask another round *keeping*
   your answers, or **`restart from clarifying`/`idle`** to start that
   stage over.
4. **`references` — confirm the prior art.** The `researcher` searches the
   workdir's **docs** (not code) and proposes the documents the PRD should
   build on, each with the section(s) and a rationale, written to
   `.artifacts/prd_references.md`. **`confirm`** to draft, **`refine "drop
   the rate-limit doc; also cite docs/auth.md §Tokens"`** to edit the list
   in place, or **`regenerate`** to search fresh.
5. **`drafting` — review the PRD.** The `author` writes the full document
   to **`.artifacts/prd.md`** and shows a digest. **`accept`** to finish,
   **`refine "<what to change>"`** to re-draft with the same inputs, or
   **`clarify`** if the draft revealed the inputs were incomplete (adds a
   Q&A round, then re-drafts).

**Outputs** all land in `{{ workdir }}/.artifacts/`: `prd_brief.md`,
`prd_references.md`, and the PRD itself `prd.md`. Each review screen shows
its saved path prominently so you can open or edit the file on disk
directly.

## Story graph

```
idle ─start─▶ clarifying ─submit_answers/skip─▶ brief ─confirm─▶ references ─confirm─▶ drafting
 │ (chat)         │ (decide → questions)          │ (.artifacts/    │ (decide →           │ (task → .artifacts/prd.md)
 │ ◀─discuss      │ ◀─answer n=… (self)           │  prd_brief.md)  │  doc references)    │
 │                ├─regenerate ─▶ clarifying       ├─confirm         ├─confirm             ├─accept ─▶ @exit:done
 │                └─quit ─▶ @exit:abandoned        ├─restart_from    ├─regenerate (budget) ├─refine ─▶ drafting (budget→abandoned)
 │                     ▲                           └─quit            ├─restart_from        ├─clarify ─▶ clarifying ◀─ another round
 │                     └──────────────────────────── clarify ───────┴─────────────────────┤
 └─quit ─▶ @exit:abandoned                                                                 ├─ restart_from ─▶ idle | clarifying
                                                                                           └─ quit ─▶ @exit:abandoned
```

`idle` is a **conversation**, not a form: the operator talks the idea
through with an `interviewer` agent (`discuss` self-loops), and `start`
distills the conversation into `world.idea` before advancing.

`brief` and `references` are **confirm-gated review rooms** inserted
before the PRD is written:

- **`brief`** is *deterministic* — no oracle call. It composes the brief
  out of what the first two phases already produced (`world.idea` from
  `idle` + the clarification transcript from `clarifying`), writes it to
  `{{ workdir }}/.artifacts/prd_brief.md` via `host.artifacts_dir`, and
  gates on `confirm`.
- **`references`** runs the `researcher` (`host.oracle.decide`, docs-only)
  to curate the existing documents the PRD must build on — each with the
  specific section(s) and a one-line rationale — also persisted to
  `.artifacts/prd_references.md`. The confirmed list is handed to the
  author so the PRD is drafted against the cited sections. Two ways to
  re-run it: **`refine`** revises the *current* list in place (keep what
  applies; add / drop / re-scope per your instruction), while
  **`regenerate`** searches fresh. (`references_revise` carries that
  distinction into the researcher prompt.)

From both review rooms (and `drafting`) the operator can go **back to
clarifying** two ways — the distinction matters:

- **`clarify`** (non-destructive) — *keeps* the answers so far
  (`clarification_log` is preserved); only this round's working answer
  state is cleared and the analyst re-runs to **refine/combine the prior
  questions and ask only what's still missing**. Use this to tweak or
  extend the questions without losing anything.
- **`restart_from clarifying`** (destructive) — *discards* the
  clarification record and re-questions from scratch.

Five rooms (`idle`, `clarifying`, `brief`, `references`, `drafting`), two
exits (`done`, `abandoned`). `drafting` is the checkpoint room — same
shape as every `bugfix` phase.

## Contract

### Entry state

`idle` — the operator talks the idea through with the `interviewer` agent
(a conversation, not a form), then types `start` (or "ready"). Set on
import via `entry: idle`.

### Exits

| Name | Description | `requires:` keys |
|---|---|---|
| `done` | PRD accepted; `prd_artifact` is final. | `prd_artifact` |
| `abandoned` | Operator or LLM bailed (`quit`), or a cycle budget was exhausted. | (none) |

Standalone load synthesises `__exit__done` / `__exit__abandoned`
terminals so `kitsoki run` and `kitsoki test flows` terminate cleanly.

### Rooms

| Room | On enter | Checkpoint? | On `accept` / advance |
|---|---|---|---|
| `idle` (conversational) | `host.chat.resolve` (get-or-create) opens the discovery chat; `interviewer` (`host.oracle.converse`) replies per `discuss` turn | no — `discuss` self-loops the conversation | `clarifying` (via `start`, which distills the chat into `world.idea`) |
| `clarifying` | `analyst` (`host.oracle.decide`) → `clarifications` | no — operator answers questions one at a time (`answer n=…`) | `brief` (via `submit_answers` or `skip`) |
| `brief` | `host.artifacts_dir` writes `.artifacts/prd_brief.md` from `world.idea` + `world.clarification_log` (deterministic — no oracle) | yes — operator `confirm`s the brief | `references` (via `confirm`); `clarify` re-questions keeping the record; `restart_from` discards |
| `references` | `researcher` (`host.oracle.decide`, docs-only) → `references`; `host.artifacts_dir` writes `.artifacts/prd_references.md` | yes — operator `confirm`s the list | `drafting` (via `confirm`); `refine` revises the list in place; `regenerate` searches fresh (both budgeted); `clarify` re-questions keeping the record |
| `drafting` | `author` (`host.oracle.task`) writes the PRD → `prd_artifact` (reads the confirmed `references`); optional `judge` | yes — `prd_artifact` | `@exit:done` (via `accept`) |

### World contract

Every key is declared with a type + default in `app.yaml` so the story
loads standalone for tests. Parent stories project the intake keys via
`world_in:`.

| Key | Type | Description | Default |
|---|---|---|---|
| `idea` | string | The distilled pitch (set when the discovery chat ends). | `""` |
| `idea_chat_id` / `idea_chat_title` | string | Persistent discovery-chat thread. | `""` |
| `idea_message` / `idea_answer` | string | The operator's latest message / interviewer's latest reply. | `""` |
| `idea_session_id` | string | Claude session id, for resume across turns. | `""` |
| `idea_turns` | int | Discovery-chat turn count (view only). | `0` |
| `upstream_paths` | string | Space/comma-separated files or dirs the agents read (seed via warp, or mention in the chat). | `""` |
| `workdir` | string | Where upstream lives + the PRD is written; pins each oracle's `working_dir`. | `"."` |
| `output_path` | string | PRD path, relative to `workdir`. Defaults under `.artifacts/` alongside the brief + reference-list artifacts. | `".artifacts/prd.md"` |
| `clarifications` | object | This round's `decide` result: `{ questions: [{id, question, why}] }`. | `{}` |
| `clarification_answers` | string | This round's replies, accumulated one `Qn: …` line per answered question (newest last). | `""` |
| `answered_count` | int | Questions answered this round; drives the "Answered so far (N/total)" readout. Reset on every new round. | `0` |
| `clarification_log` | string | Growing transcript of every prior round's Q&A (see below). | `""` |
| `brief_path` | string | Path the brief artifact was written to (`.artifacts/prd_brief.md`). | `""` |
| `references` | object | `researcher` result: `{ items: [{ path, sections, rationale }] }`. | `{}` |
| `references_path` | string | Path the reference-list artifact was written to (`.artifacts/prd_references.md`). | `""` |
| `references_cycle` | int | References re-run rounds consumed (`refine` or `regenerate`); caps via `references_budget`. | `0` |
| `references_budget` | int | Max references re-runs before `refine`/`regenerate` abandons. | `3` |
| `references_revise` | bool | `true` on a `refine` (revise the current list in place), `false` on a fresh `regenerate` — steers `prompts/references.md`. | `false` |
| `prd_artifact` | object | `task` result: `{ title, summary_markdown, file_path, confidence, needs_clarification, follow_up_questions }`. | `{}` |
| `refine_feedback` | string | Operator note carried into the next draft / question round. | `""` |
| `cycle` | int | Coarse global audit counter. | `0` |
| `clarifying_cycle` | int | Clarification rounds consumed; caps via `clarifying_budget`. | `0` |
| `clarifying_budget` | int | Max clarification rounds before `regenerate` abandons. | `3` |
| `drafting_cycle` | int | Draft refines consumed; caps via `drafting_budget`. | `0` |
| `drafting_budget` | int | Max draft refines before `refine` abandons. | `5` |
| `judge_mode` | string | `human` \| `llm` \| `llm_then_human`. | `human` |
| `judge_confidence_threshold` | float | Floor for auto-firing the judge's verdict. | `0.8` |
| `llm_verdict` | object | `{ verdict, intent, reason, confidence }` from the judge. | `{}` |
| `abandon_reason` | string | Structured reason set by an abandon arc. | `""` |
| `status` | string | `done` after `@exit:done`; `abandoned` on `@exit:abandoned`. | `""` |
| `thread` | string | Held for an optional future `transport.post` of the finished PRD. | `""` |

### Intent surface

| Intent | Slots | Description |
|---|---|---|
| `discuss` | `message` (req) | Send a free-text message in the idea-discovery conversation (self-loops `idle`). |
| `start` | — | From `idle`: distill the conversation into `world.idea` and advance to `clarifying`. |
| `answer` | `n` (req, int) · `text` (req) | Answer ONE question by its number; self-loops `clarifying` (does NOT regenerate). Appends a `Qn: …` line to `clarification_answers` and bumps `answered_count`. |
| `submit_answers` | (opt) `answers` | Advance to the `brief` review on the answers gathered so far; appends the round to `clarification_log` and increments `clarifying_cycle`. A pasted `answers` blob overrides the accumulated replies. |
| `skip` | — | Move on to the brief with what we have (no answers this round; no log append). |
| `confirm` | — | Confirm the current review step (`brief` → `references`, or `references` → `drafting`). |
| `regenerate` | (opt) `feedback` | Re-generate the current step's machine output FROM SCRATCH: in `clarifying`, re-ask this round's questions (`clarifying_cycle >= clarifying_budget` → `@exit:abandoned`); in `references`, a fresh doc search (`references_revise=false`; `references_cycle >= references_budget` → `@exit:abandoned`). Self-transition; re-fires the on_enter oracle call. |
| `accept` | — | From `drafting`: finish via `@exit:done` (re-pins `prd_artifact`, sets `status=done`). |
| `refine` | `feedback` (req) | Refine BUILDING ON the current artifact (not a fresh start): in `drafting`, re-draft with the same inputs (`drafting_cycle >= drafting_budget` → `@exit:abandoned`); in `references`, revise the current list in place (`references_revise=true`; `references_cycle >= references_budget` → `@exit:abandoned`). |
| `clarify` | — | **Non-destructive** back-to-clarifying from `brief` / `references` / `drafting`: **keeps** `clarification_log`, clears only this round's working answers, and the analyst refines/combines the prior questions and asks only what's still missing. The loop re-passes through `brief` + `references`. |
| `restart_from` | (opt) `stage` | **Destructive** redo, not extend: `idle` re-pitches (wipes the log); `clarifying` re-questions (discards the record). Offered from `brief`, `references`, and `drafting`. |
| `quit` | — | Bail; exits via `@exit:abandoned`. |
| `look` | — | Re-render the current view. |

### Host requirements

| Handler | Used by | File |
|---|---|---|
| `host.chat.resolve` | `idle` (get-or-create the discovery chat; idempotent across `on_enter` re-fires) | `internal/host/chat_handlers.go` |
| `host.oracle.converse` | `idle` (interviewer discovery chat + distill) | `internal/host/oracle_converse.go` |
| `host.oracle.decide` | `clarifying` (analyst), `references` (researcher), `drafting` (judge) | `internal/host/oracle_decide.go` |
| `host.oracle.task` | `drafting` (author, writes the PRD) | `internal/host/oracle_task.go` |
| `host.artifacts_dir` | `brief` (prd_brief.md), `references` (prd_references.md); `mode: replace` for `on_enter` idempotency | `internal/host/artifacts_dir_transport.go` |

`host.chat.*` needs a ChatStore wired into the session — `kitsoki run`
provides one (via `--db`); standalone flow fixtures stub it.

v1 does not post the finished PRD out-of-band; mirroring it to an inbox /
Confluence / a ticket is a one-line `iface.transport.post` effect on the
`accept` arc when wanted (see "Resolved decisions" below).

### Agents (persona table)

Named agents attribute model + token usage to each step in the trace.

| Persona | Verb | Tools | Role |
|---|---|---|---|
| `interviewer` | `converse` | `Read`, `Grep`, `Glob` | Runs the idea-discovery chat; helps the operator sharpen the problem, users, and scope. |
| `analyst` | `decide` | `Read`, `Grep`, `Glob` | Reads the idea + upstream docs + the transcript; asks only genuinely-new questions. |
| `researcher` | `decide` | `Read`, `Grep`, `Glob` | Searches the working dir's **docs** (not code) for prior art / constraints; curates the reference list with sections + rationale. |
| `author` | `task` | `Read`, `Grep`, `Glob`, `Write`, `Edit` | Writes the PRD markdown to disk (reads the curated references); produces `prd_artifact`. |
| `judge` | `decide` | (none) | Optional auto-advance gate (off by default). |

## The multi-round clarification loop

The `clarify` arc closes a loop back into `clarifying` for another Q&A
round, and each round **appends** to `clarification_log` rather than
overwriting it. The append is a string concat done in the
`submit_answers` effect, newest-round-first:

```
── Round N ──
Questions: {compact sorted-key JSON of world.clarifications}
Answers:   {this round's accumulated "Qn: …" replies}

{prior clarification_log}
```

Two mechanics make this work (both load-time gotchas — see the design
note in `rooms/clarifying.yaml`):

- **Leading the block with the literal `── Round …` divider** sidesteps
  the `RenderValue` short-circuit: a `set:` value that both *starts* with
  `{{` and *ends* with `}}` is evaluated as a typed expression (which
  fails to compile here), so the newest-round-first ordering is load-bearing.
- **`Questions: {{ world.clarifications }}` embeds** the object, which
  splices it as compact sorted-key JSON. A *bare* `key: "{{ world.obj }}"`
  would instead store the live map — fine for the `accept` re-pin of
  `prd_artifact`, wrong for a transcript line.

The analyst's prompt receives the accumulated log and is told to ask
**only new questions, or return an empty list** — that "only-new" framing
is what keeps round 2+ from re-asking resolved points.

`clarify` vs `refine` vs `restart_from` — three deliberately distinct
exits from the `drafting` checkpoint:

- `refine` keeps the same inputs, asks the author to revise the prose.
- `clarify` says "the inputs are incomplete" → another clarification
  round that *adds to* the transcript, then re-drafts.
- `restart_from clarifying` discards the clarification record and starts
  the questioning over (`idle` goes further: re-pitch from scratch).

The author can self-flag `needs_clarification: true` with
`follow_up_questions`; the `drafting` view raises a banner pointing the
operator at `clarify`, and the next round's analyst turns those follow-ups
into questions.

## Judge polymorphism

`drafting` runs the same `on_enter` chain in all three judge modes,
gated by `when:` — not a fork in the graph (the `bugfix` pattern):

| Mode | Behaviour at the draft checkpoint |
|---|---|
| `human` (default) | No judge call; the operator decides. |
| `llm` | Run the `judge`; when its verdict is not `uncertain` and `confidence >= judge_confidence_threshold`, `emit_intent:` auto-fires the verdict's intent the same turn. Otherwise the state holds. |
| `llm_then_human` | Same auto-fire path; falls through to the human view on an uncertain / low-confidence verdict. |

`emit_intent:` is depth-capped at `machine.EmitIntentMaxDepth` (= 8).

## Resolved design decisions

From the original design note (now retired), resolved as implemented:

1. **PRD output** — `oracle.task` writes the document to
   `{{ workdir }}/{{ output_path }}` (durable, gives `files_changed` in the
   trace) and returns a `summary_markdown` for the checkpoint view.
2. **Idea capture** — a conversational discovery chat (`oracle.converse`),
   not a form: a free-form pitch is awkward to type into one input field,
   so the operator talks it through and `start` distills it. **Clarification
   capture** is a structured numbered *list* the operator answers one at a
   time by number (`answer n=…`, e.g. "number 3 …"); the screen tracks which
   are answered and `submit_answers` advances on the accumulated replies (a
   pasted blob still overrides). Driven by typed free text rather than a menu
   selection because a choice-form param can fill only one slot, not the
   `n`+`text` pair.
3. **Upstream ingestion** — passed as `upstream_paths`; the `analyst` /
   `author` agents read them via `Read`/`Grep`, so their reads land as
   `oracle.tool_call` trace events (serving the "what files were used" ask).
4. **Sharing the PRD** — out of scope for v1; add an
   `iface.transport.post` (or `host.inbox.add`) effect on the `accept` arc.
5. **LLM judge** — the machinery is carried (cheap), default `human`.
6. **Review gates before drafting** — the `brief` and `references` rooms
   make the operator confirm the inputs before any PRD is written.
   - **The brief is deterministic** — it is *not* a new LLM synthesis. The
     first two phases already produced everything it needs (`world.idea`
     from `idle`, the clarification transcript from `clarifying`), so the
     room just composes them, persists to `.artifacts/prd_brief.md`, and
     gates. No oracle call, no cost.
   - **References uses `host.oracle.decide`** (the canonical
     structured-artifact verb), the same verb as `clarifying`. In flow
     fixtures the two `on_enter` deciders are kept apart by *what each
     binds* (`clarifying` → `.questions`, `references` → `.items`) — a
     questions-only stub leaves references' `.items` empty, a valid state —
     so no fixture asserting both shapes runs them in one session
     (`brief_references_path` starts at `brief` to isolate the researcher).
     Do **not** switch one room to a different oracle verb to dodge a
     stub-name collision.
   - **Artifacts land under `{{ workdir }}/.artifacts`** (passed as
     `artifacts_root`) so they sit in the operator's tree, and both writes
     use `mode: replace` so an `on_enter` re-fire overwrites rather than
     stacking duplicates.

## Flow fixtures

Deterministic, hermetic (host stubs only — no LLM). Run:

```
kitsoki test flows stories/prd/app.yaml
```

| Fixture | Proves |
|---|---|
| `happy_path.yaml` | Full path `idle → clarifying → brief → references → drafting → @exit:done`; the `brief`/`references` artifact writes dispatch. |
| `brief_references_path.yaml` | `brief → references → drafting → @exit:done`; the `references` list is bound + persisted (asserts content). Starts at `brief` so the only `decide` is the researcher's. |
| `clarify_from_brief.yaml` | `brief —clarify→ clarifying` PRESERVES `clarification_log` (vs `restart_from`, which wipes it); answering + submitting appends a second round (`clarifying_cycle` 1 → 2). |
| `references_refine.yaml` | `refine` sets `references_revise=true` (revise in place) and `regenerate` sets it `false` (fresh search); both budget via `references_cycle`; `confirm` clears the flag. |
| `answer_one_by_one.yaml` | `answer n=…` accumulates out-of-order; `submit_answers` advances to `brief` on the accumulated replies. |
| `refine_loop.yaml` | `refine` re-enters `drafting`; `drafting_cycle` + `refine_feedback` advance. |
| `multi_round_clarify.yaml` | Two `clarify → submit → confirm → confirm` rounds; `clarifying_cycle == 2` and `clarification_log` accumulates both rounds (newest first); passing through `brief`/`references` doesn't disturb the transcript. |
| `skip_to_brief.yaml` | `skip` from `clarifying` advances to `brief` with NO round recorded — `clarification_log` stays empty and `clarifying_cycle` does not move (the clean-move-on contrast to `submit_answers`). |
| `clarifying_regenerate.yaml` | `regenerate` re-asks: discards this round's working answers, carries `refine_feedback`, bumps `clarifying_cycle`; at `clarifying_budget` the next `regenerate` → `@exit:abandoned` (`clarifying_budget_exhausted`). |
| `restart_from_clarifying.yaml` | The DESTRUCTIVE back-path: `restart_from clarifying` WIPES `clarification_log` + every cycle counter (the deliberate contrast to `clarify_from_brief`, which preserves them). |
| `restart_from_idle.yaml` | `restart_from idle` from `drafting` re-pitches: resets the full pipeline (record + all cycles + `refine_feedback`) and lands back in `idle`. |
| `references_budget.yaml` | `refine`/`regenerate` at `references_cycle == references_budget` → `@exit:abandoned` (`references_budget_exhausted`) — the references analogue of `budget_exhausted`. |
| `budget_exhausted.yaml` | `refine` at `drafting_cycle == drafting_budget` → `@exit:abandoned` with `abandon_reason`. |
| `quit_from_references.yaml` | `quit` from a review room → `@exit:abandoned` stamped with the room-specific `abandon_reason` (`abandoned_at_references`). |
| `llm_judge.yaml` | Full path in `judge_mode: llm` with an *uncertain* verdict → HOLDS at `drafting`; also proves the three `host.oracle.decide` call sites (`analyst_questions`, `references_research`, `judge_verdict`) are stubbed apart by invoke `id:` via `by_call:`. |
| `judge_auto_accept.yaml` | The other judge half — a *confident, non-uncertain* verdict (`accept@0.92 ≥ threshold`) makes `drafting`'s `on_enter` `emit_intent: accept` the same turn, so a single `confirm` into `drafting` auto-advances to `@exit:done`. |

## File layout

```
stories/prd/
  app.yaml                 — manifest
  README.md                — this file
  rooms/
    idle.yaml              — idea discovery (conversational)
    clarifying.yaml        — generate + answer questions (multi-round)
    brief.yaml             — formalize + confirm the inputs (deterministic; writes .artifacts/prd_brief.md)
    references.yaml        — curate the doc reference list + confirm (writes .artifacts/prd_references.md)
    drafting.yaml          — write the PRD + checkpoint
  prompts/
    clarify.md             — analyst: only-new clarifying questions
    references.md          — researcher: curate the doc reference list (docs only)
    draft_prd.md           — author: write the PRD, self-assess completeness
    judge_prd.md           — judge: accept / refine / clarify / uncertain
  schemas/
    clarifications.json    — { questions: [{id, question, why}] }
    references.json         — { items: [{path, sections, rationale}] }
    prd_artifact.json       — { title, summary_markdown, file_path, confidence, needs_clarification, follow_up_questions }
    judge_verdict.json      — { verdict, intent, reason, confidence }
  views/base.pongo         — standalone base (rooms use flattened views)
  flows/                   — deterministic flow fixtures
```

## See also

- [`stories/bugfix/`](../bugfix/) — the checkpoint / refine / judge
  patterns this story mirrors.
- [`stories/oregon-trail/rooms/trail_guide.yaml`](../oregon-trail/rooms/trail_guide.yaml)
  — the conversational-room pattern the `idle` discovery chat is modeled on.
- [`docs/stories/choice-widget.md`](../../docs/stories/choice-widget.md)
  — the `param:` reply capture used for clarification answers.
- [`docs/tracing/trace-format.md`](../../docs/tracing/trace-format.md)
  — how the per-step model + file attribution lands in the trace.
