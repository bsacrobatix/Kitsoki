# Prior Art

Kitsoki sits in the overlap of five established traditions: interactive
fiction parsers, statechart frameworks, conversational-AI dialogue
managers, LLM orchestration libraries, and the low-code automation /
data-app / BPM platforms most teams actually reach for first. None of
these on their own gives the shape kitsoki wants — free-text input, an
author-declared finite intent alphabet, deterministic transitions,
replayable history — but every one of them contributes a piece
(and the fifth contributes something better than a piece: most of
them are *hosts*, not rivals — see §5). This document records
the comparison so the design rationale stays legible as the system
evolves: when a design question comes back, the answer often involves
"we already chose A over B because…".

The framing is what kitsoki *steals* from each tradition and what it
*rejects*. A third question matters as much and is easy to skip: which
differences are **architectural** — a consequence of a commitment the
other side would have to abandon to match — versus which are
**inventory**, meaning effort, surface area, or connector count that
either side can simply go and build. Only the first kind belongs in a
moat argument. A feature another platform ships and kitsoki could add
next quarter is a `Steal` entry, not a differentiator on their side;
§7.7 applies that test explicitly.

---

## 1. Interactive fiction engines

**Inform 7** compiles an English-like source into an I6 program whose
parser matches input against *grammar tokens* that produce noun-phrase,
preposition, number, or unparsed-text bindings
([Writing With Inform §17.4](https://ganelson.github.io/inform-website/book/WI_17_4.html)).
Rules fire in declared precedence, and verbs can be adapted to new
syntactic forms via conjugation
([§14.3](https://ganelson.github.io/inform-website/book/WI_14_3.html)).
**TADS 3** takes a similar approach with explicit verb grammar rules
combined with `VerbProd` action maps and `singleDobj`-style slot
keywords that the parser fills
([Creating Verbs in TADS 3](http://www.tads.org/howto/t3verb.htm)).

**Ink** takes the opposite tack: knots and stitches as addressable
units, diverts (`-> london`) for flow, and choices presented as a menu
— no parser
([ink docs](https://github.com/inkle/ink/blob/master/Documentation/WritingWithInk.md)).
**Yarn Spinner** similarly uses nodes with headers/bodies and
`<<jump>>`/`<<command>>` directives
([Nodes and Lines](https://docs.yarnspinner.dev/write-yarn-scripts/scripting-fundamentals/lines-nodes-and-options),
[Commands](https://yarnspinner.dev/docs/write-yarn-scripts/scripting-fundamentals/commands)).
**Twine/Harlowe** makes passages the unit and treats everything —
including navigation — as macro calls on an interactive text surface
([Harlowe 3.3.8 manual](https://twine2.neocities.org/)).
**ChoiceScript** exposes `*choice`, `*label`, `*goto` as the whole
grammar
([Introduction to ChoiceScript](https://www.choiceofgames.com/make-your-own-games/choicescript-intro/)).

**Steal:**

1. Grammar-first *intent* declarations (Inform, TADS) — authors should
   think in verbs and object slots, not in `if`/`else`. Kitsoki's
   `intents:` block with typed `slots:` is the lineal descendant.
2. Addressable navigation units with diverts/jumps (Ink, Yarn) — a
   clean way to represent a state graph in text. Kitsoki's `states:`
   map plus `target:` is the same idea.
3. Distinction between the *narrative* surface (what the user sees)
   and the *mechanics* (state mutations) — every IF system separates
   these. Kitsoki's `view:` template vs. `effects:` enforces the same
   split.
4. Parser fallbacks that say "I didn't understand" with targeted
   nudges — kitsoki's `guard_hint:` and the structured error envelope
   from the MCP `transition` tool are the LLM-era equivalent.

**Avoid:**

1. Inform 7's natural-language rule-declaration aesthetic — readable
   to English speakers but famously hard for programmers to debug when
   precedence goes wrong. Kitsoki's DSL is structured YAML, not prose.
2. Twine's "everything is a macro in a passage" model — it leaks
   presentation into logic. Kitsoki separates view from transition.

---

## 2. Workflow and state-machine frameworks

**XState / statecharts** give us the vocabulary: hierarchical states,
parallel regions, guards as pure synchronous predicates, composable
`and()`/`or()` and the `stateIn()` predicate for parallel-region
cross-reference
([Guards](https://stately.ai/docs/guards),
[Parallel states](https://stately.ai/docs/parallel-states)).
**SCXML** is the W3C-standardized version with the same semantics in
XML, including explicit event-to-transition matching and cond
expressions ([W3C SCXML Rec](https://www.w3.org/TR/scxml/)).

**Temporal** enforces workflow determinism by replaying an event
history and comparing re-emitted commands to the recorded sequence
([Temporal Workflow Definition](https://docs.temporal.io/workflow-definition)).
**LangGraph** provides a graph API with conditional edges plus a
checkpointer (`SqliteSaver`, `PostgresSaver`) that snapshots state per
step under a thread ID
([LangGraph Persistence](https://docs.langchain.com/oss/python/langgraph/persistence)).
**BPMN** adds a vocabulary of gateway types — exclusive, parallel,
inclusive, event-based, complex — that is a useful sanity check when
designing transitions
([Camunda BPMN Reference](https://camunda.com/bpmn/reference/)).

**Steal:**

1. Compound/hierarchical states + parallel regions (XState, SCXML).
   Without these, "you're in the edit flow AND still have the inventory
   sub-state" gets expressed as a combinatorial state explosion.
2. Pure synchronous guards that evaluate against a state snapshot
   (XState, SCXML). Side-effecting guards are a nightmare to replay.
3. Event-history-as-truth (Temporal). Replay is straightforward when
   the log is the source of state — see
   [`architecture.md` §7](overview.md#7-persistence-replay-and-auditability).
4. Event-based gateways (BPMN) — "wait for whichever event arrives
   first" is the clean way to model LLM retry timeouts.

**Avoid:**

1. Temporal-scale durability. Kitsoki does not need workers,
   activities, and long polling; it hosts one conversation per session,
   per process invocation. The persistence model (SQLite + per-session
   event log) is deliberately the small version.
2. LangGraph's implicit state-is-whatever-you-return pattern — too
   flexible, too easy to lose track of what's persisted. Kitsoki's
   `world:` is a *declared* typed schema; effects are the only writer.

---

## 3. Conversational AI / dialogue frameworks

This is the tradition with the closest match to kitsoki's full shape,
and the comparison has to be done honestly: two production-grade
frameworks already implement the recognizer/manager split that the
rest of this document treats as kitsoki's central commitment. The
question is what's left after acknowledging that, not whether the
pattern is novel.

### 3a. Rasa CALM (Conversational AI with Language Models, 2024)

CALM is Rasa's pivot away from intent-classifier dialog management
toward an LLM-as-recognizer / flows-as-manager split
([Rasa CALM overview](https://rasa.com/docs/learn/concepts/calm/)).
The pieces:

- **`CommandGenerator`** translates free text into a fixed alphabet
  of *commands*: `StartFlow`, `SetSlot`, `Cancel`, `Clarify`,
  `ChitChat`, `HumanHandoff`, `SkipQuestion`
  ([Command Reference](https://rasa.com/docs/reference/primitives/commands/)).
  This is structurally identical to kitsoki's "free text → one of the
  declared intents."
- **`flows.yml`** declares flows as ordered `steps:`; the step types
  are `collect:` (ask for a slot), `action:` (run a custom Python
  action), `link:` (jump to another flow), `set_slots:`, `noop:`, and
  `if/else:` branches with `next:`
  ([Flows reference](https://rasa.com/docs/reference/primitives/flows/)).
- **`FlowPolicy`** executes the active flow deterministically against
  the command stream; the LLM never picks the next step.
- **`Tracker`** records every event (`UserUttered`, `SlotSet`,
  `ActionExecuted`, `FlowStarted`, `FlowCompleted`, ...) with full
  replay and "interactive learning" rewind.
- **Validation actions** re-prompt on rejected slot values; same
  shape as kitsoki's `SLOT_TYPE_MISMATCH` / `SLOT_NOT_IN_ENUM`.
- **Conversation-driven development** (CDD) — annotate tracker
  exports, suggest new flow steps or training phrases from real
  conversations.

CALM is mature: production at telcos, banks, governments since the
pre-CALM era (~2017); CALM-specific tooling shipped 2024–2025;
Rasa Pro adds hosted, on-prem, SOC2.

### 3b. Dialogflow CX, modern era (Generators, Generative Fallback, Playbooks)

CX's *page-with-form + routes* structure was already in §3's earlier
draft. The modern additions close most of the gap to kitsoki:

- **Generators** are LLM prompt templates with typed input/output that
  run inside a route's fulfillment, parameterised by session state
  ([Generators](https://cloud.google.com/dialogflow/cx/docs/concept/generators)).
- **Generative fallback** uses an LLM to handle no-match events with
  a system-instruction prompt rather than a static "didn't catch
  that" ([Generative fallback](https://cloud.google.com/dialogflow/cx/docs/concept/generative-fallback)).
- **Playbooks** are LLM-driven flows defined in natural language with
  declared `Tools` (OpenAPI / Webhook / Data Store / Function), used
  for the bits that don't fit the deterministic state graph
  ([Playbooks overview](https://cloud.google.com/dialogflow/cx/docs/concept/playbook)).
- **Webhook responses** (effects, in kitsoki terms) can fully rewrite
  page parameters and session state.
- **BigQuery export** ships every turn with intent, parameters,
  matched route, latency, and conversation ID — a labelled-datapoint
  log that predates the term.
- **Versions and environments** give A/B experiments per environment
  with traffic splits.

GA since 2020; runs at conversation volumes kitsoki will not see for
years.

### 3c. Bot Framework Adaptive Dialogs

Event-driven declarative trees with triggers, actions, `Recognizer`
/ `Generator` / `Selector` components, and adaptive expressions
([AdaptiveDialog class](https://learn.microsoft.com/en-us/javascript/api/botbuilder-dialogs-adaptive/adaptivedialog?view=botbuilder-ts-latest)).
The architectural shape is the same; the design centre is Microsoft
Composer's GUI authoring, which kitsoki rejects.

### Steal

1. **Recognizer / dialog-manager separation.** The single most
   load-bearing borrowing in the design — and one kitsoki *cannot*
   claim as original. Rasa CALM and Dialogflow CX both ship it.
2. **Page ≈ state with a form** (Dialogflow CX). A state's slot list
   is first-class and the runtime loops until filled. Kitsoki's
   `MISSING_SLOTS` retry surface is the same shape.
3. **Dynamic `required_slots`** (Rasa) — the shape of the form can
   depend on previously filled values. Kitsoki's per-transition
   `when:` guards express the same kind of conditional requirement.
4. **Conversation-driven development** (CALM CDD). Kitsoki's
   `replay-routing --target` and `inspect --synonym-suggestions` are
   embryonic versions; the mature pattern is worth borrowing more
   directly.
5. **First-class repair commands** (CALM: `Cancel`, `Clarify`,
   `CorrectSlot`, `SkipQuestion`). Kitsoki has off-path and
   meta-mode for free-form repair but lacks an explicit "user
   changed their mind about slot X" pathway. Worth adopting.
6. **Flow / page natural-language descriptions** that the LLM reads
   when choosing the next flow (CALM, Playbooks). Kitsoki's per-state
   intent allowlist is similar but doesn't have an explicit
   summary-prompt for the LLM to pick from.

### Avoid

1. **Rasa's story/rule training-data paradigm** — pre-CALM Rasa
   confused "authoring" with "example data." CALM mostly fixed this
   but `nlu.yml` examples remain part of the recommended setup. Kitsoki
   is author-first, no training corpus required.
2. **Dialogflow's hidden built-in intents and implicit
   sys-parameters** — opaque magic. Every intent in kitsoki is
   declared by the author.
3. **Playbook-style "LLM-drives-the-flow-in-natural-language"** — CX
   Playbooks let the LLM pick steps from a prose description. That's
   the recognizer encroaching on the manager. Kitsoki keeps the
   manager deterministic.

---

## 4. LLM-specific orchestration

### 4a. LangGraph (the closest match in the LLM-framework tradition)

LangGraph is the framework most often cited as kitsoki-adjacent. A
careful comparison shows the resemblance is real but shallower than
it first appears.

The pieces:

- **`StateGraph`** with a typed `State` (a `TypedDict` or Pydantic
  model). Nodes are Python functions of signature `(state) -> partial
  state update` (or a `Command` that combines update + routing)
  ([LangGraph low-level reference](https://langchain-ai.github.io/langgraph/concepts/low_level/)).
- **Edges:** static (`add_edge`) and conditional (`add_conditional_edges`)
  with a routing function returning the next node name (or `END`).
- **Reducers:** declared on each state key — e.g. `Annotated[list,
  add_messages]` — control how partial updates merge. The closest
  analogue to kitsoki's effect alphabet, but typed-Python not YAML.
- **Checkpointers** (`SqliteSaver`, `PostgresSaver`,
  `AsyncRedisSaver`) snapshot the full state per super-step under a
  `thread_id`; this is the persistence layer kitsoki's
  [`architecture.md` §7](overview.md#7-persistence-replay-and-auditability)
  cites as inspiration
  ([LangGraph Persistence](https://langchain-ai.github.io/langgraph/concepts/persistence/)).
- **Interrupts** for human-in-the-loop: static
  `interrupt_before` / `interrupt_after` on a node, or the dynamic
  `interrupt()` function that pauses execution mid-node and resumes
  via `Command(resume=...)`
  ([Human-in-the-loop](https://langchain-ai.github.io/langgraph/concepts/human_in_the_loop/)).
- **Time travel** — replay or fork from any checkpoint by
  `thread_ts`.
- **Subgraphs** with explicit state-schema mapping (or a shared
  schema) for composition.
- **Send API** for dynamic fan-out (Map-Reduce-style parallel work).
- **Streams:** state deltas, node updates, LLM tokens; all observable
  in real time.

LangGraph's bet is that *the application is a graph the engineer
writes in Python.* The agent / LLM is one node among many; the graph
routes it.

#### Where LangGraph and kitsoki overlap

| Concern | LangGraph | Kitsoki |
|---|---|---|
| State graph as the unit of control | ✓ `StateGraph` | ✓ `states:` |
| Typed state schema | ✓ `TypedDict`/Pydantic | ✓ `world:` |
| Conditional routing | ✓ conditional edges | ✓ `when:` guards |
| Per-step state mutations | ✓ partial updates + reducers | ✓ ordered effects |
| Checkpointed event history per thread | ✓ checkpointers | ✓ event log |
| Resume after restart | ✓ `thread_id` | ✓ session DB |
| Time-travel replay | ✓ from any checkpoint | ✓ from event log |
| Human-in-the-loop pause | ✓ `interrupt()` | ✓ `_awaiting_reply` states |
| Subflows | ✓ subgraphs | ✓ imports |

That's a lot of overlap. Now the gaps that matter.

#### Where LangGraph is not what kitsoki is

1. **No intent alphabet.** LangGraph has no notion of "the LLM must
   call one of these named operations." Routing happens *after* a
   node runs, based on the node's return value or the agent's
   tool-call name. Kitsoki's load-bearing constraint — *free text is
   resolved to one of the state's declared intents before any
   transition fires* — has no first-class analogue. Builders fake it
   with structured-output prompts, but the framework doesn't enforce
   it.
2. **State is whatever you return; no declared schema gate.**
   LangGraph's `TypedDict` is hint-only at runtime by default; nodes
   can shove arbitrary keys in. Kitsoki's strict loader rejects
   unknown world keys. (Per
   [feedback memory on LangGraph's implicit state model](#) — already
   recorded in this document's §2 "Avoid".)
3. **No author/operator separation.** LangGraph is for the
   *engineer* who writes Python. Kitsoki is for the *author* who
   writes YAML and never touches Python unless they're adding a new
   host handler. Story authors and runtime engineers are different
   roles with different review cadences.
4. **No semantic-routing tiers.** Every routing decision in
   LangGraph runs whatever Python the engineer wrote; there is no
   built-in "try deterministic match, then word-bag, then template,
   then cache, then LLM." Builders implement that themselves per
   project.
5. **No declarative effect alphabet.** Kitsoki's `set` / `increment`
   / `say` / `invoke` / `emit` / `emit_intent` / `background` /
   `bind` / `on_error` / `on_complete` are a small, declarative
   vocabulary the loader validates. LangGraph nodes are arbitrary
   Python; the "effect" is whatever the function does.
6. **No host-binding / capability surface.** Kitsoki imports declare
   `host_interfaces:` and parents rebind them per alias
   ([`docs/stories/imports.md`](../stories/imports.md)). LangGraph subgraphs share a
   process-global namespace of functions and tools; rebinding is
   "pass a different function in the closure."
7. **No emit-across-import-boundary mechanism.** Kitsoki's
   `IntentAliases` resolves a bare intent name (`accept`) emitted by
   a deeply imported child to the rewriter-renamed arc
   (`bf__accept` / `core__bf__accept`) at dispatch time. Subgraphs
   in LangGraph don't have this — sub-state is either shared or
   explicitly mapped, but there's no aliasing layer.
8. **No multi-surface transport contract.** LangGraph apps are
   serving Python over LangServe / FastAPI / Streamlit. Kitsoki ships
   TUI, MCP, Jira, file-append transports against one
   transport-agnostic machine.

#### Steal

1. **The checkpointer pattern with a `thread_id`.** Kitsoki's
   per-session event log is the same design.
2. **`interrupt()` as a paradigm.** LangGraph's mid-node pause-then-
   resume is a clean way to model long-running work that needs
   user input mid-flight. Kitsoki's `host.RequestClarification` is
   the same idea; the LangGraph API is worth studying as a model
   for the eventual host-call ergonomics.
3. **Time-travel by forking from a checkpoint.** Kitsoki's
   `replay --mode file_diff` is the rough equivalent; the explicit
   fork-and-edit-a-checkpoint UX in LangGraph is more polished.

#### Avoid

1. **State-is-whatever-you-return.** Already in §2's Avoid; LangGraph
   is the canonical offender among LLM frameworks.
2. **Planner agents that pick the next graph node from a prose tool
   description.** LangGraph supports this pattern (the `Supervisor`
   and `Swarm` templates) and many teams reach for it first. It's
   the recognizer encroaching on the manager, again.
3. **Per-project semantic routing reinvention.** Every LangGraph
   project that needs deterministic routing ends up writing the same
   "try regex, then keyword, then LLM" stack by hand.

### 4b. Structured-output and retry-feedback libraries

LLM-retry-with-validation-feedback is an established pattern:
libraries like Instructor pipe the Pydantic validation error back
into the next prompt, achieving >95% recovery at small-schema sizes
([overview](https://techsy.io/en/blog/best-llm-structured-output-libraries)).
MCP itself specifies that *tool* errors should live inside the
result envelope (`isError: true`), not as JSON-RPC protocol errors,
so the LLM sees them and can self-correct
([MCP schema reference](https://modelcontextprotocol.io/specification/draft/schema)).

**Steal:**

1. Validation-feedback retry loop with a bounded budget. Kitsoki's
   harness retries on a structured error from the machine's validator
   before falling through to a clarify-the-human surface.
2. Structured tool errors in-band, always JSON, with a `suggestions`
   array the LLM can read. The error-code enum in
   [`state-machine.md` §4](../stories/state-machine.md#4-intents-and-slots) is the
   stable contract for those errors.

**Avoid:**

1. "Planner" agents that reason about what tool to call over multi-step
   plans. Kitsoki wants a one-shot extraction: free text → intent. If
   the user needs multi-step, the state graph models it, not the LLM.

### 4c. CodeAct / OpenHands (executable-code-as-action)

CodeAct ("Executable Code Actions Elicit Better LLM Agents", Wang et al.,
ICML 2024 — the paradigm behind OpenHands/OpenDevin and HuggingFace
`smolagents`' `CodeAgent`) makes the opposite control-level bet from every
other framework in this document: instead of a constrained tool-call format,
the agent's action space is **executable code**. A live interpreter runs the
emitted Python, stdout/stderr/return values come back as observations, the
model self-debugs from execution errors and loops until it decides it's
done. The claim is capability — code gives loops, conditionals, and
composition in one action, so agents do more in fewer steps than a
JSON-tool-call format allows.

Kitsoki's `transition` tool (§6) is exactly the constrained format CodeAct
argues against — deliberately: CodeAct wants a more capable autonomous
agent, kitsoki wants a workflow where "the model probably does the right
thing" isn't enough (`concept.md` §1). But kitsoki doesn't reject
code-as-action wholesale — it already embeds a fully-controlled code
runtime, Starlark (`host.starlark.run`, go.starlark.net), and the
**`agent.codeact` verb** (`internal/host/codeact`) is CodeAct's loop
re-admitted on top of it: the LLM emits Starlark snippets against an
author-declared capability allowlist, each step executed in a traced,
budget-bounded thread, observations (a return dict or a structured error
envelope) feed back, and a final `done(payload)` is schema-gated before
control returns to the state machine. It slots into the agent-verb taxonomy
(§7.4) between the finite intent alphabet (max control, min expressivity)
and `task` (max expressivity, min control) — composable like `task`, but
every action traced, deterministic-by-construction, bounded, and
no-LLM-replayable.

Starlark beats a raw Python REPL as the substrate precisely because kitsoki
owns the interpreter, so CodeAct's central trade — expressivity *for*
auditability — disappears:

| | CodeAct (Python) | `agent.codeact` (Starlark) |
|---|---|---|
| Sandbox | bolted-on (containers/seccomp); blast radius = whatever the model imports | intrinsic — no ambient I/O; the action space **is** the set of Go builtins the author exposes, loader-checked like every other verb's blast radius |
| Tracing | none by default | every builtin call + step journaled (same event log every other verb writes to) |
| Errors | raw tracebacks | a structured error envelope, the same contract as the validation-feedback retry loop (§4b) |
| Termination | unbounded | no `while`/recursion is free in Starlark; a step budget bounds the outer loop too |
| Replay | none — rerunning yields a new trajectory | pure interpreter + cassette'd HTTP/builtin calls → a recorded trajectory replays with zero LLM and zero live side effects (provable in CI, per the no-real-LLM-in-tests rule this whole document's `Steal`/`Avoid` sections assume) |

The payoff is a **promotion ratchet** (`concept.md` §4, "code as artifact"):
`agent.codeact` and `host.starlark.run` share one interpreter and one
capability model, differing only in authorship — a live trajectory can be
extracted (`internal/host/codeact.ExtractTrajectory`) and frozen into a
committed `.star` file, swapping the exploratory `agent.codeact` invoke for
a deterministic `host.starlark.run` call with zero further agent dispatch.
This only applies when a call's answer is genuinely a fixed function of its
inputs — a first dogfood pass tried to promote `stories/bugfix`'s
ticket-triage room and correctly refused: a triage verdict is
ticket-specific, so freezing one trajectory would silently give every
future ticket the same stale answer. The ratchet is real, but it isn't a
substitute for judgment about what's actually reusable.

#### Transports: CLI vs direct API

`codeact.Run` treats its `Agent` as a black box, so the per-step LLM call has
two interchangeable backends, selected by `AgentCodeactHandler`:

- **`RealCodeactAgent`** (`internal/host/agent_codeact_real.go`, the default)
  forks one `claude -p` per step, gating the step on the same mcp-validator
  `submit` mechanism `host.agent.decide` uses. Selected when the effect's
  `agent:` resolves to the default (`builtin.claude_cli`) or to no plugin.
- **`ApiCodeactAgent`** (`internal/host/agent_codeact_api.go`) calls an
  OpenAI-compatible model API directly — one `{endpoint}/v1/chat/completions`
  POST per step, the fixed `{action: snippet|done}` discriminated-union schema
  sent as `response_format: json_schema`. Selected when `agent:` resolves to a
  `builtin.local_llm` plugin (the opt-in check `reg.IsLocalLLM(pluginName)` in
  `AgentCodeactHandler`); it generalizes `internal/agent/local_llm.go`'s client
  rather than forking a CLI.

The direct-API path exists because every step otherwise inherits the
external-CLI friction this codebase keeps hitting — `tool_search`/MCP-approval
dance, sandbox-bypass flags, quota inference from stdout — none of it inherent
to the *model*, all of it the foreign harness layer in between. Config is the
existing plugin-routing convention `decide` already uses: the `agent:` name is
declared in both `agents:` (persona) and `agent_plugins:` (transport), sharing
one key; the `builtin.local_llm` entry adds two knobs for an authenticated
OpenAI-compatible endpoint:

```yaml
agents:
  agent.glm:
    system_prompt: "You triage bugs by emitting Starlark snippets against ctx..."
agent_plugins:
  agent.glm:
    plugin: builtin.local_llm
    endpoint: https://open.bigmodel.cn/api/paas/v4
    api_key_env: GLM_API_KEY   # env-var NAME; secret read at call time, never in YAML
    model: glm-4.6
    json_schema: true          # response_format: json_schema for any schema (OpenAI-native),
                               # independent of the llama.cpp grammar-subset gate `grammar` governs
```

The done()-payload schema gate, the structured-error feedback, the step
journal, and the no-LLM cassette/replay are all unchanged — `ApiCodeactAgent`
is just a different `Agent` impl fed into the same `codeact.Run`, so a recorded
trajectory replays identically regardless of which transport produced it.

**Steal:**

1. Code as the action for compositional, capability-scoped tasks — CodeAct's
   core insight, kept, with the interpreter's ambient I/O sealed off by
   default so the insight doesn't cost the auditability the rest of this
   document argues for.
2. Two-tier cassetting an agent loop: the LLM's emitted decisions
   (episode-level, like every other agent verb) and the interpreter's own
   I/O (exchange-level, generalizing the existing HTTP-cassette format) are
   recorded and replayed independently — necessary because a code-acting
   loop has two distinct sources of nondeterminism, not one.

**Avoid:**

1. An unsandboxed, Turing-complete action space. CodeAct's Python REPL
   trades away exactly the properties (determinism, replay, a loader-checked
   blast radius) this document's `Steal` sections keep collecting from every
   other framework.
2. Promoting a trajectory whose answer isn't actually a fixed function of
   its inputs — the ratchet's refusal case above, not a hypothetical.

---

## 5. Low-code automation, data apps, and BPM platforms

§§1–4 compare kitsoki to things engineers choose deliberately. This
section compares it to what teams are *already running* when the
question "should we build this on kitsoki?" comes up. n8n, Airtable,
and Appian are the three rungs of that ladder — self-hosted
integration automation, the spreadsheet-database that ate operations,
and enterprise BPM — and each is more likely to be the real incumbent
for a given workflow than CALM or LangGraph ever will be.

The tradition shares one assumption, and it is the assumption kitsoki
inverts:

> The process is a **diagram or a table that an operator drives**, and
> the LLM is a node inside it holding whatever credentials that node
> was given.

Kitsoki puts the LLM at the *edge* as a recognizer, and makes the
process a typed artifact in git whose blast radius the loader checks.
Everything below follows from that one difference.

### 5a. n8n

Node-based workflow automation on a visual canvas: trigger nodes,
several hundred integration nodes, a `Code` node for JS/Python, and
`Execute Sub-workflow` for decomposition. Self-hostable under a
fair-code licence, which makes it the default answer whenever "we
can't send this to a SaaS" is a constraint. Its LLM story is the
LangChain-derived `AI Agent` node — tools attached to an agent that
picks among them.

The execution model is the load-bearing difference. An n8n *execution*
is a payload of items flowing node-to-node, triggered by an event and
finished when the last node returns; it is a **run, not a session**.
Human-in-the-loop exists (`Wait` with a webhook resume, Slack/email
approval nodes) but it is pause-and-resume on a run, not a state that
holds an intent allowlist while a human deliberates for three days
across two conversations.

Sub-workflows pass a data payload in via the `Execute Sub-workflow
Trigger`'s declared input fields and return the last node's output
([n8n — Sub-workflows](https://docs.n8n.io/build/flow-logic/break-workflows-into-smaller-parts)).
That's a real parameter contract — closer to kitsoki's `world_in:`
than CALM's global slot bag. What it lacks is the other half:
the child's *capabilities* are fixed inside the child, because
credentials bind to nodes. Running the same pipeline against Jira in
production and local files in a fixture means duplicating the
workflow, not rebinding an interface (§7.3).

### 5b. Airtable

A relational-ish database with views, an interface builder,
automations (trigger → actions, including a scripting action), AI
fields that write a computed value into a column, and — since the
Omni/Cobuilder generation — an LLM that assembles the schema and UI
from a prompt.

Airtable's model of a process is a **`Status` single-select on a
record**, moved by a human in a view or by an automation. That sounds
primitive next to a statechart, and as a formalism it is: no guards,
no per-state action allowlist, no validation beyond field types, no
replay past revision history. But it is the most successful workflow
substrate in this document by adoption, for reasons kitsoki should
take seriously — the operator surface is free. Filter, sort, and group
every in-flight instance; hand a non-technical owner the base; get a
form and a mobile app without asking anyone. Kitsoki has none of that
and is not trying to.

Airtable's AI fields are also, quietly, the most *contained* LLM
integration in this section: a field agent's write target is one
column on one record. That is a real blast-radius boundary, arrived at
by data-model accident rather than by design, and it is narrower than
what n8n's or Appian's agents get.

### 5c. Appian

Enterprise low-code BPM. Process models are BPMN and the model *is*
the runtime — "that model isn't just the starting point for building
your process, it is the process"
([Appian — Process Modeling](https://docs.appian.com/suite/help/25.4/process_modeling.html)).
Around it: SAIL interfaces, Records and Data Fabric, task assignment
to users and groups with SLA timers and escalation, RPA, process
mining, and the compliance posture (FedRAMP, HIPAA, SOC) that makes it
the answer in regulated industries. Since 25.4, Agent Studio builds AI
agents from a prompt plus reusable tools that "connect to your data
fabric and processes," dropped into a process model via the
`Execute AI Agent` smart service
([Appian — Agent Studio](https://docs.appian.com/suite/help/25.4/agent-studio.html)).

Appian is the closest thing in this document to "durable process with
human tasks, typed data, and an audit trail" — the things kitsoki
argues for, shipped at enterprise scale a decade ago. The divergences
are authorship and enforcement. Process models are objects in a
proprietary repository with Appian's own versioning, authored in
Appian Designer; they are not text you review in a pull request,
bisect, or hand to a coding agent. And an `Execute AI Agent` node's
constraint on what the agent may do is the prompt plus which tools
were attached — configuration, not a loader-checked property of the
*call kind* (§7.4).

### The important part: these are hosts, not rivals

For most of §§1–4 the framing is "kitsoki instead of X." Here it is
usually **"kitsoki on top of X."** Kitsoki already draws its boundary
at the host interface, and these platforms sit on the far side of it:

```yaml
host_bindings:
  ticket: host.airtable      # the record, the views, the human owner
  effector: host.n8n         # a webhook into 400+ integrations
  inbox: host.appian         # the enterprise task queue and its SLAs
```

An `invoke` into an n8n webhook is a perfectly good effect. An
Airtable base is a perfectly good `ticket` binding — it is a better
one than a flat file for anything a human has to browse. Kitsoki has
no connector marketplace and no UI builder, and building either would
be a strategic error. What kitsoki contributes is the layer none of
them have: **the process itself as a reviewable, testable, importable
artifact with the LLM's blast radius enforced rather than configured.**

This is also why their advantages read as inventory rather than moat
(§7.7). The binding is one-directional: kitsoki can absorb a connector
catalog, a grid, or a task inbox by naming it in `host_bindings:`,
while nothing in an n8n canvas or an Airtable base can import a
loader-checked capability surface — that would require the process to
stop living on a canvas or in a hosted schema. Anything on the far
side of the host boundary is borrowable; the boundary itself is not.

### Steal

1. **n8n's per-node execution inspector and pinned data.** Seeing the
   exact input and output items at every node, and freezing one node's
   output so downstream iteration doesn't re-hit live APIs, is the
   best authoring loop in this document. Kitsoki has the ingredients
   (`session_trace`, cassettes) but not the "pin this one
   `on_enter` result and iterate the rest" ergonomic.
2. **n8n's workflow-level error workflow.** A named handler that
   catches any failure anywhere in the run. Kitsoki has per-effect
   `on_error:`; a story-level default error route is the missing
   piece — the current alternative is remembering to write `on_error:`
   everywhere.
3. **Airtable's "process state is a queryable field."** The
   many-instances-at-once operator view — all sessions in state X,
   grouped by phase, sorted by age — is something kitsoki's event log
   already has the data for and nothing currently reads. The daemon's
   job list is the embryonic version.
4. **Appian's timers and escalation on a human task.** Every
   `_awaiting_reply` state in kitsoki waits forever. Appian's
   assign-to-group, SLA timer, reassign, and escalate vocabulary is
   the mature version of a gap kitsoki genuinely has — most visible on
   the `judge_mode=human` path of §7.1.
5. **Appian's process mining.** Mining real instances to find the
   state where everything piles up. Kitsoki's per-session event logs
   are exactly the input this needs.

### Avoid

1. **Canvas-as-source** (n8n, Appian). When node UUIDs and canvas
   coordinates live in the artifact, diffs are unreadable, merges
   conflict on layout, and two agents cannot work the same process in
   parallel. Kitsoki's source is text and any layout is derived —
   which is also why an agent can edit a story at all.
2. **Hosted-only state** (Airtable, Appian). If the process cannot run
   on a laptop against a fixture, the millisecond test surface of
   §7.5 cannot exist, and the authoring loop is bounded by a network
   round-trip to someone else's environment.
3. **Agent-with-tools *as* the process** (Appian's `Execute AI Agent`,
   n8n's `AI Agent` node). The third appearance of the same critique
   levelled at CX Playbooks (§3) and LangGraph's `Supervisor` (§4a) —
   the recognizer encroaching on the manager. It is the default
   shape being shipped across the entire industry right now, which is
   precisely why kitsoki keeps refusing it.
4. **Letting per-seat or per-execution pricing shape the
   architecture.** Metered runs push authors toward fewer, fatter
   steps that do more per LLM call. Kitsoki's cost model rewards the
   opposite: many small steps, most of which never reach an LLM at all
   (§7.8, semantic routing tiers).

---

## 6. Why one generic MCP tool, not per-state typed tools

The most consequential MCP design choice was to register a single
`transition` tool with `{intent, slots}` payload, instead of a typed
tool per intent (or per state) that the LLM picks from.

Two reasons:

1. **Tool-list churn defeats caching.** The LLM's tool list would
   change every turn. Most LLM tool-calling APIs cache the tool list
   per session; reshuffling it per turn defeats caching and adds
   latency.
2. **Per-intent tools leak the author's internal names.** Authors
   would be forced to expose their intent identifiers (`hang_cloak`,
   `restart_from`) in tool schemas the LLM sees. That couples the LLM
   prompt to the app's internals and makes refactoring fragile.

With one `transition` tool the LLM's job is uniform across apps:
given the current state's intent catalog (included in the system
prompt), call `transition` with one of them. The validator does the
shape-checking and returns a structured error envelope on mismatch.

The trade-off accepted: the LLM has slightly more freedom to call the
wrong intent name, which the validator catches on the way in. In
exchange the prompt is stable, the cache hits, and refactoring the
intent vocabulary doesn't break the tool catalog.

This decision is not theoretical-only: today `internal/mcp/server.go`
registers exactly one tool by this name, and the validator is the
gatekeeper rather than the schema.

---

## 7. Why kitsoki, given that CALM, CX, LangGraph, n8n, Airtable, and Appian all exist

The honest reading of §§3–4 is that kitsoki's *separation of
recognizer from dialog manager*, *event-sourced replay*, and *typed
slot-filling with validation feedback* are no longer differentiators.
CALM ships them. Dialogflow CX ships them. LangGraph ships the
graph + checkpointer half. Anyone claiming "kitsoki is the first
deterministic-core conversational engine" is several years late. §5
adds a blunter version of the same problem: for most workflows the
real incumbent is not a framework at all, it's an n8n canvas, an
Airtable base with a `Status` column, or an Appian process model — all
of which already work, already have an operator UI, and already have
someone maintaining them.

This section is the worked example of what *is* differentiated. It
uses `stories/bugfix/` — the seven-room bugfix pipeline — because it
exercises every load-bearing kitsoki mechanism in one self-contained
story. If a competing framework can express the same story with the
same guarantees and at comparable cost, kitsoki is redundant. If it
can't, the gap names what kitsoki is for.

### 7.1 Judge polymorphism with one `on_enter` chain

The bugfix story has three judge modes — `human`, `llm`, and
`llm_then_human` — selected by `world.judge_mode`. The defining
contract property:

> Every `_awaiting_reply` state runs **the same `on_enter` chain** in
> all three modes. The seven checkpointed rooms have identical
> `on_enter` shapes; only `<phase>` and the next-room target vary.
> ([`stories/bugfix/README.md`](../../stories/bugfix/README.md))

The judge runs a single `host.agent.decide` call gated by `when:
judge_mode != "human"`. The verdict lands in `world.llm_verdict`; an
`emit_intent:` effect auto-fires the verdict's intent in the same
turn when `confidence >= judge_confidence_threshold`. The state
graph is identical across modes; the *operator* swaps in and out.

What this requires from the runtime:

- A pure declarative effect (`emit_intent`) that synthesises a turn
  inside the current turn, with a depth cap
  (`machine.EmitIntentMaxDepth = 8`).
- A `when:` guard language that can read both `world.*` and the
  bound result of the previous agent call in the same `on_enter`.
- A view layer that re-renders deterministically after the auto-fire
  resolves, with no LLM call.

What it produces:

- **One story, three deployment shapes.** The same artifact is the
  fully-manual triage tool, the fully-automated CI bot, and the
  hybrid escalation pipeline.
- **Replay parity across modes.** A `judge_mode=human` flow fixture
  and a `judge_mode=llm` flow fixture exercise the same state graph
  — divergence is a runtime bug, not an authoring concern.

What it would cost in CALM: three flows or one flow with imperative
`if/else` over `slots.judge_mode` at every checkpoint, replicated
seven times. The auto-fire requires a custom action that tail-calls
into the next flow step; CALM's command stream is not
author-extensible, so the engine cannot enforce the `emit_intent`
contract. The `host.agent.decide` verdict's typed return
(`{verdict, intent, reason, confidence}`) becomes a custom Python
action with no schema gate.

What it would cost in Dialogflow CX: a webhook on every
`_awaiting_reply` page that fans out to one of three branches based
on a session parameter, with the LLM call written by hand inside the
webhook. The branch-equivalent of `emit_intent` is a same-turn page
transition fired from the webhook response — possible but with no
framework guarantee of replay determinism.

What it would cost in LangGraph: an engineer writes a Python node
that branches on `state["judge_mode"]`, calls the LLM if needed,
and returns a `Command` with the next node name. The seven
checkpoints become seven near-identical functions with a shared
helper. The contract that "all seven have identical shape" lives in
code review, not in a loader-enforced schema.

### 7.2 Cycle budgets as a declarative pattern

The bugfix story declares per-phase budgets:

```yaml
world:
  reproducing_cycle: { type: int, default: 0 }
  reproducing_budget: { type: int, default: 3 }
  # ... same shape for proposing/testing/validating/done
```

The `refine` arc at each checkpoint is gated by
`when: <phase>_cycle < <phase>_budget`. When the counter hits the
budget, the next `refine` routes to `@exit:abandoned` with
`abandon_reason=<phase>_cycle_budget_exhausted` instead of looping.
`restart_from` rewinds and resets the target phase's counter to 0.

The phase-template version of this (
[`state-machine.md` §13](../stories/state-machine.md#13-phase-templates))
compresses the seven-room expansion into one template plus per-phase
parameters; the loader auto-synthesises the counter increments and
guards.

This is a small, declarative retry-and-budget pattern that the
loader can validate and the replay log can audit. CALM's equivalent
is a `set_slots:` step plus an `if/else:` branch at every
checkpoint, written by hand. LangGraph's equivalent is incrementing
an integer in `state` and branching on it in a routing function,
also by hand. Neither framework has a *named* concept of a
phase-scoped retry budget that an author can declare and the engine
enforces.

### 7.3 Sub-story imports with capability rebinding

The bugfix story is *importable*. The dev-story and kitsoki-dev
stories embed it under an alias:

```yaml
imports:
  bf:
    path: stories/bugfix/app.yaml
    entry: idle
    world_in:
      ticket_id: world.current_ticket.id
      workdir: world.workspace.path
    hosts: declared            # strict — child can only invoke listed hosts
    host_bindings:
      ticket: host.jira
      vcs: host.bitbucket
      ci: host.jenkins
```

What this gives:

- **Capability rebinding by alias.** The child declares
  `host_interfaces: [ticket, vcs, ci, workspace, transport]` with
  default bindings (`host.local_files.ticket`, `host.git`, etc.).
  The parent can rebind any of them per import, swapping the local
  binding for `host.jira` / `host.bitbucket` / `host.jenkins`
  without touching child YAML.
- **World isolation with explicit projection.** The child sees only
  the world keys the parent projects via `world_in:`; everything else
  is the child's private namespace. On exit, per-`@exit:` `set:`
  blocks project results back. This is the inverse of LangGraph's
  shared-namespace subgraphs and CALM's global slot bag.
- **Host gating** (`hosts: declared`) — the child cannot invoke a
  host the parent did not authorise. The loader enforces this; the
  runtime cannot escape it.
- **`emit_intent` across the import boundary.** When the LLM judge
  inside the imported `bf` alias emits the bare intent `accept`, the
  runtime walks the leaf state's `IntentAliases` map to resolve it to
  the rewriter-renamed arc (`bf__accept` or, two layers deep,
  `core__bf__accept`). The author writes `emit_intent: accept` once;
  the runtime makes it work at any import depth. See
  [`docs/stories/imports.md`](../stories/imports.md) "emit_intent across the fold
  boundary" and `resolveEmittedIntentName` in the runtime.

This composition story has no equivalent in CALM, Dialogflow CX, or
LangGraph. CALM flows can `link:` to other flows but share a global
slot namespace and have no capability-binding mechanism — the same
`action_open_pr` runs on every parent. CX flows are not parameterised
sub-apps. LangGraph subgraphs come closest (with explicit state
mapping) but have no notion of a capability surface that the parent
rebinds per child instance.

### 7.4 Agent-verb taxonomy with per-verb guarantees

Each agent call in `bugfix` carries an `agent:` selecting a persona,
and uses the verb dictated by the call's blast radius
([`stories/bugfix/README.md` §Agent-split persona table](../../stories/bugfix/README.md)):

| Persona | Verb | Why this verb |
|---|---|---|
| `reproducer`, `implementer`, `test_author`, `validator` | `task` | Agentic; may read or write files |
| `proposer` | `ask` | Read-only structured analysis; carries `bash_profile: read-only` |
| `judge` | `decide` | Verdict-only; typed return `{verdict, intent, reason, confidence}`; no file access |

The verb is the framework-level guarantee. `ask` and `decide` cannot
mutate files because the host handler enforces it (read-only Bash
profile, sandboxed validator). `task` carries an acceptance loop
with replay modes A/B/C
([`docs/architecture/hosts.md`](hosts.md) §replay modes). The author picks the
verb; the runtime enforces the contract.

CALM has one `action:` step type; the read-only vs read-write
distinction lives in the Python class the engineer writes. CX
webhooks have no read-only mode at all. LangGraph nodes are
arbitrary Python.

This matters specifically for the bugfix story because the
`judge_mode=llm` path *must* be side-effect-free — a judge that
accidentally writes a file corrupts the workspace mid-pipeline. The
`decide` verb makes that a loader-enforced property, not a
code-review property.

### 7.5 The determinism surface: fixtures, cassettes, capsules, sandbox

`stories/bugfix/flows/` contains 25 YAML fixtures, each a scripted
sequence of intents-with-slots that exercises a path through the
state graph against stub host envelopes. They are not LLM tests —
they're FSM tests with the LLM stubbed at the agent boundary.

```
happy_human.yaml                       — accept at every checkpoint
happy_llm.yaml                         — judge_mode=llm, confident verdict
llm_uncertain_holds.yaml               — uncertain verdict holds the state
refine_budget_exhaust_reproducing.yaml — exact counter-equals-budget edge
restart_from_resets_budget.yaml        — restart_from clears the counter
jump_to_each_target.yaml               — every jump alias, including unknown
mode_switch_full_to_quick.yaml         — bugfix_mode mid-flow flip
mixed_judge_swap.yaml                  — judge_mode swap mid-run
```

Each fixture runs in milliseconds and asserts the full event-log
shape. The complete suite covers the state graph as exhaustively as
unit tests cover a function. This is feasible only because (a) the
machine is pure, (b) the host boundary is the only LLM seam, and (c)
the fixture format is identical to the runtime event log.

CALM has e2e tests with stubbed LLM responses, but they run through
the full pipeline (NLU, command generator, policy, action server)
and are minutes-not-milliseconds. CX has test cases but they target
intent recognition, not state-graph traversal. LangGraph has unit
tests on individual nodes; whole-graph deterministic tests are an
engineering exercise per project.

This is downstream of [memory: tests must be fast]: a story author
runs the full bugfix fixture suite on every YAML edit and gets
feedback in under a second.

#### The fixtures are the visible layer of a larger commitment

Fast state-graph tests are one consequence of a property that runs
deeper than any single row of the §7.7 table: **every source of
nondeterminism in kitsoki has a named, recorded, replayable seam**,
and no automated gate is permitted to cross one live. The state graph
is the seam the fixtures above cover; there are four more:

| Seam | Mechanism |
|---|---|
| The agent's *decision* | episode-level `host_cassette` — a recorded episode replaces a whole handler ([`hosts.md`](hosts.md)) |
| The agent's *I/O* | exchange-level HTTP cassettes with declared match fields and `record_mode: none \| once \| new_episodes \| all` |
| The *repository* under a workspace verb | hermetic capsules — `capsules/<name>/capsule.yaml` + `capsuletest.Open(t, …)` instead of ad-hoc `git init` |
| The *executor* running agent work | the fake executor that capsule CI's gates run against |

The cost of the equivalent elsewhere is the point. CALM's e2e tests
stub the LLM but still run NLU → command generator → policy → action
server, in minutes. LangGraph gives you a checkpointer and leaves the
rest as an engineering exercise; recording an agent's HTTP at
exchange granularity *and* its decisions at episode granularity *and*
its git fixture is three separate homegrown systems per project. n8n
re-runs an execution against pinned node data by hand. Appian requires
an environment. Airtable has nothing.

Two consequences that are not obvious from "we have good tests":

1. **Cost is bounded by policy, not by discipline.** No automated gate
   in this repository can reach a real model — a rule the whole
   `Steal`/`Avoid` framing above quietly assumes. Anywhere else on
   this list, "our CI accidentally spent money" is a live failure
   mode; here it is a structural impossibility.
2. **A recorded trajectory is a reproduction.** A bug in an agent
   interaction replays into a failing test rather than into a
   paragraph describing what the model did that day —
   `internal/host/agent_converse_replay_repro_test.go` exists because
   that is the normal way a bug is filed here.

#### Starlark: the sandbox is not the differentiator, the unification is

Sandboxed code execution is table stakes in 2026 — E2B, Modal,
`smolagents`' sandboxed executors, n8n's Pyodide `Code` node.
Starlark itself is unremarkable too (Bazel, Buck, Tilt, Drone). What
is not commodity is that kitsoki uses **one interpreter with one
capability model for three different jobs**:

- author-written glue (`host.starlark.run`),
- agent-written exploratory code (`agent.codeact`, §4c),
- and the frozen artifact a promoted trajectory becomes.

Because they share a substrate, the action space *is* the set of Go
builtins the author exposed — loader-checked exactly like every other
verb's blast radius (§7.4) — so there is no ambient I/O to sandbox
against in the first place, every builtin call lands in the same event
log every other effect writes to, and a recorded trajectory replays
with zero LLM and zero live side effects. The competitors' sandboxes
are containment bolted onto a general-purpose interpreter; the
question they answer is "what damage can this contain?" Kitsoki's
question is the narrower and more useful one: "what was this call ever
able to touch?" — answerable from the YAML, before it runs.

That is also what makes the promotion ratchet of §4c possible at all:
swapping an exploratory `agent.codeact` invoke for a deterministic
`host.starlark.run` on a committed `.star` file is a one-line change
precisely because nothing about the capability surface or the trace
changes underneath it.

### 7.6 The same story on n8n, Airtable, and Appian

§§7.1–7.5 costed the bugfix story against frameworks an engineer
picks. Costing it against the platforms a team already runs is a
different exercise, because two of the three would get a working
version of it up faster than kitsoki would — and then stop being able
to change it safely.

**n8n.** The seven checkpointed rooms become a canvas with `Wait`
nodes and webhook resumes; the judge is an LLM node with an `IF` on
`judge_mode`. This is achievable, and for a one-team pipeline with a
handful of integrations it is the pragmatic choice. Where it stops:
the three judge modes are three branch subtrees on a canvas, not one
`on_enter` chain, so "all seven checkpoints have identical shape"
(§7.1) has no enforcement and drifts on the first hurried edit. The
cycle budgets of §7.2 are `Code` nodes incrementing a static-data
counter with an `IF` after each. The import of §7.3 gets the input
contract but not the rebinding — production-Jira and fixture-local
are two copies of the workflow. And the 25 fixtures of §7.5 have no
analogue at all: you re-run executions against pinned data, by hand,
one path at a time.

**Airtable.** A `Bugs` table, a `Phase` single-select, an automation
per transition, an AI field for the judge verdict. Fastest to a demo
by a wide margin, and the resulting operator surface — every bug in
flight, grouped by phase, owned by a name — is better than anything
kitsoki ships. Where it stops is earlier than n8n: there is no
guard language, so nothing prevents a record moving from `reproducing`
straight to `done`; the cycle budget is a number a human is trusted
not to ignore; and the story is not an artifact — it is a base
configuration that cannot be diffed, reviewed, imported into another
base, or run offline. The correct reading of Airtable here is §5's:
bind it as `host.airtable` for the ticket and keep the process in
YAML.

**Appian.** Expresses the most of it. Seven checkpoints as BPMN user
tasks with real assignment, SLA timers, and escalation — strictly
better than kitsoki's wait-forever `_awaiting_reply`. Durable
instances, typed records, a compliance-grade audit trail. Where it
diverges is on the two properties §7.4 and §7.5 are about: the judge's
inability to write files is a matter of which tools were attached to
the agent and what the prompt says, reviewed by a human, rather than a
property of the verb the loader checks; and validating a change means
deploying objects to an environment and running instances, not running
25 YAML fixtures in under a second on a laptop. Add the licensing and
the proprietary object repository and the artifact stops being
something you can hand to a coding agent, bisect, or fork.

The summary across all three: **they win on time-to-first-working and
on operator surface; kitsoki wins on the second edit and every edit
after it.** Note which half of that is architectural. The head start
is inventory — connectors already written, a UI already built, and it
is a real advantage measured in quarters of work. The second half is
not: it falls out of where each system decided the process lives, and
no amount of shipping closes it from their side. If a workflow is
going to be written once and watched by a human forever, these
platforms are the right answer and kitsoki is overhead. If it is going to be changed repeatedly, reviewed, forked
per team, run in CI, and given progressively more LLM autonomy, then
"the process is a diffable artifact whose LLM blast radius is
loader-checked and whose full state graph tests in milliseconds" is
worth more than the head start.

### 7.7 The composite picture

| Mechanism | CALM | DF CX | LangGraph | n8n | Airtable | Appian | Kitsoki |
|---|---|---|---|---|---|---|---|
| Recognizer/manager split | ✓ | ✓ | partial | agent node | none | agent + tools | ✓ |
| Event-sourced replay | ✓ | partial | ✓ | execution log | revision history | instance history | ✓ |
| Typed slot validation w/ retry | ✓ | ✓ | DIY | DIY | field types | ✓ | ✓ |
| Conversation-driven dev | ✓ mature | ✓ | DIY | n/a | n/a | n/a | embryonic |
| Per-state intent allowlist | flow-scoped | page-scoped | DIY | none | none | none | ✓ |
| **`emit_intent` synthesised turn** | DIY action | DIY webhook | DIY | DIY | DIY | DIY | ✓ declarative, depth-capped |
| **Declarative cycle budgets** | DIY | DIY | DIY | DIY | DIY | DIY | ✓ phase template |
| **Sub-story imports w/ capability rebinding** | flow link | none | subgraph (no rebind) | sub-workflow (no rebind) | none | sub-process (no rebind) | ✓ `host_bindings` |
| **World isolation + projection per import** | none | none | partial | ✓ in/out payload | none | ✓ process params | ✓ `world_in:` / per-exit `set:` |
| **`emit_intent` resolution across import depth** | n/a | n/a | n/a | n/a | n/a | n/a | ✓ `IntentAliases` walk |
| **Agent-verb blast-radius taxonomy** | one action type | webhook | one node | one agent node | field-scoped agent | agent + tools | ✓ ask / decide / task / extract / converse / codeact |
| **Sandbox-enforced read-only LLM call** | DIY | none | DIY | none | column-scoped write | none | ✓ verb-level |
| **Semantic routing tiers before LLM** | NLU adapter | route matcher | DIY | none | none | none | ✓ four tiers |
| **Multi-surface transport (TUI / MCP / Jira / file)** | channels DIY | CX channels | LangServe | webhook / chat | web + mobile | portals / sites | ✓ first-class |
| **Meta-mode read-only sidebar agent** | none | none | none | none | none | none | ✓ |
| **Background jobs + mid-flight clarification** | none | none | `interrupt()` | `Wait` node | none | ✓ tasks + timers | ✓ `host.RequestClarification` |
| **Process is a diffable text artifact in git** | ✓ | export blob | ✓ Python | JSON + canvas coords | none | proprietary objects | ✓ YAML |
| **Runs locally against a fixture, no service** | ✓ | none | ✓ | ✓ self-host | none | none | ✓ |
| **Flow fixtures = state-graph tests (ms)** | minutes | service-level | DIY | manual re-run | none | env deploy | ✓ |
| **Agent decisions cassette-able (episode-level)** | stubbed | none | DIY | none | none | none | ✓ `host_cassette` |
| **Agent I/O cassette-able (exchange-level)** | DIY | none | DIY | pinned data | none | none | ✓ + `record_mode` |
| **Hermetic repo/workspace fixtures** | n/a | n/a | DIY | none | none | none | ✓ capsules |
| **No automated gate can reach a real model** | convention | n/a | convention | convention | n/a | convention | ✓ policy + fake executor |
| **Agent-written code: capability allowlist, not container** | n/a | n/a | sandbox opt-in | vm-sandboxed `Code` node (author-written) | n/a | RPA / tools | ✓ Starlark, loader-checked |
| **One interpreter for author glue, agent code, frozen artifact** | none | none | none | none | none | none | ✓ promotion ratchet |
Inventory rows — real gaps today, none of them architectural, all of
them either buildable or bindable (§5 Steal):

| Inventory gap | CALM | DF CX | LangGraph | n8n | Airtable | Appian | Kitsoki |
|---|---|---|---|---|---|---|---|
| *Prebuilt integration catalog* | few | few | tool libs | ✓ 400+ | ✓ + sync | ✓ enterprise | ✗ — or bind n8n |
| *Non-technical operator UI (grid / form / inbox)* | none | ✓ | none | partial | ✓✓ | ✓✓ | ✗ — or bind Airtable |
| *Human-task SLA timers + escalation* | none | none | none | partial | none | ✓✓ | ✗ — BPMN timer events, §2 |
| *Process mining over instances* | none | partial | none | none | none | ✓✓ | ✗ — event log has the data |

#### Which of these are actually differentiators

A capability another platform ships is only a differentiator *for them*
if kitsoki would have to change its architecture to get it. Most of the
time it wouldn't — so the useful test is:

> Can the other side add this without abandoning their central bet?

Appian's SLA timers and escalation are the clearest case. They are a
better answer than kitsoki's wait-forever `_awaiting_reply`, and
they are also just timer events on a state — BPMN vocabulary §2
already draws from, expressible in the existing effect alphabet, no
redesign required. Same for the connector catalog and the operator
grid: those are inventory and surface area, bought with effort or
borrowed across the host boundary. They belong in `Steal`, not in a
moat calculation on the other side of the table.

Run the test the other direction and it stops being symmetric. For n8n
to give you a diffable text artifact, it has to stop being a canvas.
For Airtable to run against a local fixture, it has to stop being a
hosted base. For Appian to make a judge's read-only-ness a
loader-checked property rather than a prompt-plus-attached-tools
configuration, it has to introduce a typed verb taxonomy underneath
Agent Studio and re-authorise every existing agent against it. Those
are not backlog items; they are the thing each product *is*.

That asymmetry has a concrete form in kitsoki's design, which is why
§5 lands where it does: **capabilities flow one way across the host
boundary.** Kitsoki can consume n8n's 400 connectors through a single
webhook binding, Airtable's grid as a `ticket` binding, and Appian's
task inbox as an `inbox` binding — and lose nothing, because the
process semantics stay on this side. There is no corresponding
binding that lets an n8n canvas import a loader-checked blast radius
or a millisecond state-graph suite. The bold rows above are the ones
that survive that test in both directions.

The rest of the pattern: the columns to the left match kitsoki on the
*conversational* core. The rows in **bold** are where kitsoki is
either uniquely declarative, uniquely composable, or uniquely fast
to author against. They are not separate features; they are
consequences of the same architectural commitment per
[memory: kitsoki moat is architecture] — *separate interpretive
decisions from deterministic execution, with pluggable operators per
decision and every decision recorded.* CALM, CX, and LangGraph each
make that commitment at the top level but not all the way down; n8n,
Airtable, and Appian make the opposite one — the operator drives, and
the LLM is a node with credentials.

### 7.8 What "worth the effort to continue" means

Kitsoki should not be sold as "deterministic conversational AI" —
that battle is over and there are three winners already. The
defensible pitch is narrower and more specific:

1. **An authoring substrate for state-machine stories that compose
   like libraries** — with capability rebinding, world isolation,
   and intent-resolution across import depth. Per
   [memory: kitsoki audience breadth] this is for any team that
   wants to share a "bugfix pipeline" or "incident triage" story
   the way they share a Python package.
2. **A verb taxonomy that makes LLM blast radius a loader-checked
   property** — not a code-review property. The judge cannot write
   files; the proposer cannot mutate state; the runtime enforces it.
3. **A semantic-routing stack that pushes most turns off the LLM
   entirely** — four deterministic tiers before LLM fallback
   ([`semantic-routing.md` §1](semantic-routing.md)), with a
   documented promotion path from "LLM decision in trace" → "synonym"
   → "slot template" → "deterministic edge." Deliberately manual, not
   auto-promoting. The direction of travel is the differentiator: a
   kitsoki story gets *cheaper and more deterministic* the longer it
   runs, where every other system on this list has a fixed per-turn
   LLM cost that only a hand-rolled cache changes.
4. **A determinism surface with a named seam per source of
   nondeterminism** — state graph, agent decision, agent I/O,
   repository, executor — so the state graph tests in milliseconds,
   an agent bug files as a replayable test, and no automated gate can
   reach a real model (§7.5). This is the least glamorous item and
   probably the most decisive one: it is what makes points 1–3
   editable by an agent without a human watching every turn.
5. **One sandboxed interpreter shared by author glue, agent-written
   code, and the frozen artifact a trajectory promotes into** — where
   the guarantee is not "the container held" but "the loader can tell
   you what this call was ever able to touch" (§7.5, §4c).
6. **A process artifact an agent can safely edit** — text in git,
   diffable, reviewable in a PR, bisectable, forkable per team. This
   is the one §5 makes urgent: the moment the thing changing your
   workflow is itself an LLM, a canvas of node UUIDs and a hosted base
   configuration stop being viable substrates, and everything in
   points 2, 4, and 5 becomes the safety net rather than a nicety.

These are the things that make the bugfix story possible in 408 lines
of YAML + 25 flow fixtures, importable into a parent story without
modification, with three judge modes and seven retry-bounded
checkpoints. None of CALM, CX, or LangGraph can produce that
artifact at that cost today. That gap is what kitsoki is for.

And the inverse, stated plainly so nobody has to discover it the
expensive way: if the requirement is a connector to a SaaS nobody has
integrated yet, a grid a non-technical owner maintains, a task inbox
with SLAs, or a workflow that will be written once and never seriously
changed, then n8n, Airtable, or Appian is the right answer and kitsoki
is overhead. The productive question is almost never "which one" —
it's which layer each owns. They own the integrations, the records,
and the humans. Kitsoki owns the process semantics and the LLM's
blast radius, and calls the rest through `host_bindings:`.

---

## Sources

- Inform 7, *Writing With Inform* §17.4 Standard tokens of grammar.
  https://ganelson.github.io/inform-website/book/WI_17_4.html
- Inform 7, *Writing With Inform* §14.3 More on adapting verbs.
  https://ganelson.github.io/inform-website/book/WI_14_3.html
- TADS 3 — Creating Verbs. http://www.tads.org/howto/t3verb.htm
- Ink — Writing With Ink.
  https://github.com/inkle/ink/blob/master/Documentation/WritingWithInk.md
- Yarn Spinner — Nodes and Lines.
  https://docs.yarnspinner.dev/write-yarn-scripts/scripting-fundamentals/lines-nodes-and-options
- Yarn Spinner — Commands.
  https://yarnspinner.dev/docs/write-yarn-scripts/scripting-fundamentals/commands
- Twine/Harlowe 3.3.8 manual. https://twine2.neocities.org/
- ChoiceScript — Introduction.
  https://www.choiceofgames.com/make-your-own-games/choicescript-intro/
- Stately (XState) — Guards. https://stately.ai/docs/guards
- Stately (XState) — Parallel states. https://stately.ai/docs/parallel-states
- W3C — State Chart XML (SCXML) Recommendation. https://www.w3.org/TR/scxml/
- Temporal — Workflow Definition. https://docs.temporal.io/workflow-definition
- LangChain — LangGraph Persistence.
  https://docs.langchain.com/oss/python/langgraph/persistence
- Camunda — BPMN 2.0 Symbols Reference. https://camunda.com/bpmn/reference/
- Rasa — Forms. https://legacy-docs-oss.rasa.com/docs/rasa/forms/
- Rasa — CALM (Conversational AI with Language Models).
  https://rasa.com/docs/learn/concepts/calm/
- Rasa — Commands reference.
  https://rasa.com/docs/reference/primitives/commands/
- Rasa — Flows reference.
  https://rasa.com/docs/reference/primitives/flows/
- Google Cloud — Dialogflow CX Pages.
  https://cloud.google.com/dialogflow/cx/docs/concept/page
- Google Cloud — Dialogflow CX Generators.
  https://cloud.google.com/dialogflow/cx/docs/concept/generators
- Google Cloud — Dialogflow CX Generative Fallback.
  https://cloud.google.com/dialogflow/cx/docs/concept/generative-fallback
- Google Cloud — Dialogflow CX Playbooks.
  https://cloud.google.com/dialogflow/cx/docs/concept/playbook
- n8n — Break workflows into smaller parts (sub-workflows).
  https://docs.n8n.io/build/flow-logic/break-workflows-into-smaller-parts
- n8n — Execute Sub-workflow node.
  https://docs.n8n.io/integrations/builtin/core-nodes/n8n-nodes-base.executeworkflow
- Airtable — Automations overview.
  https://support.airtable.com/docs/getting-started-with-airtable-automations
- Airtable — Using Airtable AI in fields (field agents).
  https://support.airtable.com/docs/using-airtable-ai-in-fields
- Airtable — Using Omni AI in Airtable.
  https://support.airtable.com/docs/using-omni-ai-in-airtable
- Appian — Process Modeling with Appian.
  https://docs.appian.com/suite/help/25.4/process_modeling.html
- Appian — Agent Studio.
  https://docs.appian.com/suite/help/25.4/agent-studio.html
- Appian — Create and configure an AI agent.
  https://docs.appian.com/suite/help/25.4/create-and-configure-ai-agent.html
- LangGraph — Low-level concepts.
  https://langchain-ai.github.io/langgraph/concepts/low_level/
- LangGraph — Persistence.
  https://langchain-ai.github.io/langgraph/concepts/persistence/
- LangGraph — Human-in-the-loop.
  https://langchain-ai.github.io/langgraph/concepts/human_in_the_loop/
- Microsoft — AdaptiveDialog class reference.
  https://learn.microsoft.com/en-us/javascript/api/botbuilder-dialogs-adaptive/adaptivedialog?view=botbuilder-ts-latest
- Model Context Protocol — Schema Reference.
  https://modelcontextprotocol.io/specification/draft/schema
- mirascope — LLM Validation With Retries.
  https://mirascope.com/tutorials/more_advanced/llm_validation_with_retries/
- IFWiki — Cloak of Darkness.
  https://www.ifwiki.org/index.php/Cloak_of_Darkness
- TADS Guide — Cloak of Darkness specification.
  https://users.ox.ac.uk/~manc0049/TADSGuide/cloak.htm
- ESR — Open Adventure resource page.
  http://www.catb.org/~esr/open-adventure/
- Charm — Bubble Tea framework. https://github.com/charmbracelet/bubbletea
