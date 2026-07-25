# Kitsoki story YAML — domain model

Authored `app.yaml` surface. Two views:

1. **Composition outline** — the nesting tree, mirroring what an author writes.
2. **Reference table** — cross-links (by-name refs) the tree can't show.

Grounded in `stories/bugfix/*.yaml` and `internal/app/types.go`.
Legend: `{...}` = map keyed by name · `[...]` = list · `→` = references by name.

This is the **map** of the story model — the one place the whole authored surface
is laid out and cross-referenced. For depth on individual subsystems it points to
the authoritative deep-dives rather than restating them:
[state machine](state-machine.md) · [imports & composition](imports.md) ·
[choice widget](choice-widget.md) · [authoring](authoring.md) ·
[semantic routing](../architecture/semantic-routing.md) ·
[Starlark host](../architecture/starlark.md) · [hosts](../architecture/hosts.md) ·
[story programming paradigm](../architecture/story-programming-paradigm.md) ·
[prior art](../architecture/prior-art.md). A companion class diagram sits
beside this file: [`domain-model.mmd`](domain-model.mmd).

---

## 1. Composition outline

```
Story (app.yaml)                         root of one story
├── app                                  identity block
│     id, version, title, author, license
│     context | context_path             Layer-2 system-prompt grounding
├── include: [path]                      merge rooms/*.yaml into states
├── root: <state name>                   → Room (start state)
├── hosts: [string]                      host.* allow-list
│
├── world { <name>: WorldVar }           typed world variables
│     └── WorldVar: type, default, values(enum)
│
├── intents { <name>: Intent }           global intent library
│     └── Intent: title, description, examples[], priority, hidden, synonyms[]
│           └── slots { <name>: Slot }
│                 └── Slot: type, required, default, values(enum), validator
│
├── states { <name>: Room }              top-level entry = a "room"
│     └── Room: type(atomic|compound|parallel), mode, description,
│     │        terminal, initial, menu[]→Intent, default_intent→Intent
│     ├── states { <name>: Room }         nested children (compound)
│     ├── view: [ViewBlock]               presentation
│     │     └── ViewBlock: banner | prose | heading | code | template |
│     │                    list | kv | media | choice
│     │           └── choice: mode, prompt
│     │                 └── items: [ChoiceItem]
│     │                       └── ChoiceItem: label, hint, intent→Intent,
│     │                                       slots, param(inline slot capture)
│     ├── on_enter: [Effect]              effects fired on entering the room
│     ├── on { <intent>: [Transition] }   intent-driven arcs
│     │     └── Transition: when(guard), target→Room|@exit:name|"." ,
│     │     │              default, emit[]→Intent
│     │     └── effects: [Effect]
│     └── intents { <name>: Intent }      state-local intents
│
│     Effect (used by on_enter + transition effects):
│           when(guard), set{}, increment{}, say,
│           invoke(host.*|iface.*), with{}, bind(world key),
│           emit_intent→Intent (+slots), on_error, background,
│           commit_operation | discard_operation
│
├── agents { <name>: AgentDecl }         reusable LLM personas
│     └── AgentDecl: system_prompt|system_prompt_path, model,
│                    provider→ProviderDecl, tools|toolbox→ToolboxDecl,
│                    bash_profile, effect(pure|read|write|external)
├── providers { <name>: ProviderDecl }   LLM backends: backend, model, effort
├── agent_plugins { <name>: AgentPluginDecl }  plugin, command|endpoint, model
├── toolboxes { <name>: ToolboxDecl }     tools, effect
│
├── host_interfaces { <name>: HostInterface }  named capability
│     └── HostInterface: ops[], default(binding)
│
├── imports { <alias>: ImportDef }        compose a child story under an alias
│     └── ImportDef: source(./path|@kitsoki/name|git+), entry(child start),
│     │             world_in(parent→child projection), intents, overrides
│     ├── exits { <name>: ImportExit }
│     │     └── ImportExit: to→Room (parent state), set(projection)
│     └── host_bindings { <iface>: HostBinding }
│           └── HostBinding: handler, script(scripts/*.star) → HostInterface
│
├── exits { <name>: ExitDef }            named exits this story offers a parent
│     └── ExitDef: requires(world guard)
├── exports                              intents surfaced to importing parent
│     └── intents: []→Intent
│
├── operations { <name>: OperationPolicy }  session run policy
│     └── mode|execution_mode, run_in_background, stop_on, terminal_artifact
└── routing                             semantic routing tier config
```

---

## 2. Reference table (leaf entities + cross-links)

| Entity | YAML location | Key fields | References out |
|---|---|---|---|
| Story | `app.yaml` root | include, root, hosts | root → Room |
| App | `app:` | id, version, title, context | — |
| WorldVar | `world.<name>` | type, default, values | — |
| Intent | `intents.<name>` | title, description, examples, priority, hidden, synonyms | — |
| Slot | `intents.<name>.slots.<name>` | type, required, default, values, validator | — |
| Room | `states.<name>` | type, mode, terminal, initial, menu, default_intent | menu/default_intent → Intent |
| View | `states.<name>.view` | list of ViewBlock | — |
| ViewBlock | view list item | banner/prose/heading/code/template/list/kv/media/choice | — |
| Choice | view `choice:` | mode, prompt | — |
| ChoiceItem | `choice.items[]` | label, hint, intent, slots, param | intent → Intent |
| Transition | `states.<name>.on.<intent>[]` | when, target, default, emit | target → Room; emit → Intent |
| Effect | `on_enter[]` / transition `effects[]` | when, set, increment, say, invoke, with, bind, emit_intent | invoke → HostInterface; emit_intent → Intent |
| AgentDecl | `agents.<name>` | system_prompt, model, tools, bash_profile, effect | provider → ProviderDecl; toolbox → ToolboxDecl |
| ProviderDecl | `providers.<name>` | backend, model, effort | — |
| AgentPluginDecl | `agent_plugins.<name>` | plugin, command, endpoint, model | — |
| ToolboxDecl | `toolboxes.<name>` | tools, effect | — |
| HostInterface | `host_interfaces.<name>` | ops, default | — |
| ImportDef | `imports.<alias>` | source, entry, world_in, intents, overrides | source → child Story |
| ImportExit | `imports.<alias>.exits.<name>` | to, set | to → Room (parent) |
| HostBinding | `imports.<alias>.host_bindings.<iface>` | handler, script | → HostInterface |
| ExitDef | `exits.<name>` | requires | — |
| Exports | `exports` | intents | intents → Intent |
| OperationPolicy | `operations.<name>` | mode, run_in_background, stop_on, terminal_artifact | — |
| Routing | `routing` | (semantic routing config) | — |

---

## 3. Item reference — detailed

Every construct, its exact fields, runtime behavior, and a real snippet. Anchors
are `file:line` in the repo (`internal/app/types.go` unless noted). This section
is the authoritative guide; the outline and table above are the map.

### 3.0 Load pipeline (how the tree comes to be)

An `app.yaml` is not used as-written. The loader runs
`parseAndMerge → resolveImports → expandPhases → validateDef` (`imports.go:8`):

1. **parseAndMerge** — parse `app.yaml`, then merge every `include:` file's
   `states:`/`intents:` into the root (rooms usually live one-per-file under
   `rooms/*.yaml`).
2. **resolveImports** — recursively fold each `imports.<alias>` child story into
   the parent (§3.13). This namespaces states/world/intents/ifaces.
3. **expandPhases / desugar passes** — `expandWorkbenches` desugars each
   `workbench:` into four primitives; phase templates expand; `world_in` becomes
   synthesized `on_enter` setters; `iface.*` invocations are rewritten to concrete
   `host.*` names (`resolveAllInterfaces`, `imports_interfaces.go:70`).
4. **validateDef** — schema + guard-compile + cross-reference checks (every
   `target:` resolves, every `invoke:` is in `hosts:`, enum/slot rules, etc.).

Guards and effect-value templates are **compiled once at load** and cached; per
turn only evaluation happens. Many "author errors" are therefore load-time hard
failures, not runtime surprises.

### 3.1 Story root (`app.yaml`)

The top of the tree. Beyond the nested blocks (documented below) it carries three
top-level scalars:

- **`root:`** — the name of the start state (a top-level state, i.e. a room). Where
  a session begins.
- **`include:`** — `[]path`; files whose `states:`/`intents:` are merged into the
  root before import resolution. Purely a source-splitting convenience.
- **`hosts:`** — the allow-list of `host.*` handler names this app may invoke
  (§3.12). A handler not listed here cannot be called.

### 3.2 `app:` — identity (`AppMeta`, types.go:809)

Documentation/identity block, no runtime graph behavior. `id`, `version`, `title`,
`author`, `license`, and `context` / `context_path` — the last two point at
Layer-2 grounding text folded into agent system prompts. `id`+`version` also gate
cassette matching (a cassette records `app_id`/`app_version`).

### 3.3 `world:` — typed state variables (`VarDef`, types.go:829)

The `world:` block is `map[string]VarDef` — one entry per variable. A `VarDef` is
`{type, default, values}`.

