# The Story Paradigm: an AI-Native Programming Model

*A draft framing of the kitsoki story concept as a programming paradigm —
what it takes from functional programming, what it takes from object
orientation, what only a graph can provide, and why the combination is
specifically suited to a world where AI both writes and runs the code.*

---

## 1. The problem being solved

Every mainstream paradigm eventually produces the same failure at scale:
**comprehension collapses before the code does.** Control flow disperses
across call stacks, mutable state hides in objects and globals, and the
only complete description of what the program can do is the program
itself. Humans cope with this badly; LLMs cope with it worse, because an
LLM's working memory is a context window — it can only reason reliably
about what fits in front of it.

An AI-native paradigm therefore has one governing constraint:

> **Every unit of the program must be small enough to be completely
> understood, tested, and mocked in isolation — and the units must be
> connected by structure that is itself data, not emergent behavior.**

No single existing paradigm delivers this. Three of them each deliver a
piece.

## 2. Three inheritances

### From functional programming: reasoning

FP's gift is that a pure function is a *closed* object of reasoning:
same inputs, same outputs, no elsewhere to look. The paradigm keeps the
whole FP discipline, but applies it at the level of *transitions* rather
than expressions:

- **The core is pure.** A transition is a function
  `(world, intent, slots) → (world′, view)`. No clocks, no randomness,
  no I/O inside the machine. Same world + same intent → same result,
  always — which is what makes replay, flow tests, and deterministic CI
  possible at all.
- **Mutable state is confined, not banned.** There is exactly one state
  object — the **world** — and it is typed, scoped, and only ever
  written through declared binds. Nothing mutates ambiently.
- **Effects are quarantined at a declared boundary.** Interaction with
  the environment (shell, files, LLMs, trackers) happens only through
  declared `invoke:` effects that run outside the machine and return a
  *typed* result the author explicitly `bind:`s back into the world.
  This is the IO-monad move: impurity doesn't disappear, it gets a type,
  a name, and a recorded transcript.
- **All outcomes are accounted for.** An effect's result is a sum type
  in spirit: the success branch binds, the failure branch routes through
  a declared `on_error`, and guards are total predicates over the world.
  There is no "the exception propagated somewhere" — every reachable
  state of the computation is a state the author named. This is the
  monadic sensibility: `Result`/`Maybe` thinking enforced structurally
  rather than by library convention.

What FP alone lacks: a pile of pure functions has no *shape*. Composition
is invisible at rest; you cannot look at a functional codebase and see a
map of what the program does.

### From object orientation: comprehension

OOP's gift is cognitive, not mathematical: people naturally think in
*things* that have properties and behaviors. The paradigm keeps the
packaging and discards the pathology:

- **The node is a typed object.** A room/state packages everything about
  one situation into one reviewable unit: the data it can see (its slice
  of the world schema), the verbs available there (its `intents:` with
  typed `slots:`), its behavior (`on:` transitions, `on_enter:` chains),
  and its presentation (`view:`). It is an object in the honest sense —
  a coherent bundle of type + functions — and it is *spatial*: "where
  you are" is a concept every reader already has an intuition for.
- **Encapsulation without hidden mutation.** Classic OOP encapsulation
  hides state *and lets the object mutate it privately*, which is
  exactly where reasoning dies. Here the node encapsulates *vocabulary
  and behavior* but state changes remain pure, declared transitions over
  the shared typed world. You get the mental model of objects without
  the aliasing-and-mutation swamp.
- **Interfaces as finite alphabets.** An object's method set is its
  contract; a room's intent set is the same idea made strict — a
  *finite, declared alphabet* of everything that can possibly happen
  there. If the room doesn't declare a verb, the verb does not exist.

What OOP alone lacks: the relationships between objects are implicit —
pointers, injections, event buses. The structure of the program is
smeared across call sites, which is precisely the spaghetti problem.

### From graphs: structure

The graph is what neither FP nor OOP provides — **relationships as
first-class, queryable data**:

- **Edges are declared, not emergent.** A transition's `target:` is an
  edge in a directed cyclic graph. `next:` enumerates every legal
  destination from a phase. Control flow is not a property you discover
  by tracing execution; it is a data structure you can render, diff, and
  lint before the program ever runs.
