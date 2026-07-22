# Start ad hoc, grow into a story

There is no "too exploratory for a story" tier. There's a one-room story
whose room happens to be a big prompt — the [`workbench:`](../architecture/room-workbench.md)
primitive — and from turn one it already has a trace, an acceptance schema,
a write-mode gate, and a typed verdict that a bare prompt doesn't. This
walks one task through three stages: scaffold the workbench, run it and
read its trace, then promote the judgment the trace shows repeating into a
deterministic room. See
[`concept.md` §4](../architecture/concept.md#4-progressive-determinism) for
why this is the whole lifecycle, not a special case of it.

## Stage 1 — scaffold the workbench

A `workbench:` block needs an app, one `toolboxes:` entry, one `agents:`
entry using the WS `toolbox:`/`effect:` vocabulary, a prompt template, and
a JSON acceptance schema. Nothing here is workbench-specific syntax beyond
the `workbench:` key itself — see
[`room-workbench.md`](../architecture/room-workbench.md#the-declaration)
for the full field list.

`app.yaml`:

```yaml
app:
  id: quickfix
  version: 0.1.0

hosts:
  - host.agent.task

toolboxes:
  quickfix_toolbox:
    tools: [Read, Grep, Glob, Edit, Write, Bash]
    effect: write

root: bench

agents:
  quickfix_agent:
    system_prompt: >
      You are a general-purpose engineering assistant. Diagnose and fix
      whatever the operator describes, using the repo at {{ world.workdir }}.
    toolbox: quickfix_toolbox

states:
  bench:
    description: "One-room ad-hoc workbench."
    workbench:
      agent: quickfix_agent
      prompt: prompts/bench.md
      acceptance_schema: schemas/bench-note.json

    view:
      - prose: "Describe what's broken. I'll dig in read-only, then ask before changing anything."
```

`prompts/bench.md`:

```
The operator said: {{ args.request }}

Do the work. Return a one-line `summary` of what you did or found.
```

`schemas/bench-note.json` — the minimal permissive shape (mirrors
`stories/dev-story/schemas/landing-note.json`, the pattern this scales
into):

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "bench_note",
  "type": "object",
  "required": ["summary"],
  "properties": {
    "summary": { "type": "string", "minLength": 1 },
    "category": { "type": "string" }
  },
  "additionalProperties": true
}
```

That's the whole story. Load it through the full loader pipeline before
running anything — no session, no cost:

```sh
kitsoki validate app.yaml
```

```
✓ app.yaml — valid
```

What you get for those ~40 lines that a bare prompt doesn't: `write_mode:
read_only` on every mutating tool call until an operator grants it, a
synthesized free-text capture intent, a session trace, and a schema-checked
close-out note instead of loose prose. That's the entire fixed overhead —
every turn after the first is the same single `host.agent.task` dispatch a
bare prompt would make.

## Stage 2 — run it, then read the trace

```sh
kitsoki run app.yaml
```

Describe a few unrelated failures across a couple of sessions — the
workbench handles each one, read-only until you grant a write. Then read
what actually happened instead of trusting the transcript:

```sh
kitsoki trace --turns --app quickfix
```

`kitsoki trace` resolves the newest session under `~/.kitsoki/sessions/`
for the given app id with no path to remember (see
[`trace-format.md`](../tracing/trace-format.md) for the on-disk layout);
`--turns` prints, per turn, the routed input, the dispatched prompt, and
the bound close-out note. Look at the `bench_note` the agent returned each
time — this is where a repeated judgment shows up as the same field, hand
re-derived, turn after turn:

```
turn 1: bench_note.summary="test flaked on CI" bench_note.category="flaky_test"
turn 2: bench_note.summary="null pointer in the handler" bench_note.category="real_regression"
turn 3: bench_note.summary="timeout under load" bench_note.category="flaky_test"
```

The agent isn't just fixing things — it's silently classifying every
request into "retry this" versus "escalate this" before deciding what to
do next. That's the recurring decision point stage 3 promotes.

## Stage 3 — promote the judgment, not the whole room

The workbench keeps doing the diagnosis (still an LLM's job — see
[`concept.md` §3](../architecture/concept.md#3-narrow-llm-domains)). What
stops being free-form is what happens *after* the agent classifies: instead
of trusting the agent to act on its own judgment, a post-bind guarded
`emit_intent` reads the typed field the agent already emits and routes
deterministically — the same discipline
[`stories/dev-story/rooms/verifying.yaml`](../../stories/dev-story/rooms/verifying.yaml)
uses for its `verify_ok` verdict.

```yaml
intents:
  retest:
    title: "Retest"
    description: "Re-run the flaky-looking test."
  escalate:
    title: "Escalate"
    description: "Hand a real regression to a human."

states:
  bench:
    workbench:
      agent: quickfix_agent
      prompt: prompts/bench.md
      acceptance_schema: schemas/bench-note.json

    on_enter:
      # bench_note.category is "" until the synthesized on_enter
      # host.agent.task call binds it, so neither guard fires until the
      # agent's close-out note settles (the cherny decision-emit
      # discipline — see verifying.yaml above).
      - when: "world.bench_note.category == 'flaky_test'"
        emit_intent: retest
      - when: "world.bench_note.category == 'real_regression'"
        emit_intent: escalate

    on:
      retest:
        - target: retesting
      escalate:
        - target: escalating

  retesting:
    description: "Deterministic retry — no LLM judgment needed anymore."
    on_enter:
      - invoke: host.run
        id: rerun-test
        with:
          cmd: bash
          args: ["-c", "{{ world.bench_note.summary }}"]
        bind:
          retest_output: stdout
        on_error: bench
    view:
      - prose: "Retested: {{ world.retest_output ?? '(pending)' }}"

  escalating:
    description: "Deterministic escalation."
    view:
      - prose: "Escalated: {{ world.bench_note.summary }}"
```

`kitsoki validate app.yaml` passes on this shape unchanged. Read the trace
again after a few more runs: turns that used to end with an unstructured
"and then I decided to retry" now end with a `TransitionApplied` to
`retesting` or `escalating`, driven by a guard the loader validated at load
time — the same task, a more structured trace, with the LLM narrowed to
exactly the one judgment it's still earning its keep on.

## Where this goes next

The loop repeats: read the next recurring judgment out of the trace, add
one guard, one room. There's no point at which the story "graduates" out of
having a workbench — `stories/dev-story/rooms/landing.yaml` is the same
primitive at the far end of this process, one free-form floor among a dozen
deterministic pipelines it grew around itself.

- [`../architecture/room-workbench.md`](../architecture/room-workbench.md) —
  the full `workbench:` field reference and desugaring contract.
- [`../architecture/concept.md`](../architecture/concept.md) — the thesis
  this walkthrough is one worked instance of.
- [`ad-hoc-plan.md`](ad-hoc-plan.md) — the next rung up: a validated
  `plan` artifact with its own apply/verify loop, for when the workbench's
  free-form close-out note isn't enough structure.
- [`../tracing/trace-format.md`](../tracing/trace-format.md) /
  [`../tracing/testing.md`](../tracing/testing.md) — the trace format in
  depth, and how to lock a promoted room down with a no-LLM flow fixture.