- **`type`** is a loosely-checked hint, not a closed enum. The values the runtime
  actually coerces against are `string`, `int`, `float`, `bool`, `enum`, `list`,
  `object`, `map`. Coercion (`coerceSetValue`, `machine.go:3268`) converts scalar
  RHS to the declared type on every `set:` — critical because `world_in` literals
  and template output arrive as strings (`"true"`→bool, `"3"`→int64).
- **`default`** is seeded into the initial world by `WorldFromSchema`
  (`machine.go:3893`) **only when non-nil**. A var with no `default:` reads back as
  `nil` (not a zero value) until first written. The engine also unconditionally
  seeds reserved keys (`session_cost_usd`, `turn_cost_usd`→`0.0`, `last_error`→`""`,
  `write_mode_scope`→`""`) so guards can read them from turn 1.
- **`values`** carries enum metadata. Enum enforcement happens on **slot** values,
  not on world writes — there is no runtime rejection of a `set:` that writes an
  off-enum value.

Two author forms (`VarDef.UnmarshalYAML`, types.go:851): the **schema form** (a map
with any of `type`/`default`/`values`), and the **inline-default form** — any other
YAML value is stored wholesale as `Default`, letting test apps seed rich nested
state directly (`landing_note: { plan: {...} }` ≡ `{ default: { plan: {...} } }`).

**Reading/writing at runtime:**
- read via `world.<key>` in guards, effects, and views; absent keys read `nil`.
- **`set:`** (map) — a single block is atomic and order-independent (all keys
  render against the same pre-block snapshot); *successive effects* see prior
  writes.
- **`increment:`** (map, integer deltas).
- **`bind:`** (`{world_key: result_key}`) — on a host `invoke:`, copies fields out
  of the host result envelope into world. Applied post-dispatch by the
  orchestrator.

**Namespacing:** imports prefix a child's world keys as `<alias>__<key>`, and
rewrite every `world.<key>` inside the child to match. Exception:
`ReservedWorldKeys` (`imports.go:91`) stay **bare at every depth** and are
engine-owned — a story may not `set:` them: `last_error`, `host_error`,
`write_mode_scope`, `session_id`, `operation_run`, `operation_drafts`, `error_log`,
`error_origin`. Any room may read them without declaring them.

**Operation overlay:** inside a room with an `operation:` block, `set`/`increment`/
`bind` write to a task-local `Patch`; `Vars` remains the readable committed+overlay
view; `commit_operation`/`persist_draft`/`discard_operation` close it (§3.9, §3.15).

```yaml
world:
  ticket_id:      { type: string, default: "" }
  cycle:          { type: int,    default: 0 }
  ticket_sources: { type: list,   default: [] }
  profession:     { type: enum, values: [banker, carpenter, farmer] }
```

### 3.4 `intents:` — typed actions (`Intent`, types.go:1461)

The `intents:` block is the global intent library (`map[string]Intent`); states may
also declare local `intents:`. An intent is a named action the user (or the engine,
via `emit_intent`) can invoke. Fields and exactly how each affects matching:

- **`title`** — display label (menus, disambiguation cards). Not itself a match key.
- **`description`** — LLM-facing gloss; shapes the **semantic/LLM** routing tiers
  and decider prompts, not the deterministic/synonym tiers.
- **`examples`** — do double duty: an exact normalized match routes deterministically
  (band 1.00, `deterministic.go:229`), and each example is also an implicit synonym
  in the semantic tier (band 0.90).
- **`priority`** (int) — ordering/tie-break only, never a match gate. Sorts allowed
  intents and menu rows descending.
- **`hidden`** (bool) — the intent stays callable but is hidden from the rendered
  menu and allowed-intent listings. For shortcut/utility intents.
- **`synonyms`** — alternate phrasings resolved by the semantic tier. Plain phrases
  (`"wade"`) match as bare-string (band 0.90); template phrases with `{slot}`
  captures (`"buy {items} for {total}"`) match as templates (bands 0.80/0.65) and
  capture slot values. A bare-string hit always outranks a template hit.

**`Slot`** (types.go:1479) — a typed parameter on an intent:
`{type, required, default, values, validator, format, synonyms, ...}`.
- `type` ∈ `string|enum|bool|int|float`.
- `required` — absent value → `MISSING_SLOTS` rejection; also forces the semantic
  router to fall through to the LLM rather than auto-dispatch with an empty slot.
- `default` — injected for an absent optional slot **before effects run**, so
  `slots.<name>` is always readable (no `??` guard needed).
- `values` — enum; a present off-enum value → `INVALID_SLOT_VALUE`.
- `validator` — an expr guard string checked against the value.
- `synonyms` — `{enumValue: [phrasings]}`, **enum slots only**; every key must be a
  declared enum value (loader-enforced).
- `format` — names a custom semantic format (e.g. `"jql"`) with its own validator.

**Slot values arrive from three sources**, all converging on
`IntentCall{Intent, Slots, Confidence}`: (1) NL extraction / template `{slot}`
capture, (2) `slots:` presets on a transition or choice item, (3) a choice item's
`param:` free-text capture.

```yaml
intents:
  work_ticket:
    description: "State which ticket to work when none is loaded yet."
    synonyms: ["work ticket {ticket_id}", "work ticket {ticket_id} titled {ticket_title}"]
    priority: 89
    slots:
      ticket_id:    { type: string, required: true }
      ticket_title: { type: string, required: false }
```

### 3.5 Intent routing — the tier cascade

How an utterance becomes an `IntentCall` (`orchestrator.go:1071`). First hit wins,
cheapest tier first:

1. **Deterministic** (`TryDeterministic`) — exact display/example string match,
   confidence 1.00. Always runs (zero cost).
2. **Semantic** (`TrySemantic`, only when `routing.enabled`, the default):
   a. **deterministic semroute** — bare-string synonym/example subset (0.90), then
      template `{slot}` match (0.80 all slots / 0.65 missing slot), tie 0.50.
   b. **embedding tier** (0.82) if an `embedding:` sidecar is configured.
   c. **contextual router** if the room has `contextual_routing.enabled`.
   d. **LLM extract tier** only if `routing.extract_llm_on_no_match: true`.
   Band dispatch: 0.50 → disambiguation card; ≥ high_bar (0.80) → direct submit
   (unless a required slot is unfilled → abdicate to LLM); ≥ mid_bar (0.65) →
   clarification card; below → near-miss (routed to a workbench capture if reachable,
   else fall through).
3. **turn cache** — replay a prior (state, signature) resolution.
4. **`default_intent` sink** — whole utterance → one required string slot, no LLM.
5. **`routing.free_form_fallback`** — app-level unmatched-prose sink (a canonical
   work-intake state+intent).
6. **Main-turn LLM interpreter** — the last, paid tier.

**`routing:` block** (`RoutingConfig`, types.go:424; defaults types.go:521) tunes
this: `enabled` (kill switch), `semantic_high_bar` (0.80), `semantic_mid_bar`
(0.65), the turn-cache knobs (`cache_enabled`, `cache_max_age` 30d, `cache_cap`
10000, `revalidate_strikes` 3), `stopwords_extra`, `extract_llm_on_no_match` +
`extract_llm_agent` (default `agent.local`), `free_form_fallback`, and `embedding`.

**`contextual_routing:` block** (per-room, `ContextualRoutingConfig`, types.go:1643)
opts one room into a final tier that classifies input into
`intent | help | room_request | meta_edit`, routing each class to `room_chat` /
`help_chat` / `meta_chat`. The `plan_*` fields add a **deterministic, no-LLM**
pending-plan guard: when a non-empty map sits at `pending_plan_path` (default
`landing_note.plan`), a bare affirmation routes to `plan_accept_intent` and
content-bearing input to `plan_refine_intent`, before the LLM classifier fires.