- **Structural questions get structural answers.** Reachability ("can
  the user ever get from review to ship without passing the gate?"),
  coverage ("which rooms has no flow test ever visited?"), impact
  ("what downstream rooms read this world field?") are graph queries,
  not archaeology.
- **Cycles are governed, not feared.** Feedback arcs, back-jumps, and
  retries exist — but as declared edges with guards and cycle budgets,
  so even the loops are enumerable and bounded.

What a bare graph/workflow engine alone lacks: its nodes are usually
untyped blobs of imperative script. The graph gives shape but not
reasoning; you need the FP discipline *inside* each node and the OOP
packaging *of* each node.

## 3. The synthesis, stated as laws

The paradigm is the intersection of the three inheritances, enforced
mechanically:

1. **Nodes are typed objects.** Each node packages a world slice, a
   finite intent alphabet with typed slots, guarded transitions, and a
   view. A node fits in a context window — human or machine.
2. **Edges are declared and finite.** All control flow is graph
   topology. There is no way to reach a node except along an edge the
   author wrote down.
3. **Transitions are pure.** `(world, intent, slots) → (world′, view)`,
   deterministic and replayable. The machine contains no I/O.
4. **Effects are typed border-crossings.** The only path to the
   environment is a declared effect; its result is typed, every branch
   (including failure) is bound to a named destination, and its inputs
   and outputs are recorded so the turn replays exactly.
5. **State is singular, typed, and scoped.** One world, written only by
   binds. No hidden mutable state anywhere in the system.
6. **The interpreter translates; it never decides.** Free text enters,
   but an LLM (behind cheaper deterministic routing tiers) may only map
   it onto one member of the current node's declared intent alphabet.
   It cannot invent an action, write the world, or choose a path that
   isn't an edge.

Law 6 is the AI-era addition that no parent paradigm has: it is the
control inversion. In agent frameworks the LLM holds the plan and the
runtime is its tool belt; here the runtime holds the plan and the LLM is
a *component* — a fuzzy parser at the input boundary and a scoped worker
inside declared effects, both fenced by types.

## 4. Why this is "AI-native"

**AI as reader.** Each node is a self-contained comprehension unit: to
reason about a room you need the room, its world slice, and its edges —
all of which fit in a context window. The graph is the table of
contents; an AI can navigate structure instead of grepping for call
sites.

**AI as author.** Because nodes are small, typed, and isolated, an AI
can write or modify one node with bounded blast radius, and the graph
lint/render/reachability tools verify the structural consequences
mechanically — the review burden is a diff of declared topology, not a
hunt through imperative code.

**AI as component.** The paradigm assumes LLM calls are *inside* the
program — but as typed, recorded, mockable effects. Cassettes and flow
tests replace the live model in every automated gate; determinism
everywhere else means tests never need an LLM to exercise the machine.

**AI as suspect.** Traces, receipts, and replay exist because model
claims are not evidence. The paradigm's answer to "did the agent
actually do it?" is structural: the recorded turn either replays or it
doesn't.

The deeper point: the properties that make code tractable for an LLM —
small closed units, explicit typed interfaces, declared structure, total
accounting of outcomes — are the same properties that made code
tractable for humans all along. AI didn't create the requirement; it
removed our tolerance for violating it, because an LLM has no ability to
compensate with years of accumulated tribal knowledge about where the
bodies are buried.

## 5. What the paradigm rejects

- **Ambient mutation** (OOP's private mutable fields, shared globals):
  replaced by the single typed world and declared binds.
- **Emergent control flow** (call stacks, event buses, exceptions as
  flow): replaced by declared edges; even error paths are edges.
- **Unbounded interpretation** (the LLM decides which tool, which order,
  whether to ask): replaced by the finite intent alphabet and the
  translate-don't-decide boundary.
- **The monolith as unit of understanding**: nothing in the system
  requires whole-program comprehension; every question is answerable at
  the node, the edge, or the graph-query level.

## 6. Naming

Working candidates, in rough order of preference:

- **Story-graph programming** — leads with the kitsoki noun; the
  "story" is the program, honest about the narrative/spatial framing.
- **Room-oriented programming** — the OOP pun is doing real work: the
  object is a *place*, and places have finite doors.
- **Graph-functional-object (GFO)** — descriptive of the synthesis,
  dry, safest for an academic-register pitch.

## 7. Caveats — and how to resolve them without leaving the paradigm

The mapping from paradigm to the existing story runtime is honest but
not perfect. Two claims above are aspirational in degree, and both can
be closed with the paradigm's own tools — declared structure plus
load-time verification — rather than by importing someone else's type
system.

### 7.1 Monadic totality is enforced by convention, not by types

**The gap.** "All outcomes are accounted for" is delivered structurally:
`bind:` names the success path, `on_error` names the failure path, and
the recursion cap bounds the error loop. But nothing *proves*
exhaustiveness. An effect's result is a typed value, not a declared sum
type; if a host call can produce three meaningfully different outcomes
and the author only routes two, the loader does not object. The monad is
present in sensibility, absent in checking.

**Resolution inside the paradigm.** The paradigm's move is always the
same: make the implicit thing declared, then lint the declaration.

- **Declared outcome variants on effect contracts.** Let a host verb's
  contract enumerate its outcome variants (`ok`, `not_found`,
  `conflict`, `timeout`, …) the way a room enumerates intents. An
  `invoke:` is then complete only when every variant is bound to a
  destination — success binds, each failure variant routes to a named
  edge or explicitly collapses into a shared handler. This is sum-type
  exhaustiveness, but as a load-time graph lint rather than a compiler:
  the same mechanism that already refuses an undeclared verb refuses an
  unrouted outcome.
- **Typed binds against the world schema.** The world already has a
  schema; the missing check is that every `bind:` target and every
  guard expression type-checks against it at load time, so "the effect
  returned a shape the world can't hold" is an authoring error, not a
  runtime surprise. This is the FP discipline applied at the seam where
  it is currently softest.
- **Totality lint on guard partitions.** Where several guarded
  transitions fan out from one intent, a lint can require the guard set
  to be provably exhaustive (a final unguarded arm, or an explicit
  `else:` edge). Unreachable and uncovered world regions become
  structural findings, like unreachable rooms already are.

None of this needs a general type system — the alphabets are finite and
declared, so exhaustiveness is a graph property, checkable by the
loader.

### 7.2 The OOP inheritance/polymorphism gap

**The gap.** Rooms deliver the *packaging* virtue of objects but none of
the classic reuse machinery: no inheritance, no subtype polymorphism, no
instantiable classes. Today's reuse tools — phase templates, `imports:`
with world projection and host rebinding, prompt overlays — are
composition mechanisms, and the omission of implementation inheritance
is deliberate (it is where hidden coupling comes from). But the gap is
real when many rooms should *behave alike* and nothing enforces that
they do.

**Resolution inside the paradigm.** Take the half of polymorphism that
was ever worth having — *contracts* — and leave implementation
inheritance buried:

- **Room interfaces (intent-alphabet contracts).** Declare a named
  contract — a set of intents with slot signatures, and optionally
  required world fields — that a room can claim to implement. The
  loader verifies the claim. "Every phase room in this pipeline
  implements `Reviewable`" becomes a checkable statement, and a
  back-jump or import can target *any implementor* rather than a
  hard-coded room name. This is structural subtyping over finite
  alphabets — polymorphism as a graph-lint, no vtables required.
- **Parameterized rooms (generics by template).** Phase templates
  already instantiate one room shape many times; generalize the same
  mechanism so a room template takes declared parameters (world slice,
  host bindings, exit edges) and the loader stamps out verified
  instances. Reuse stays visible and flat — every instance is a real
  node in the rendered graph — instead of hiding in an inheritance
  chain.
- **Composition remains the default.** Where classic OOP would subclass,
  the paradigm's answer stays `imports:` + projection + rebinding: reuse
  by *wiring*, which the graph can display, rather than by *ancestry*,
  which it cannot.

### 7.3 Structural queries are partial

**The gap.** The paradigm promises that reachability, coverage, and
impact are graph queries. Rendering and reachability exist; coverage
mining exists for stories; but "which rooms read this world field" and
edge-level impact analysis are not yet uniform, first-class queries.

**Resolution inside the paradigm.** The declarations already contain the
answer — guards and binds name the world fields they touch. Extracting
a field-level read/write index at load time turns data-flow impact into
the same kind of query as reachability, and closes the loop with 7.1's
typed binds: one schema, one index, every structural question answered
from declarations rather than execution.

The common thread: each caveat is a place where a property currently
holds *by authoring discipline*, and the fix in every case is the
paradigm's signature move — promote the discipline into a declaration,
then make the loader refuse the program that violates it.

## 8. Lineage (one paragraph)

None of the pieces are new — statecharts contributed hierarchical
states and pure guards, Temporal contributed replay-enforced
determinism, interactive fiction contributed grammar-first verbs and
the view/mechanics split, FP contributed effect quarantine and
totality, OOP contributed the packaging. The claim to novelty is the
*combination under the AI-native constraint*: the graph is the program,
the nodes are typed objects, the transitions are pure, the effects are
monadic border-crossings, and the LLM is a fenced component instead of
the planner. `docs/architecture/prior-art.md` records the detailed
steal/reject ledger; `docs/architecture/concept.md` is the control-
inversion thesis this paradigm framing generalizes.