**The decider gate** (`DeciderSpec`, types.go:900; `decider.go:98`) is separate from
utterance routing: when the machine rests at a multi-way decision gate that owes an
autonomous choice (one-shot mode, or a room's `decider: llm` pin) with no external
driver, the judge agent (`host.agent.decide`) picks among the candidate intents and
fires one, looping up to depth 8. A verdict below `threshold` (default 0.8) bails to
a human.

### 3.6 Guard expression language (`when:`)

`when:` guards (and effect values, view templates) are compiled by `internal/expr`,
a whitelisted wrapper over `github.com/expr-lang/expr`. Guards compile via
`CompileBool` (bool-constrained at load); an empty `when:` is always-true; a false
guard skips the effect/arm silently; an **eval error aborts the surrounding chain**
(loud failure).

- **Scope (roots):** member chains must start at a whitelisted root —
  principally `world.*` and `slots.*` for guards; `event`, `run` (`run.id`,
  `run.turn`), `args`, `result` (host-result data in binds), and view-only roots
  `menu`/`prerequisites`/`item`. Access is gated by root, not leaf. `state` is
  **not** in scope for guards (view templates only).
- **Operators:** comparison, boolean (`&&`/`||`/`!`, plus `not X` sugar),
  arithmetic, ternary `?:`, membership `in`, nil-coalescing `??`, indexing.
- **Builtins (whitelist):** `len, trim, upper, lower, split, join, hasPrefix,
  hasSuffix, now, int, float, string, abs, round, type, get, keys, min, max`.
  Lambdas, `let`, map literals, and user functions are rejected at compile time.
- **nil-safety:** `len(X)` is auto-rewritten to `len(X ?? [])`, so
  `len(world.foo.questions) > 0` reads as "none yet" when `foo` is absent instead
  of faulting.

```yaml
when: "world.repro_checked && !world.regression_red_pre_fix"
```

### 3.7 `states:` — the room graph (`State`, types.go:945)

`states:` is `map[string]*State`. A **room is simply a top-level state** — the
first path segment (`StatePath.TopLevel()`, types.go:117); the TUI treats a change
in that segment as room navigation. There is no `room:` keyword.

**`type`** — statechart node kind:
- `atomic` (default) — a leaf.
- `compound` — has nested `states:` + `initial:`; on entry the machine descends to
  the initial leaf (`resolveInitial`, `machine.go:2783`; `initial:` supports pongo
  interpolation, so it can be world-dependent).
- `parallel` — nested `states:` are concurrent regions (each usually compound with
  its own `initial:`), coordinating via `emit:` events.

**`mode`** — `conversational` enables the free-form Agent Room harness. Consequences:
`transcript` defaults to `transient`; `agent_off_ramp` is rejected at load
(conversational rooms already converse); almost always paired with `default_intent`.

**Core fields:** `description` (location label), `terminal` (end state; rejects
off-ramp), `initial` (compound start child), `menu` (explicit intent allow-list,
else derived from `on:` arcs), `default_intent` (free-text sink — must name a
reachable intent with exactly one required string slot; routes unmatched input with
no LLM), and nested `states:`.

**State-level extras (all optional):**
- `prerequisites` (`[]Prerequisite`, types.go:1145) — deterministic readiness checks
  re-evaluated on every render; `{id, title, severity, when, satisfied_when,
  summary, help, action}`; `action` points at an existing intent (no transition).
- `timeout` (`TimeoutDef`, types.go:1713) — `{after, target, cancel_job}`;
  auto-transition after a duration, optionally aborting the latest background job.
- `assignment` (`AssignmentPolicy`, types.go:938) — `{role, required, allow_reassign,
  sync}`; names a role, never a person.
- `operation` (`OperationDecl`, types.go:1372) — `{scope}`; declares this room owns
  an operation-local world overlay (writes stay task-local until committed).
- `write_mode` — `""|open` (agent runs under its own tool policy) or `read_only`
  (room boots read-only; mutating tool calls gated on an operator grant; headless
  denies). Only valid on an agent-dispatch room.
- `agent_off_ramp` (`OffRampDef`, types.go:1526) — free text matching no intent is
  handed to an agent converse turn *without advancing state or mutating world*.
  Accepts `true` or `{agent, persona, banner, capture_free_text}`.
- `workbench` (`WorkbenchDecl`, types.go:1592) — a governed free-form work floor;
  `{agent, prompt, acceptance_schema, capture_slot, off_ramp_agent, context_args,
  plan}`. Desugared at load into `write_mode: read_only` + `agent_off_ramp` + an
  `on_enter host.agent.task` + a capture arc. The referenced agent must declare
  `toolbox:` + `effect: write|external`.
- `decider` — `""` follow run mode / `human` always stop at a gate / `llm` always
  auto-advance. Overrides the run's execution mix for this room.
- `contextual_routing` — §3.5.
- `footer` / `theme` / `transcript` — top-level rooms only: per-room status-line
  pongo template; a `blocks.Renderer` theme (`default`, `meta-blue`, `meta-amber`,
  `off-path`); and `persistent|transient` re-entry behavior.
- `relevant_world` / `relevant_slots` — pin keys/slots shown in the location
  indicator.
- `intents` — locally-scoped intent definitions.

### 3.8 `view:` — the presentation blocks (`view_element.go`)

`view:` normalizes (custom `UnmarshalYAML`, `view_element.go:252`) into a uniform
list of typed elements. Three author surfaces: a **scalar string** (legacy, one
pongo body), an **element array** (the main form), or **`{extends, blocks}`** /
**`{template_file}`**. Every element takes an **element-level `when:`** guard as a
*sibling* of the kind key (a nested `when:` fails loudly).

**Block vocabulary** (exactly one kind per element; enumerated at
`view_element.go:637`):

| Kind | Shape | Notes |
|---|---|---|
| `prose` | `prose: "<text>"` | markdown paragraph |
| `heading` | `heading: "<text>"` | section heading |
| `code` | `code: "<text>"` | code block |
| `template` | `template: "<pongo>"` | raw pongo body — the escape hatch |
| `list` | `list: {items: [...], marker: "-"}` | items are strings or `{label, hint, when}` |
| `kv` | `kv: {pairs: {K: "v"}}` | ordered; **every value must be a string** (wrap numbers in `"{{ }}"`) |
| `banner` | `banner: {text, subtitle, color}` | figlet ASCII from `text`; `color` is CSS hex |
| `media` | `media: {handle, caption, kind, path, annotate_intent, ...}` | display-only; `kind` ∈ video/image/pdf/html/slideshow; `annotate_intent` makes annotations drive a transition |
| `choice` | (below) | **at most one per view**; cannot live in `extends/blocks` |

**`choice:`** — the interactive widget (`choice.go`, validated by an embedded JSON
schema). Fields: `mode` (`single`/`multi`/`form`), `prompt`, `intent` (top-level
fire for multi/form), `slot` (selection-bound slot for multi), `min`/`max`, and
`items[]`:
- single-mode item: `{label*, intent*, hint, slots, param, when}`.
- multi-mode item: `{value*, label, hint, when}`.
- `param` (`choice.go:96`) — one-shot free-text slot capture on a single item:
  `{slot, type, placeholder, values, required}`.
- form mode adds a `template:` mad-lib body + ordered `fields:` (`ChoiceField`,
  `choice.go:108`).

**Rendering** is pongo2 (`render/pongo.go`, autoescape OFF). Stock filters
(`default`, `yesno`, `length`, `title`) plus Kitsoki customs: `reverse`, `col`/`rcol`
(pad/truncate to N runes), `reference` (line-numbered attributed source),
`wordwrap`, `truncatechars`. The `??` operator is translated to pongo `|default:`.

```yaml
view:
  - banner: { text: "REPRODUCING", subtitle: "Phase 1 / 7", color: "#06B6D4" }
  - kv:
      pairs:
        Ticket:  "{{ world.ticket_id }} — {{ world.ticket_title }}"
        "Bug verified": '{{ world.reproduction_artifact.bug_verified|yesno:"yes,no,(pending)" }}'
  - choice:
      mode: single
      prompt: "Actions"
      items:
        - { label: "continue", intent: accept, hint: "post and advance" }
        - label: "refine"
          intent: refine
          param: { slot: feedback, type: string, placeholder: "what to redo", required: false }
```

### 3.9 `on_enter:` / `on:` / `Transition` / `Effect`

**`on_enter:`** (`[]Effect`, types.go:996) — effects run on entering the state, in
author order.

**`on:`** (`map[string][]Transition`, types.go:994) — maps an intent name to an
**ordered list of arcs**. `Transition` (types.go:1178):
`{target, when, default, effects, guard_hint, view, emit, push_history, operation}`.

**Arc ordering** (`evaluateArmsTraced`, `machine.go:2284`) — arms are evaluated
top-to-bottom; the **first match wins**. A `default: true` or unguarded arm fires
immediately (so `default` must be **last**, else it shadows later arms). If none
match, the first non-empty `guard_hint` is shown to the operator; a guard eval error
aborts loudly.

**Target semantics** (`resolveTarget`, `machine.go:2755`):
- `""` / `"."` → self (the idiomatic re-render arc, e.g. `look`).
- relative refs walk the path; absolute refs normalize slashes to dots.
- **`@exit:<name>`** → an import exit, handled by the imports pass, not
  `resolveTarget`: rewritten to the parent state in `imports.<alias>.exits.<name>.to`.
  At the story root, an unmapped `@exit:X` becomes a synthesized `__exit__X`
  terminal.

**`Effect`** (types.go:1215) — one atomic mutation/side-effect, applied sequentially
(`applyEffectsTracedWithOptions`, `machine.go:2369`):
- **`when`** — per-effect guard; sees the post-prior-effect world (an earlier `set:`
  steers a later branch). For `set/say/invoke` an eval error is fatal; for
  `emit_intent` the effect is **deferred silently** and re-evaluated post-bind (emit
  guards routinely reference keys only populated by later host binds).
- **`set`** (map) — atomic within one block, progressive across effects;
  type-coerced to the world schema.
- **`increment`** (map, int deltas).
- **`say`** — appends a narrative message (a `MachineSay` event).
- **`invoke`** — calls a host fn (`host.agent.task`, `host.run`, `iface.transport.post`,
  `host.starlark.run`, …). The machine **collects** it as a `HostInvocation`; actual
  dispatch is at orchestrator time. `with:` args are re-rendered against the
  post-bind world before each call, so a later invoke sees an earlier one's binds.
- **`with`** (map) — templated invoke args.
- **`bind`** (`{world_key: result_key}`) — extracts dotted paths out of the host
  result envelope (`submitted.summary`, `stdout_json.red`, `path`, `sha`) into world;
  applied at dispatch.
- **`id`** — stable call-site label (threaded as reserved `call`) so flow stubs
  (`by_call:`) and cassettes (`match: {call: <id>}`) can target one of two calls
  sharing a handler.
- **`on_error`** — a transition target fired when the invoke errors, after setting
  reserved `last_error`/`host_error`. Mutually exclusive with `ack_error`.
- **`ack_error`** — "record it, keep going": a machine-readable reason string that
  logs the failure as handled, suppresses the failure banner, and continues the chain.
- **`background`** — dispatch the invoke as a job; binds `job_id` instead of running
  synchronously. `on_complete:` (`[]Effect`) fires when the job terminates and may
  carry a `target:` to trigger a synthetic transition.
- **`once`** — reload-safe idempotency: skip the invoke when **every** `bind` target
  is already set (nil/`""`/`{}`/`[]` = unset); clearing a bind target re-arms it.
- **`emit_intent`** (+ `slots`) — dispatch a **synthetic intent** against the current
  state after the chain (self-loop / auto-advance, e.g. LLM judge → `accept`).
  Templated (`emit_intent: "{{ world.llm_verdict.intent }}"`), bounded by depth 8,
  must resolve to a declared arc; empty-after-render is a no-op.
- **`emit`** — send a named event to parallel regions.
- **`agent`** (`AgentPlugin`) — the `agent_plugins:` alias handling the call (default
  `agent.claude`); **`selection`** pins harness/profile with evidence.
- **Operation effects:** `commit_operation` (`{world, clear}` — publish overlay,
  close), `persist_draft` (`{id, title, world}` — save overlay under a draft handle),
  `discard_operation` (`{reason}`).

The canonical `on_enter` shape (bugfix `reproducing`): guarded deterministic
`host.run` gate binding `stdout_json.*` with `on_error`, then a guarded `emit_intent`,
then a `once: true` `host.agent.task` binding the artifact, then a conditional
`host.agent.decide` binding a verdict, ending with `emit_intent:
"{{ world.llm_verdict.intent }}"` — "produce → gate → judge → auto-route".

### 3.10 `agents:` — LLM personas (`AgentDecl`, types.go:1726)

A named persona: a system prompt + tool surface + model + blast-radius class,
invoked from a `host.agent.*` effect via `with: {agent: <name>}`.

**Key fields:** exactly one of `system_prompt` / `system_prompt_path` (the persona
body, which **replaces** Claude Code's default prompt unless `inherit_claude_default:
true`); `model`; `tools` **or** `toolbox` (mutually exclusive) with
`tools_add`/`tools_remove`; `provider` (a `providers:` entry); `harness`; `effort`
(`low..max`); `mcp` (`{servers, tools}`); `permissions` (`{mode, disallowed_tools}`);
`bash_profile` (required when Bash is present and the agent is used with `ask`/
`decide`; ignored by `task`/`converse`); `token_budget` (`{warn_tokens,
refuse_tokens}`, both required, refuse ≥ warn); and **`effect`**.

**The effect capability ladder** (`internal/effect/effect.go:9`) — four ordered
tiers, most-privileged tool wins:
- **pure** — touches nothing (tool-less LLM call).
- **read** — reads state, no change (`git log`, `Read`, `Grep`).
- **write** — mutates replayable local state (`Write`/`Edit`, `git commit`); **Bash
  classifies as write**, not external.
- **external** — irreversible external action (`host.transport.post`, a PR, an email,
  `WebFetch`).
When `effect:` is omitted the loader resolves it as the join over the tool surface
(`FromTools`) and writes it back. When declared, the loader checks it: a declared
`read`/`pure` whose surface actually contains a mutator is a **load-time hard error**
(unknown tools/MCP servers fail closed to external).

**The `host.agent.*` verbs** (the invocation surface; a persona's declared surface
must fit the verb):
- **`ask`** (read) — read-only inspection; returns prose, or typed JSON when
  `schema:` is supplied. One call, no acceptance loop.
- **`decide`** (read) — typed LLM verdict; `schema:` mandatory (auto-attached submit
  tool); returns `{submitted, rationale}`.
- **`extract`** (read) — tiered resolver: `synonyms` → `slot_template` → `llm`
  fallback; returns `{submitted, resolved_by}`.
- **`task`** (write) — the agentic verb: Edit/Write/Bash freely in the working dir,
  driving an `acceptance:` loop (`{schema, post_cmd, max_retries}`) until `submit()`
  passes; returns `{submitted, files_changed, final_diff, replay_mode}`.
- **`converse`** (write) — free-form conversational session with persistent
  transcript; `permission_mode` ∈ `bypassPermissions|ask|denyAll`.
- **`codeact`** (write) — bounded code-act loop: the agent emits a Starlark snippet
  per step, observes the result, terminates with `done(payload)` conforming to
  `schema:`.

The `ask`/`decide`/`extract` verbs are read-only **by construction** — the loader
rejects a mutator in their tool surface.

### 3.11 `providers:` / `agent_plugins:` / `toolboxes:`

- **`providers:`** (`ProviderDecl`, types.go:208) — named backend profiles referenced
  by `agent.provider` or `with:{provider}`. `{backend (claude|codex|copilot), model,
  effort, env}`; `env` merges `${VAR}`-interpolated vars onto the subprocess (e.g.
  `ANTHROPIC_BASE_URL` to point some calls at an alternate backend).
- **`agent_plugins:`** (`AgentPluginDecl`, types.go:155) — transport configs keyed by
  agent alias (`agent.claude`, `agent.local_llm`). A default `agent.claude =
  builtin.claude_cli` is injected when absent. `{plugin, command/args, endpoint/tool/
  headers, env, model, grammar/json_schema, port/server_bin/api_key_env}`. Named
  `agent_plugins:` because `hosts:` was taken.
- **`toolboxes:`** (`ToolboxDecl`, types.go:1834) — reusable tool surfaces an agent
  references via `toolbox:` and specializes with `tools_add`/`tools_remove`.
  `{tools, effect}`; when `effect:` is set it asserts the joined surface class,
  checked at load.

### 3.12 `hosts:` / `host_interfaces:` (ifaces)

- **`hosts:`** (`AppDef.Hosts`) — the flat allow-list; every `host.*` handler must be
  listed to be invokable. Iface-resolved hosts are unioned in implicitly at fold
  time, so authors only list the hosts their rooms call directly. Registry dispatch
  is exact-match then longest registered prefix (so `host.gh.ticket.comment` can
  resolve to a `host.gh.ticket` carrier that dispatches on an `op` arg). Common
  namespaces: `host.agent.*`, `host.run`, `host.starlark.run`, `host.git`,
  `host.gh.ticket.*`, `host.inbox.add`, `host.diff.open`, `host.ide.*`,
  `host.transport.post`, `host.capsule_workspace`, `host.artifacts_dir`, `host.chat.*`.
- **`host_interfaces:`** (`HostInterfaceDef`, types.go:748) — an **iface** is a
  provider-neutral capability surface with a fixed I/O contract and a default
  binding. Rooms call `iface.<name>.<op>`; the fold rewrites every such call to the
  concrete `<binding>.<op>` (`resolveAllInterfaces`, `imports_interfaces.go:70`),
  where `binding = iface.default` unless an importer overrides it via `host_bindings`.
  `HostInterfaceDef` = `{description, operations, default}`; each
  `HostInterfaceOp` (types.go:756) = `{input, output, effect?, deterministic?}` — the
  `effect`/`deterministic` knobs override the builtin verb classification for the
  bound handler. The default binding is what makes standalone runs work without an
  instance wrapper.

```yaml
host_interfaces:
  ticket:
    description: "Issue tracker abstraction (file / GitHub Issues / Jira)."
    operations:
      get:     { input: {id: string}, output: {id: string, title: string, body: string} }
      comment: { input: {id: string, body: string}, output: {ok: bool} }
    default: host.local_files.ticket
```

### 3.13 `imports:` — composition (`ImportDef`, types.go:615)

`imports:` folds a child story under an alias. Fields: `source` (`./path` |
`/abs` | `@kitsoki/<name>` → `<repo>/stories/<name>` | `git+…`), `entry` (child's
start state, child namespace), `world_in` (`{childKey: parentExpr}` evaluated at
entry), `hosts` (`inherit`|`declared`), `exits` (`{name: ImportExit}`), `intents`
(`{export, import}`), `overrides` (`{states, intents, prompts}`), and
`host_bindings` (`{iface: handler|script}`).

**Fold semantics** (`foldChild`, `imports.go:596`) — everything is namespaced:
1. **States** — the child installs as a single **compound** at `states[alias]` with
   `initial = entry`; child states become its children under their child-local names.
   Bare transition targets inside the child are rewritten to `<alias>/<state>` (the
   visible path uses a slash).
2. **World keys** — prefixed `<alias>__<key>`; every `world.<key>` in the child
   rewritten. Reserved keys stay bare.
3. **`world_in`** — becomes synthesized `on_enter` setters on the wrapper (parent-scope
   RHS → child's prefixed key). The fold also re-seeds child world keys to their
   schema defaults on every enter, so a re-entered import doesn't run against stale
   terminal flags.
4. **Intents** — lifted into the parent's flat table as `<alias>__<name>`
   (colliding bare names are rejected; `intents.import` can lift non-colliding ones
   to bare).
5. **Exits** — a child `@exit:<name>` is rewritten to the parent state in
   `ImportExit.to`, with `ImportExit.set` (child-scope exprs → parent keys) attached.
   The loader validates each mapped exit exists and that the `@exit` transition sets
   every key the child declared in `exits.<name>.requires`.
6. **host_interfaces** — child ifaces renamed `<alias>__<iface>`; `host_bindings`
   override the default; unprefixed terminal-name matching rebinds every lifted
   `*__ticket` iface at once.
7. **Agents / meta-modes** — renamed `<alias>__<agent>`; all references rewritten.

**`host_bindings`** (`HostBindingSpec`, types.go:666) — `{iface: handler}` binds an
iface straight to a `host.*`; `{iface: scripts/x.star}` synthesizes a handler that
delegates to `host.starlark.run` with the op injected into `ctx.inputs.op`.

```yaml
imports:
  tail:
    source: ../delivery-tail
    entry: integrate
    world_in:
      workspace_branch: "{{ world.feature_branch }}"
    exits:
      shipped:
        to: "@exit:shipped"                       # re-export up this story's own exit
        set: { shipped_sha: "{{ world.tail__shipped_sha }}" }   # reads the folded child key
```

### 3.14 `exits:` / `exports:` (this story's own surface)

- **`exits:`** (`ExitDef`, types.go:735) — `{name: {description, requires}}`. Named
  return points a parent maps back to its own states; `requires` lists child world
  keys that must be set when the exit fires (static + runtime checked). A standalone
  load synthesizes `__exit__<name>` terminals.
- **`exports:`** (`ExportsBlock`, types.go:743) — `intents:` this app surfaces to
  importers; only these may be `intents.import`-ed into a parent.

### 3.15 `operations:` (`OperationPolicy`, types.go:918)

An operation is an abandonable, task-local world overlay plus a session run policy.
A room with an `operation:` block owns the overlay (writes stay task-local until
`commit_operation`/`persist_draft`); an `operations:` entry names a runnable policy
over it: `{title, mode (interactive|autonomous|supervised), execution_mode
(one-shot|staged), run_in_background, stop_on/pause_on (signals like needs-human,
gate-failed), terminal_artifact (world key of the final artifact), phase_summary}`.
A transition's `operation:` field starts a run using one of these policies.

### 3.16 `scripts/*.star` — story-authored Starlark functions

A story can **bring its own functions**: `.star` files under the story dir, written
in [Starlark](https://github.com/bazelbuild/starlark) (Bazel's deterministic,
embeddable Python dialect, via `go.starlark.net`). They run in-process through
`host.starlark.run` — either invoked directly from an effect, or bound to an iface
op via `host_bindings` (§3.13). They exist to fill the gap between the YAML effect
vocabulary (too weak for fiddly data transforms) and a bespoke Go handler (too
heavy) or raw shell (opaque, unsandboxed): a real expression language that is
nonetheless deterministic, introspectable, and replayable
(`internal/host/starlark/doc.go:5-12`).

**Call contract** (`doc.go:20-24`, `run.go:208-212`) — a script defines
`def main(ctx)` and **returns a dict**. The single `ctx` struct carries:
- `ctx.inputs.<name>` — the typed inputs (from the effect's `with.inputs`, plus the
  injected `op` when bound to an iface).
- `ctx.world.get("key")` — **read-only** world snapshot (`None` when absent).
- capability attributes (below), only when granted.

Outputs flow **only** through `main`'s return dict — there is deliberately no
`ctx.world.set` (`ctx.go:54-57`). A `<name>.star.yaml` sidecar beside each script is
**authoritative** over its interface: `inputs:` are validated before eval, `outputs:`
after (`schema.go`), so a bad shape fires the effect's `on_error:` arc and sets
`world.last_error` rather than crashing.

**Execution model** (`run.go:124-234`) — a sandboxed thread with a 10M-instruction
ceiling, strict `FileOptions` (no recursion, no global reassignment, no `set`
builtin), and only `json`/`math`/`yaml` predeclared. Explicitly **no clock, no
randomness, no environment, no shell** (`doc.go:26-32`); map keys are sorted on
conversion so iteration order is stable. A run is hermetic by construction.

**Capability surface — deny-by-default, opt-in** via the effect's
`with.capabilities:` (`capabilities.go:82-93`). The default grant is pure
(`json`/`math`/`yaml` + `ctx.inputs` + read-only `ctx.world`); every external
surface must be named:

| `ctx` attr | grant | surface |
|---|---|---|
| `ctx.http.get/post` | `http` | network, method/host allow-listed |
| `ctx.fs.read/write/glob` | `fs.read`/`fs.write` | repo-rooted, size-capped, no delete/chmod/rename |
| `ctx.probe(name, args)` | `probe`/`vcs`/`github` | a **fixed argv allow-list**, not a shell (`git.status` → `git status --porcelain`) |
| `ctx.host.call(name, args)` | `host.verbs` | the only door onto the engine host registry — a fixed ~30-verb read/introspection vocabulary, subset-granted; an ungranted name is rejected before dispatch |

Every network call goes through an injectable `HTTPClient` and every fs/probe
through an injectable `Inspector`, so production records them and flow tests replay
them from a cassette — **byte-for-byte reproducible**, with a body-free
`{method,url,status}` trace summary per call.

**Wiring** — two forms:
```yaml
# A. bound to an iface op (the dispatched op arrives as ctx.inputs.op)
imports:
  core: { host_bindings: { greeter: scripts/greet.star } }

# B. invoked directly from an effect
- invoke: host.starlark.run
  with:
    script: scripts/weather_report.star
    capabilities: { http: { methods: [GET] } }
    inputs: { location: "{{ world.location }}" }
  bind: { headline: headline, coords: coords }
  once: true
  on_error: failed
```

**Relationship to `codeact`** — `host.agent.codeact` (§3.10) is the *same* sandbox
driven turn-by-turn by an LLM: the agent emits a Starlark snippet each step and
terminates with `done(payload)`. It runs through the identical evaluator with one
shared `CapabilitySpec`, bounded by a step budget and an optional output schema. So
an agent may *write* Starlark, but it can only ever reach the exact capability
surface the story granted — author-supplied and LLM-supplied code get the same
deterministic, capability-gated treatment.

---

## Notes

- **Rooms are just top-level states.** A story's state graph is `states{}`;
  the top-level entries are the visible rooms, nested `states{}` are substates
  of a compound room.
- **`Effect` is one shape reused in two places** — `on_enter[]` (on room
  entry) and each `Transition.effects[]` (on taking an arc).
- **Two cross-cutting reference webs**: intent routing
  (`Transition.target` / `ChoiceItem.intent` / `Effect.emit_intent` / `menu`)
  and host capability (`Effect.invoke` → `HostInterface`, rebound by importers
  via `host_bindings`).
- **Composition via `imports`** folds a child story under an alias: child
  states are namespaced, child world keys prefixed, and child `exits` map back
  to parent rooms through `ImportExit.to`.

---

## 4. One story, a spectrum of runtimes

The single most under-appreciated property of the model: **the same authored
story runs anywhere on a continuum** — from a fully deterministic, zero-LLM
workflow orchestrator to a fluent, free-text, multi-room agent wizard — with no
change to the artifact. That is possible because two things are chosen
*independently, per state and per transition*:

- **The driver** — how an intent is selected each turn: deterministic tiers
  (exact display / example / synonym match, menu selection, turn-cache) →
  semantic / embedding → LLM router → synthetic `emit_intent` → autonomous
  decider (§3.5).
- **The actor** — what a transition actually does: a deterministic host call
  (`host.run`, `iface.*`, Starlark §3.16) → an LLM agent (`host.agent.*`, §3.10).

Neither is baked in, and the routing cascade is layered so the **deterministic
tiers sit above the LLM tiers**. A story therefore *degrades to zero-LLM by
default* when phrasings are known and *escalates to a model only on genuine
ambiguity* — same graph, same world, same views. The two ends of the dial:

**1. Deterministic workflow orchestrator (zero LLM).** Route every turn
deterministically (examples / synonyms / menu picks / `emit_intent`) and make
every action a deterministic host call. The pure machine (§3.9) computes turns
with no I/O; calls are dispatched to deterministic handlers or Starlark. This is a
Temporal / Serverless-Workflow-class orchestrator — typed state, guards, parallel
regions, `on_error` retries, import subflow composition — that is **byte-for-byte
replayable** (flows / cassettes) and *never calls a model*. Intent routing here is
a deterministic classifier over a fixed grammar, not a prompt.

**2. Fluent multi-room agent wizard.** Route turns from free text: the semantic /
LLM tiers resolve an utterance to a typed intent + slots; rooms dispatch LLM
agents (`ask` / `decide` / `task` / `converse` / `codeact`); the decider gate and
autonomy modes (`operations`: interactive → supervised → autonomous, staged →
one-shot) progress the run **agent to agent** — e.g. reproduce → propose →
implement → test → review — auto-advancing on confident verdicts and bailing to a
human below threshold. The user talks; the story walks the graph.

**3. Everything in between, freely mixed — authored, not forked.** The valuable
cases live in the middle, and they are one artifact:
- a mostly-deterministic pipeline with a single conversational triage room;
- a fluent wizard whose *commit* arcs are deterministic `git` / CI host calls, so
  the **irreversible** steps never depend on an LLM;
- free-text routing that fires a **deterministic action** — the router resolves
  "open the PR" to the `open_pr` intent, whose effect is a plain `host.git` call:
  *fluent input, mechanical execution*;
- the **same** story run head-lessly against cassettes in CI (zero LLM,
  deterministic) *and* live in front of an operator (fluent) — the test surface
  and the product are literally the same file.

**Why this is hard to buy off the shelf.** The two ends are normally *different
products*. A workflow engine (Temporal, Serverless Workflow, Airflow) is
deterministic but has no free-text / agent / presentation model — you cannot make
it fluent without leaving the tool. An agent framework (LangGraph, Rasa, AutoGen)
is fluent but is not a byte-for-byte-replayable zero-LLM orchestrator — its
determinism story is checkpointing, not "runs identically with the model
unplugged." Kitsoki puts both ends on one dial because the determinism boundary
(§3.9) and the layered router (§3.5) are *structural*: the LLM is an **optional
tier and an optional actor**, never a load-bearing assumption. You author the
story once and then choose — per room, per transition, per run mode — how much
intelligence to spend. Sections 5 and 5.6 make the per-competitor version of this
argument; this is the property underneath it.

---

## 5. Why not Serverless Workflow 1.0?

**The question.** "This is a state machine with tasks, data, events, and
subflows — why invent a domain model instead of authoring in the CNCF
[Serverless Workflow](https://serverlessworkflow.io/) DSL (v1.0.0)?" It's a fair
question and deserves a precise answer, not a dismissal. The honest version:
Serverless Workflow is an excellent spec **for the domain it targets**, and that
domain is not the one a Kitsoki story occupies. The overlap is the *substrate*
(typed data, branching, concurrency, subflows, events, errors); the parts of a
story that are actually the product (intent routing, presentation, governed LLM
agents, statechart composition) have no home in it.

### 5.1 What Serverless Workflow 1.0 actually is

A vendor-neutral DSL for **orchestrating service and function calls in reaction
to events**. A workflow document is `{document, use, input, do, output,
schedule, timeout}`; the heart is `do:` — an ordered list of named **tasks** that
transform a single flowing JSON data document. The task vocabulary is well
chosen and genuinely overlaps a story's control flow:

| SW task | Purpose |
|---|---|
| `call` | invoke a function — HTTP / OpenAPI / gRPC / AsyncAPI / custom |
| `run` | run a container / script / shell / **subworkflow** |
| `do` | a nested task sequence (composite) |
| `switch` | conditional branch — `case: { when, then }` |
| `fork` | concurrent `branches`, optional `compete` |
| `for` | iterate `each` in a collection |
| `try` / `raise` | typed error handling (RFC 7807 problems) |
| `emit` / `listen` | produce / await **CloudEvents** |
| `set` / `wait` | mutate data / pause |

Data flows via runtime expressions (jq by default, `${ }`), `input.from`,
`output.as`, and `export.as` into `$context`. Flow directives (`then:`) are
`continue | exit | end | <taskName>`. It is mature, CNCF-governed, and the
right tool for "when order X lands, call inventory, then fan out to three
fulfilment services, retry with backoff, emit a shipped event."

We reuse its **ideas** — Kitsoki's `set` / guard-`switch` / `fork`(parallel
regions) / `try`(`on_error`) / `emit` all have direct spiritual analogs, and our
expr dialect is jq-adjacent. The disagreement is not with the primitives; it's
with what sits *above* them.

### 5.2 Construct mapping — and what's lost in translation

| Kitsoki story construct | Nearest SW construct | What SW cannot express |
|---|---|---|
| `world` (typed, schema'd, coerced) | the jq data document | ~ parity (minor: schema/coercion, reserved keys) |
| `Effect.set` / `increment` | `set` task | ~ parity |
| `Transition` guards / arc ordering | `switch` cases + `then` | ~ parity |
| parallel `states` + `emit` | `fork` branches / `listen` | no hierarchical **entry actions**, no history |
| `Effect.invoke` (host) | `call` / `run` | no capability class, no collect-then-dispatch boundary |
| `on_error` / `ack_error` | `try` / `catch` | ~ parity |
| **`intents` + `slots`** (examples, synonyms, priority) | — | **no intent/slot/NL-routing model at all** |
| **`routing` / `contextual_routing` / decider** | `listen` (typed events) | **no NL classification, confidence bands, disambiguation** |
| **`view` / `choice` / themes / footers** | — | **no presentation layer; SW is headless** |
| **`agents` + effect ladder + verbs** | `call` a function | **no LLM persona, blast-radius class, acceptance loop, write-mode gate** |
| **`states` as navigable rooms** | — | **no location model; `do` is a pipeline, not a place you are** |
| **`imports` fold + `host_interfaces` rebind** | `run: workflow` + `use` | **no namespaced statechart fold, no capability rebinding, no exit→state mapping** |
| **`operations` (autonomy modes, overlays)** | — | **no autonomy policy, no abandonable task-local overlay** |
| pure machine → flows / cassettes | — | **no pure-core/effect-boundary or record-replay contract** |

Every blank cell in the right column is load-bearing. They are not edge features;
they are the reason a story exists.

### 5.3 The structural mismatches

**1. The driver is natural-language intent routing, not a task pipeline.** A SW
workflow *runs*: it advances through `do:` and reacts to typed CloudEvents. A
story *converses*: each turn, an utterance (from a human, an LLM router, or a
synthetic `emit_intent`) is classified against **the current room's allowed
intents and slot schema** through a six-tier cascade (deterministic → synonym →
embedding → contextual → LLM → fallback) with confidence bands, slot-filling,
and disambiguation cards. SW's `listen` matches events by `type`/`source`/
attributes — it has no notion of "route this free-text sentence to one of these
typed actions, filling these typed slots, and if you're only 0.65 sure, render a
clarification." That routing model *is* §3.4–3.5, and it has no SW surface.

**2. It's a hierarchical statechart, not a flat task graph.** Rooms are Harel
statechart nodes: compound states with `initial` descent and **`on_enter` entry
actions**, parallel regions, a history stack (`push_history`), and self-loops
(`look`). "Where you are" is a first-class, navigable location. SW `fork` gives
concurrency and `then` gives jumps, but there is no hierarchical state with entry
behavior, no history, and no location semantics — the statechart in Kitsoki is
the *product* (a set of rooms you move through), not just the shape of the
control flow. We *did* reuse prior art here: the lineage is SCXML/statecharts,
not "we invented state machines."

**3. A story is a UI — and a specific kind of UI.** This is the part most easily
undersold. A room `view` is not decoration bolted onto a workflow; it is a
**typed, composable, per-state presentation model that is two-way bound to the
intent/slot grammar**. `view` is an ordered list of typed blocks (banner, kv,
list, code, media, pongo `template`) plus at most one interactive `choice`
widget in single / multi / **form** mode. A rendered choice item *is* an intent
dispatch: selecting it fires `intent` with preset `slots`, and a `param`/`form`
field captures typed slot values inline — the button and the command are the same
object. Three properties make this distinct from adjacent approaches:

- **Deterministic, not generative.** The view renders the *same* output from the
  same world (pongo2 over `world`/`slots`, §3.8). It is not an LLM emitting
  HTML/React per turn — so it is auditable, diffable, cassette-replayable, and
  free of per-render model cost or hallucinated markup.
- **Surface-independent.** The same `view` model renders to the TUI and the web
  frontend; `theme`, `footer`, and `transcript` are room-level chrome. The
  authored artifact is the presentation, not a channel-specific template.
- **Bound to state and routing.** Blocks and items carry `when:` guards over
  world; the widget's options are exactly the room's reachable intents. The UI
  and the state machine cannot drift apart because they are authored as one thing.

The closest mental model is a conversational-UI **card system** (Adaptive Cards)
fused into a statechart — not generative UI and not a fixed chat transcript.
Serverless Workflow, by design, has no presentation concept at all: it
orchestrates backends. Modeling a story in SW strands this entire layer — the
thing a user actually sees and touches — outside the spec.

**Boundary, stated honestly:** the shipped `view:` model is a typed,
state-bound presentation, not yet a complete application shell. It does not
declare application-wide navigation, reusable pages/cards, custom presentation
components, native VS Code contributions, or one inbound handler exposed
consistently through CLI/MCP/JSON-RPC. The proposed
[`story-application-platform`](../proposals/story-application-platform.md)
preserves the room view as the canonical content/action unit and adds a
presentation-free application frame plus optional surface presentations around
it. This is an extension of the view/intent binding above, not a second UI
controller.

**4. LLM agents are governed, capability-classified actors — not function
calls.** A SW `call` is a call; the spec has no reason to care whether the callee
is deterministic. A Kitsoki `agent` is a persona with a system prompt, a tool
surface, a **pure/read/write/external** blast-radius class enforced at load, a
verb taxonomy where `ask`/`decide`/`extract` are read-only *by construction*, an
acceptance loop that retries until a schema validates, token budgets, permission
modes, and per-room `write_mode` gating that can require a live operator grant
before any mutation. Governing **nondeterministic, mutating, expensive** actors
is the central design problem of the story engine. SW has no vocabulary for it,
because for service orchestration it isn't the problem.

**5. Composition is a statechart fold with capability rebinding, not
call-a-subworkflow.** SW composes by `run: workflow` (invoke a subworkflow with
input) or `use` (reusable functions/retries/auth). Kitsoki `imports` folds a
child story into the parent's **own statechart**: child states are namespaced and
become substates, child world keys are prefixed, child intents are lifted, child
`@exit:` points are rewritten to *parent* rooms with data projection, and — the
part with no SW analog — the child's provider-neutral `host_interfaces` are
**rebound** by the importer at one place (`host_bindings`). That's how the same
`bugfix` story runs standalone against local files and, imported, against GitHub
Issues without editing the child. SW's call-with-input model can't express
"graft your rooms into mine and swap out your capability bindings."

**6. The engine is a pure function; effects are collected, not executed.** The
machine computes `(state, world, intent) → TurnResult` with **zero I/O** — host
calls are *collected* as `HostInvocation`s and dispatched by the orchestrator
afterward. That boundary is what makes flow fixtures and cassettes work: a story
is exhaustively testable with **no LLM and no live services**, deterministically,
by construction (a hard requirement in this repo — see AGENTS.md). Serverless
Workflow runtimes execute `call`/`run` inline; the spec defines neither a
pure-core/effect split nor a record-replay contract. Authoring in SW would mean
giving up the property the whole test strategy rests on.

**7. Human-in-the-loop autonomy is first-class.** `operations` encode run
policies — interactive / supervised / autonomous, one-shot / staged,
`run_in_background`, `stop_on`/`pause_on` signals — over an abandonable,
task-local world overlay (drafts that commit or discard). Plus prerequisites,
role `assignment`, off-ramps, and the decider gate that bails to a human below a
confidence threshold. SW 1.0 has no first-class human task (you'd simulate one
with `listen`) and no autonomy-mode policy. The operator-governance surface is
most of what makes a story safe to run unattended, and it's absent upstream.

### 5.4 The collapse test

Could you *encode* a story in Serverless Workflow? Only by pushing intents,
slots, routing, views, agent governance, statechart composition, and the
autonomy model into `extensions` and bespoke `call` functions — at which point
the document is ~90% extension wrapped around a foreign execution model, you have
inherited an inline-execution semantics your pure-core testability cannot live
inside, and the spec has bought you nothing but a conformance badge for the 10%
that was never the hard part. The authoring surface *is* the deliverable: authors
write intents, rooms, views, and agents, not `call`/`listen`/`fork`. A spec whose
vocabulary omits four of those five is the wrong surface to author in, however
good it is at the fifth.

### 5.5 The honest bottom line

Serverless Workflow 1.0 is the right answer to "orchestrate services and
functions in reaction to CloudEvents." A Kitsoki story is a different artifact: a
**navigable, human-facing, LLM-agentic statechart application** whose domain is
conversational intent routing, presentation, governed nondeterministic agents,
and statechart composition. We adopted the good ideas from the orchestration
tradition (typed data, guard/switch, parallel regions, typed errors, jq-style
expressions, statechart lineage from SCXML, CloudEvents-shaped events) and drew
the domain model where our actual problem lives. Choosing SW would not have
saved work — it would have relocated every hard part of the problem into the
spec's escape hatches while forfeiting the determinism boundary the system is
built on.

### 5.6 Landscape: presentation + agent + sandboxing

Serverless Workflow is the "why not this spec" question. The sharper question is
the *tool* one: **"someone must already do presentation + LLM agents + sandboxed
execution together — why not adopt that?"** The honest answer: several tools do
two of those three well, one all-in-one platform touches all three loosely, but
none integrates them the way a story does — as one authored, composable
statechart with a typed presentation surface bound to a dialogue grammar, behind
a pure-core replay boundary. Kitsoki is not novel on any single axis; it is novel
in the *integration*. Mapping the axes to the real prior art:

**Axis A — conversational domain model (intents / slots / flows / dialogue).**
The closest lineage to §3.4–3.5. [Rasa](https://rasa.com/docs/learn/concepts/calm/)'s
**CALM** is LLM-native dialogue where developers define flows + slots and the LLM
reads intent in context — explicitly hardened against hallucination and prompt
injection, exactly the concern §3.10 addresses for agents. [Microsoft Copilot
Studio](https://www.microsoft.com/microsoft-copilot/microsoft-copilot-studio)
pairs topics / trigger phrases with **Adaptive Cards** for presentation and
enterprise governance. Voiceflow / Cognigy / Botpress are the same shape. **What
they lack:** presentation is thin channel-specific cards, and there is no
author-brought sandboxed code layer nor a graded blast-radius model for
tool-using agents. They own dialogue; they don't own a UI or a sandbox.

**Axis B — presentation + agent (generative / streaming UI).** [Vercel AI SDK /
v0](https://vercel.com/blog/ai-sdk-3-generative-ui) streams React Server
Components (`streamUI`) so an LLM emits rich component UIs; Claude Artifacts and
OpenAI Canvas render model output in a sandboxed iframe; Chainlit/Streamlit wrap
agents in a UI. **What they lack:** the presentation is *generative* (the model
emits the components each turn), not a typed per-state view bound to an intent/slot
grammar — so it isn't deterministic, diffable, or cassette-replayable; sandboxing
is the deploy target, not a per-capability boundary; and there is no
statechart/intent model underneath. This is the axis where "underselling
presentation" bites — their presentation is impressive but *unruled*; ours is a
governed, replayable state-bound surface.

**Axis C — agent orchestration + human-in-the-loop.**
[LangGraph](https://www.langchain.com/langgraph) models agents as a graph with
first-class human-in-the-loop via checkpointing and durable state — the nearest
analog to §3.9 + §3.15's autonomy/decider machinery. AutoGen / Semantic Kernel /
CrewAI are peers. **What they lack:** no presentation layer, no built-in sandbox,
and no intent/slot dialogue grammar — they orchestrate agents, they don't render
to or converse with a user, and the sandbox is BYO.

**Axis D — sandboxing for agent code.** [E2B](https://e2b.dev) /
Daytona / Firecracker microVMs give hardware-isolated, ~150 ms-cold-start Linux
sandboxes for AI-generated code; Open Interpreter and code-interpreter are the
in-process cousins. **What they lack:** everything else — they are isolation
infrastructure, a substrate other tools call, not an authoring model.

**The all-in-one that comes closest: [Dify](https://docs.dify.ai/en/use-dify/build/workflow-chatflow).**
Its **chatflow** is a conversational app (memory, streaming answer nodes, rich
media) on a visual node canvas, with a **sandboxed code node** (Linux sandbox) and
a hosted web UI (link / iframe / API) plus tool and agent nodes. That genuinely
touches presentation + agent + sandbox + conversation in one product — the best
single answer to the user's question. Where it still differs from a story: it is a
**node-graph, not a statechart** (no hierarchical rooms, entry actions, history,
or a "where you are" location model); presentation is a **fixed chat UI**, not an
authored per-room typed view two-way-bound to intents; the sandbox is a **code
node you drop in**, not a capability-gated deterministic language a story *brings*
and an importer *rebinds*; there is no **statechart-fold composition** with
capability rebinding (§3.13); and there is no **pure-core record/replay of the
whole app** — you can't run the entire conversational application deterministically
with no LLM, which is a hard requirement here.

**Kitsoki's distinctive move: layered, authored sandboxing.** "Sandboxing" in most
tools is one thing (a microVM, a code node). A story stacks four isolation layers,
each authored and each auditable:

1. **Pure-core boundary** — the engine does no I/O; effects are *collected* and
   dispatched outside it (§3.9), so the whole app is replayable.
2. **Starlark capability sandbox** (§3.16) — author- *and* LLM-supplied glue runs
   hermetically with deny-by-default, per-capability grants (no clock, randomness,
   network, or shell unless explicitly injected), byte-for-byte replayable.
3. **Agent tool sandbox** (§3.10) — the pure/read/write/external effect ladder,
   `bash_profile`, `permissions`, and per-room `write_mode` operator grants gate
   what a *nondeterministic* agent may touch.
4. **Capsule workspace isolation** — agent file mutation happens in clone-backed
   managed workspaces, not the live checkout.

No tool in the landscape composes those four under one authored artifact. That —
not any single axis — is the answer to "why not an existing tool."

**Sources:** [Rasa CALM](https://rasa.com/docs/learn/concepts/calm/) ·
[Vercel AI SDK 3.0 Generative UI](https://vercel.com/blog/ai-sdk-3-generative-ui) ·
[LangGraph](https://www.langchain.com/langgraph) ·
[Dify Workflow & Chatflow](https://docs.dify.ai/en/use-dify/build/workflow-chatflow) ·
[E2B](https://e2b.dev)

---

## 6. Roadmap — what's missing, and what we plan to add

The model is deliberately incomplete. What follows is planned work, not shipped
behavior. Each item is stated honestly against what exists today, and each is
chosen because it extends the **same spine** — a pure machine, typed world,
composable statechart, capability-gated effects, and the deterministic↔fluent
spectrum of §4 — rather than bolting on a foreign concept. Nothing here changes
the core; they fill in its edges. The application-platform proposal contains a
[gap-closure matrix](../proposals/story-application-platform.md#paradigm-alignment-and-gap-closure)
that names which of these capabilities it implements, integrates, or consumes;
the full POG conformance gate may not silently waive a required row.

### 6.1 Event binding *(planned)*

Session-scoped event subscriptions that fire handlers **without a user turn**. A
room or the app would declare bindings from a typed event — a timer, a
background-job completion, an inbox / transport message, an external webhook or
ticket update, or an intra-fleet signal — to either a **background effect chain**
(mutate world, dispatch a host call, emit an intent) or a **synchronous
interrupt** that pulls the operator into a room for input. Today the pieces exist
only in specialized forms: `timeout` (a time event, §3.7), background
`on_complete` (a job event, §3.9), `host.inbox.add` (an async notice), and
parallel-region `emit` (§3.9). There is no unified, first-class event→handler
binding with session scope and an explicit background-vs-synchronous choice.
Unifying them turns "the story advances only when the user speaks" into "the story
reacts to its world" — what an unattended, long-running session needs. It is our
answer to Serverless Workflow's `listen`/`emit` (§5.3.1), but scoped to a live
session and able to choose background vs. human-synchronous handling.

### 6.2 Per-invocation routing mode *(planned)*

An explicit control — settable per run / per operation / per room / per invocation
— over **which routing tiers are allowed**: e.g. `deterministic-exact-only`,
`deterministic+synonym`, `semantic`, `llm`, or `off`. Today routing is an
app-level `routing:` block with a global on/off (§3.5); the determinism dial of §4
is real but is expressed by *how you author rooms*, not by a runtime knob. Making
it a first-class mode lets the **same** story be pinned to a zero-LLM,
exact-match-only classifier for a deterministic batch run (guaranteeing no model
spend and byte-for-byte replay) and unpinned to full fluency for an interactive
session — turning §4's spectrum from an authoring property into a per-invocation
dial. It also gives CI a **strict conformance mode**: any fall-through to the LLM
is a failure, not a silent cost.

### 6.3 Reusable component libraries — à la carte imports *(planned)*

Today the unit of reuse is a whole **story**, and composition brings in *rooms*.
`imports:` composes another *story* — a full app.yaml with a room graph and an
`entry` state — folded under an alias (§3.13); you cannot import "just an agent
library," because the fold installs the child as a compound state and needs states
to enter. `include:` *does* merge fine-grained pieces — agents, providers,
toolboxes, intents, meta-modes, states — but only as **local file-splitting within
one story**: a flat, un-namespaced merge (name collisions are errors), from local
globs, with no packaging or versioning. The planned [`kits`](../proposals/kits.md)
model would share whole stories plus `schemas`, `interfaces`, and `ui` — but
does not yet cover à-la-carte agents, toolboxes, intents, or providers.

So there is no first-class **reusable component library**: a shareable,
versioned, *namespaced* package of agents / toolboxes / intents / providers /
host-interfaces / schemas / UI components that a story imports à la carte,
*without* dragging in a room graph. Reusing a well-tuned judge persona, a
standard intent vocabulary, a card renderer, or a provider profile across ten
stories today means copy-paste, a same-repo `include:`, or importing an entire
story you don't want.

Planned: a **component-package** form — resolvable like a story (`./path`,
`@kitsoki/<name>`, `git+…`) — that `imports:` can pull selectively and
namespace. It also carries the reuse contracts identified by the
[story-programming paradigm §7.2](../architecture/story-programming-paradigm.md#72-the-oop-inheritancepolymorphism-gap):

- **room interfaces** — intent/slot/world contracts that any room may implement,
  loader-verified and targetable without naming one concrete room; and
- **parameterized room templates** — declared parameters for world slices, host
  bindings, and exit edges, expanded into visible concrete graph nodes.

This keeps composition as the default while making `agents`, `toolboxes`,
`intents`, `providers`, `host_interfaces`, room templates/contracts, schemas,
and application components first-class shared building blocks. It pairs
directly with 6.4: once components are shareable, they need semver.

### 6.4 Composition versioning *(planned)*

Real semver resolution for imports and kits. Today an import's `version:` is
metadata only (§3.13) and a kit's `extends`/`composes` carries a `constraint` that
is not enforced. A fleet that composes many stories needs constraint solving, a
lockfile, and conflict detection so a shared child story can evolve without
silently breaking its importers. Imports are how stories scale (§3.13), and scale
needs versioning — this is the composability objective's missing half.

### 6.5 Run-level cost / intelligence governor *(planned)*

A session budget ceiling that **interacts with the determinism dial**. Agents
already declare `token_budget` (§3.10) and operations declare stop/pause signals
(§3.15), but there is no run-level governor that, on approaching a cost ceiling,
**degrades gracefully** — routes deterministically, selects cheaper models, or
pauses for human approval — instead of simply refusing. This is the operational
form of §4's "choose how much intelligence to spend": spend becomes a first-class,
bounded, gracefully-degrading resource rather than a per-agent hard stop.

### 6.6 Dynamic fan-out *(planned)*

First-class spawning of a **runtime-sized** set of parallel sub-sessions with a
join. Today `parallel` states are *static*, author-declared regions coordinating
via `emit` (§3.7); there is no "for each item in a world collection, spawn a worker
region / sub-session and gather the results" — the dynamic supervisor/worker
pattern. Adding it (Serverless Workflow's `for` × `fork`, but over *governed agent
sub-sessions*) lets the multi-room agent model scale to fleets a single graph
cannot enumerate at author time.

### 6.7 Compensation / saga semantics for external effects *(planned)*

Declared **undo** for irreversible actions. `external`-tier effects (the top of
the §3.10 ladder) are by definition the ones you cannot replay away — a posted
comment, an opened PR, a sent email. Today `on_error` / `ack_error` handle
*forward* failure (§3.9), but there is no *compensation* (a declared inverse fired
when a later step fails) nor an idempotency-key contract for retried external
calls. Since governing irreversible actions is central to the model, saga-style
compensation is the missing safety net at the fluent-wizard end of the spectrum.

### 6.8 The learning loop — promote mined phrasings to the deterministic tier *(planned)*

Close the loop so **fluent runs make future runs cheaper and more deterministic.**
The mining and routing-test surfaces already capture real phrasings, and the
turn-cache replays resolutions (§3.5), but there is no automatic promotion of a
validated mined synonym / example into a room's deterministic grammar. Wiring that
loop means a story **starts fluent** (LLM-routed) and **drifts toward zero-LLM** as
its real vocabulary is learned and promoted — §4's spectrum traversed
*automatically over a story's lifetime*, and the strongest possible answer to "why
pay a model twice for the same sentence."

### 6.9 Deeper statechart static analysis *(planned)*

Model-checking beyond target resolution. Load-time already proves every
`target:` resolves, every `invoke:` is allow-listed, and every guard compiles
(§3.0); a fuller pass would prove:

- **reachability** — no orphan rooms, intents, handlers, actions, or UI nodes;
- **exit satisfiability** — every `@exit`'s `requires` set is achievable;
- **outcome totality** — every declared host/handler result variant has a named
  edge or an explicit collapse;
- **typed binds and guards** — every result-to-world bind and expression agrees
  with the world schema;
- **guard exhaustiveness** — a `default:`/`else:` or total cover on every
  decision gate;
- **room-interface conformance** and parameterized-room validity;
- **deadlock-freedom**; and
- **field-level impact** — one load-time read/write/call index answers which
  rooms, effects, handlers, and views depend on a world field.

The same index extends naturally into the application program graph:
page/card/component → action → handler/intent → transition/effect → world
read/write. For an artifact that aims to be deterministic and auditable,
provable well-formedness and queryable impact are the natural completion of the
load-time contract.

### 6.10 Application shell and surface projections *(planned)*

Today a story is a UI at the **room** level (§5.3): typed view blocks are bound
directly to intents and slots, and the same semantic view reaches TUI and web.
It is not yet a reusable application definition. Application-wide navigation,
page/card composition, custom Vue components, native VS Code projections,
Kitsoki-owned Vite/HMR, and one typed inbound handler exposed through
JSON-RPC/MCP/CLI still require surface-specific code.

[`story-application-platform`](../proposals/story-application-platform.md)
closes that boundary with a presentation-free application frame, optional
story-owned presentations, a common handler/event registry, versioned component
packages, and a program-graph conformance gate. The frame must stay a projection
of the same room/intent/world/effect graph: components dispatch declared
actions; they do not acquire hidden state or a second controller. POG is the
first external acceptance target for proving a product can be wholly
story-owned while retaining deterministic TUI/headless fallbacks.

---

*These are additive. The through-line: keep the pure-core / typed-world /
statechart / capability-gated spine fixed, and extend it toward reacting to events
(6.1), controlling the determinism dial explicitly (6.2, 6.5, 6.8), making
composition fine-grained, versioned, and concurrent (6.3, 6.4, 6.6), making
irreversible actions safe (6.7), proving the whole thing well-formed (6.9), and
projecting that same governed program as a reusable cross-surface application
(6.10).*
