# Epic: Stories as a reusable application platform

**Status:** In progress v4. The cross-surface application foundation is
implemented: `application/v1` loading and validation, program-graph projection,
`application-frame/v1`, the handler/event registry and receipt envelopes, the
default Vue renderer, semantic inspection, TUI projection, and initial CLI,
MCP, JSON-RPC, web, and VS Code-webview adapters. The remaining work is deeper
story-contract analysis, functional Starlark/event execution, the managed
Vite/package lifecycle, the wizard framework, native surface integration, and
the external POG conformance migration. Reconciled with
[`docs/stories/domain-model.md`](../stories/domain-model.md) and
[`docs/architecture/story-programming-paradigm.md`](../architecture/story-programming-paradigm.md);
the remaining focused child proposals should be cut from the residual slices.
**Kind:**   epic
**Slices:** 8 (0/8 shipped)

## Why

Kitsoki stories already own workflow state, intent routing, typed views,
deterministic effects, Starlark glue, host interfaces, composition, and
extension. They can be driven through the TUI, web UI, VS Code extension, CLI,
MCP, and JSON-RPC. But those surfaces still behave mostly as Kitsoki-owned
clients around a story: a story cannot define a complete application shell,
declare reusable pages and cards, bind custom Vue components to story actions,
or export one typed handler through CLI, MCP, and JSON-RPC without
surface-specific glue.

That gap prevents a product from being implemented *as stories*. A product such
as POG should be able to keep workflow, application UI, and public handlers in
composable story packages while Kitsoki supplies the runtime, development
server, hot module replacement, transport adapters, tracing, and host
integration. The product repository should need only a thin deployment and
branding shell, not a parallel router, API layer, and frontend workflow model.

## What changes

A story may declare an optional **application contract** beside its state
machine. The contract has two separable outputs:

1. a presentation-free `application-frame/v1` document describing semantically
   named navigation, regions, cards, typed content, forms, actions, validation,
   and current workflow state; and
2. an optional presentation bundle that maps that document to a supplied UI
   framework or story-owned Vue components.

Kitsoki compiles the same contract onto every supported surface:

- **web** renders the default card/wizard framework or a story-supplied Vue
  presentation, served and hot-reloaded by Kitsoki/Vite;
- **VS Code** reuses the web presentation in webviews by default and may map
  declared actions to native commands, trees, quick picks, and status items;
- **TUI** projects cards, forms, and actions onto typed view elements and native
  terminal controls;
- **CLI, MCP, and JSON-RPC** expose the story's named typed handlers through
  generated adapters backed by one handler registry; and
- **events** enter through the same registry, so timers, job completions,
  webhooks, inbox updates, and fleet signals can refresh or advance an
  application without fabricating a user turn.

Story imports, overrides, interfaces, component packages, and future kits
compose application fragments by the same ownership rules as workflow
fragments. A base `@kitsoki/wizard-ui` component package can therefore provide a
headless frame contract, the default card presentation, or both; a product story
can import it, replace selected cards/components, and retain deterministic
fallbacks for non-web surfaces.

## Impact

- **Spans:** runtime, story, tui, tracing, web/VS Code tooling.
- **Net surface:** a finite-contract/program-graph foundation; an application
  manifest and merge model; canonical frame, semantic-node, feedback-anchor, and
  action envelopes; typed handler and event registry; Vue component loader; a
  Kitsoki-owned Vite dev/build lifecycle; versioned component packages; adapters
  for web, VS Code, TUI, CLI, MCP, and JSON-RPC; and a cross-surface conformance
  harness.
- **Compatibility:** stories without `application:` continue to use the current
  room-oriented UI and transport behavior. Existing typed views become the
  default content projection inside an application frame.
- **Docs on ship:** `docs/stories/applications.md`,
  `docs/architecture/application-runtime.md`, updates to
  `docs/architecture/transports.md`, and surface-specific guidance under
  `docs/tui/`.

## Existing substrate

This epic generalizes shipped mechanisms instead of introducing a second story
engine.

| Existing piece | Role in the platform |
|---|---|
| Typed `app.View` and `ViewElement` (`internal/app/view_element.go`) | Presentation-free content and form/action fallback vocabulary |
| Interactive web runtime (`docs/tui/web-ui.md`, `internal/runstatus/server/`) | Live session ownership, JSON-RPC writes, SSE updates, and the current Vue host |
| Standalone `chat`, `trace`, and `graph` Vue surfaces (`tools/runstatus/src/surfaces/`) | Proof that one SPA can mount reusable projections in browser and VS Code |
| Framework-neutral embed plugins (`tools/runstatus/src/lib/embedPlugin.ts`) | Precedent for dependency-injected, story-selected presentation plugins |
| Semantic sidecars and `AnnotationAnchor.semantic_element` (`internal/host/semantic_sidecar.go`, `internal/host/annotation_anchor.go`) | Existing cross-media wire shape for a stable ref, kind, label, description, provenance data, and optional surface geometry |
| Reviewed bug reports and the generic feedback SDK proposal (`tools/runstatus/src/stores/bugReport.ts`, [`generic-feedback-sdk-slidey-reports.md`](generic-feedback-sdk-slidey-reports.md)) | Capture/review/sink path that application semantic anchors must enrich rather than replace |
| VS Code bridge transport (`tools/runstatus/src/transport/`) | Reuse of the web application over a native extension relay |
| Story imports, overrides, exits, and host interfaces (`docs/stories/imports.md`) | Composition, extension, private namespaces, and swappable capabilities |
| `host.starlark.run` (`docs/architecture/hosts.md`) | Deterministic implementation for typed application handlers |
| `kitsoki turn`, `kitsoki serve`, Studio MCP, and runstatus JSON-RPC | Existing adapters to consolidate behind the handler registry |
| Canonical typed-view work (`view-rendering-readability.md`) | Required source of truth for width-free, cross-surface view content |
| Kits (`kits.md`) | Future distribution/versioning boundary for reusable application frameworks |

## Paradigm alignment and gap closure

The application platform must consume the story paradigm rather than put a
frontend beside it. The two source documents name gaps that are easy to miss if
this epic starts at Vue. This table makes their ownership explicit.

| Existing idea or gap | Platform disposition |
|---|---|
| Declared effect outcome variants; typed binds; exhaustive guard partitions | Slice 1 makes these loader-checked contracts. Exported handlers reuse the same variants and may not collapse an unhandled result into a generic UI error. |
| Room interfaces and parameterized rooms | Slice 1 adds structural room contracts and visible template instantiation. Pages/actions may target a room interface; the loader resolves the implementor and proves its intent/slot/world contract. |
| Field-level read/write and impact queries | Slice 1 emits one program graph covering rooms, intents, transitions, world reads/writes, effects, handlers, events, pages, components, and actions. POG's ownership report is a query over this graph. |
| Semantic UI observability and actionable feedback | Slices 1 and 2 make stable refs, names, descriptions, roles, relationships, state, and source provenance part of the application graph/frame. Slices 4 and 6 project those nodes into the existing semantic annotation and feedback contracts on every surface. |
| Event binding without a user turn | Slice 3 owns typed session event subscriptions and their background-vs-interrupt semantics. Events dispatch the same intent/handler contracts as operator actions. |
| Per-invocation routing mode | Slice 3 carries routing policy on every action, handler, event, and adapter call; slice 6 exposes it consistently. Deterministic conformance fails on an attempted LLM fall-through. |
| Reusable component libraries | Slice 7 packages agents, toolboxes, intents, providers, host interfaces, schemas, application components, tokens, and room templates à la carte without importing a room graph. |
| Composition versioning | Slice 7 coordinates with [`kits.md`](kits.md): semver constraints, one lockfile, source digests, and conflict detection apply equally to stories, kits, and component packages. |
| Run-level cost/intelligence governor | Slice 3 admits budget policy and emits budget/degradation state; frames and adapters surface it. The governor remains runtime-owned rather than Vue-owned. |
| Dynamic fan-out | The handler/event result model carries durable child-run collections and join state; the application renders them generically. The underlying supervisor/worker primitive remains a runtime dependency, not UI code. |
| Compensation and idempotency | Slice 3 requires idempotency keys and declared compensation for retryable `external` handlers; actions cannot bypass those policies. |
| Learning loop | Slice 3 records route resolution/feedback and slice 6 transports it, so validated phrasing promotion can improve every surface without changing presentation code. |
| Deeper statechart analysis | Slice 1 unifies reachability, exit satisfiability, outcome/guard totality, deadlock checks, room-interface conformance, and UI/action coverage as program-graph lints. |

These are not optional aspirations hidden behind the POG slice. The full POG
gate cannot pass while a required row is unimplemented. Where the underlying
runtime capability belongs to a sibling proposal, this epic owns the
integration contract and treats that proposal as a named prerequisite instead
of recreating the capability inside the frontend.

## Application contract

The author surface below is illustrative. Slice 1 owns the final schema and
must align it with the existing `App` loader rather than parsing an unrelated
manifest.

```yaml
app:
  id: pog
  version: 1.0.0

application:
  schema: application/v1
  name: "POG"
  description: "Shape, govern, and deliver product changes."
  semantic_ref: pog.application
  feedback:
    context:
      workflow_state:
        source: frame.workflow.state
        sensitivity: internal
        policy: include
  shell:
    contract: "@kitsoki/wizard-ui/v1"
    presentation: default       # none | default | custom
    entry: dashboard

  navigation:
    - id: dashboard
      name: Dashboard
      description: "Open the portfolio overview and current delivery status."
      semantic_ref: pog.nav.dashboard
      page: dashboard
    - id: changes
      name: Changes
      description: "Browse and inspect proposed or active product changes."
      semantic_ref: pog.nav.changes
      page: changes

  pages:
    dashboard:
      name: "Portfolio"
      description: "Summarize portfolio health and the work requiring attention."
      semantic_ref: pog.page.dashboard
      regions:
        main:
          name: "Portfolio overview"
          description: "The primary dashboard content and available actions."
          semantic_ref: pog.region.dashboard-main
          items:
            - card:
                id: active-changes
                name: "Active changes"
                description: "List changes that are currently being shaped or delivered."
                semantic_ref: pog.card.active-changes
                component: pog.change-list
                props:
                  source: "{{ world.change_summary }}"
                actions: [pog.change.open]

  components:
    pog.change-list:
      name: "Change list"
      description: "Present change summaries and allow an operator to open one."
      semantic_ref: pog.component.change-list
      web:
        module: ui/web/ChangeList.vue
        export: default
      props_schema: schemas/change-list-props.json
      fallback:
        element: list

  actions:
    pog.change.open:
      name: "Open change"
      description: "Open the selected change for inspection and action."
      semantic_ref: pog.action.change-open
      handler: pog.change.open

  surfaces:
    web:
      presentation: custom
      entry: ui/web/main.ts
    vscode:
      reuse: web
      native:
        commands: [pog.change.open]
    tui:
      projection: cards
```

The loader compiles this to a canonical frame:

```json
{
  "schema": "application-frame/v1",
  "application_id": "pog",
  "session_id": "01...",
  "revision": 12,
  "page": "dashboard",
  "workflow": {"state": "portfolio.ready", "allowed_intents": ["open"]},
  "navigation": [],
  "regions": [
    {
      "id": "main",
      "semantic": {
        "ref": "pog.region.dashboard-main",
        "kind": "region",
        "name": "Portfolio overview",
        "description": "The primary dashboard content and available actions."
      },
      "cards": [
        {
          "id": "active-changes",
          "semantic": {
            "ref": "pog.card.active-changes",
            "kind": "card",
            "name": "Active changes",
            "description": "List changes that are currently being shaped or delivered.",
            "source": {"story": "pog", "member": "application.pages.dashboard.regions.main[0]"}
          },
          "body": [{"kind": "component", "component": "pog.change-list", "props": {}}],
          "actions": [{"id": "pog.change.open", "enabled": true}],
          "state": {"visible": true, "selected": false}
        }
      ]
    }
  ],
  "errors": [],
  "capabilities": {"presentation": ["typed-elements", "custom-components"]}
}
```

The frame is the durable semantic boundary. It contains no Vue component
instances, ANSI, HTML, VS Code objects, or transport-specific callbacks.
Presentation renderers consume it; traces and replay record it. Every
`page/card/component/action` node carries its program-graph id and source-story
provenance so impact, coverage, and ownership queries do not depend on parsing
Vue.

### Semantic UI model and feedback anchors

The frame must describe what the application means, not merely where a renderer
put pixels. Every addressable application, navigation item, page, region, card,
component, field, action, status, and artifact declares or inherits a semantic
node with:

- a stable, application-qualified `ref` that is independent of DOM structure,
  terminal coordinates, visible copy, and the node's current parent;
- a concise human-facing `name` and an operator/agent-facing `description` that
  states the node's purpose rather than restating its kind;
- a finite `kind`/role, source-story provenance, and program-graph id;
- declared relationships to its parent, bound data/world fields, actions,
  intents/handlers, artifacts, and navigation destinations; and
- runtime state such as visible, enabled, selected, validation outcome, and
  frame revision, without copying arbitrary world values into the frame.

Names and descriptions are part of the program contract, not presentation
decoration. Web accessible names, VS Code command/tree descriptions, TUI focus
labels, CLI/MCP help, graph inspection, traces, and report summaries all default
from them. A custom renderer may change visible copy, but it may not erase the
underlying semantic identity or description.

The loader rejects duplicate refs, dangling relationships, invalid kinds,
action/handler mismatches, and explicitly addressable nodes with empty names or
descriptions. It warns when a description only repeats the name/kind, when two
sibling controls have indistinguishable names, or when an interactive node has
no accessible-name projection. Legacy typed views receive deterministic
generated metadata marked `generated: true`; strict application conformance,
including POG, rejects generated metadata for product-owned interactive or
reportable nodes.

The program graph is the source of truth and `application-frame/v1` is its
runtime projection. Renderers expose the node through the shipped
`AnnotationAnchor.semantic_element` shape:

```json
{
  "kind": "semantic_element",
  "semantic_element": {
    "plugin": "kitsoki.application",
    "ref": "pog.card.active-changes",
    "semantic_kind": "card",
    "label": "Active changes",
    "description": "List changes that are currently being shaped or delivered.",
    "data": {
      "application_id": "pog",
      "program_node": "application.pages.dashboard.regions.main.active-changes",
      "story": "pog",
      "frame_revision": 12
    }
  }
}
```

Kitsoki also exposes a read-only semantic inspection query over that graph.
Given a ref, developer tools and report builders can retrieve the node,
provenance, current frame state, and bounded incoming/outgoing relationships
without scraping the renderer or invoking product behavior.

`label` is the wire-compatible projection of the story node's `name`. The
optional `bbox`, selector, and visible text plus surface metadata such as a
terminal cell range, VS Code placement, or transport call id are
locators/evidence supplied by an adapter; they are never canonical identity.
Moving a card, changing copy, or reusing the web presentation in VS Code
therefore does not detach existing feedback from its owner.

Each surface makes the current semantic target inspectable and reportable:

| Surface | Semantic target and feedback behavior |
|---|---|
| Web and VS Code webview | Rendered roots carry the stable ref; inspect/report UI resolves it before falling back to DOM role, selector, text, or coordinates. |
| VS Code native | Commands, tree items, quick picks, and status items retain the same ref and attach their contribution id as surface evidence. |
| TUI | Focusable sections, fields, choices, actions, and media expose the ref through renderer metadata; report/annotate commands attach it without requiring a bounding box. |
| CLI | Discovery/help shows the handler/action name and description; a failed or questioned call can report its semantic ref plus receipt id and redacted input digest. |
| MCP and JSON-RPC | Discovery carries the same descriptions, and invocation receipts retain the handler/action ref so client feedback can target the declared capability. |

Feedback context is allowlisted at the semantic node or application level. A
story may contribute safe identifiers, enum state, or redacted summaries with
privacy metadata; raw props, field values, world snapshots, credentials, and
local absolute paths are excluded by default. The reviewed report bundle from
[`generic-feedback-sdk-slidey-reports.md`](generic-feedback-sdk-slidey-reports.md)
includes the semantic anchor, graph relationships, trace/receipt references,
and optional surface evidence. That lets a report answer “what is this, which
story owns it, what action does it drive, and what state was it in?” without
making a screenshot or CSS selector the debugging API.

### Components and actions

- A built-in component is a pinned semantic kind supplied by Kitsoki
  (`prose`, `form`, `list`, `table`, `artifact`, `status`, and the existing
  typed elements). A custom component is a named module plus an input schema.
- A component receives serializable props, frame context, and a dependency
  injected action dispatcher. It cannot call arbitrary story internals.
- Every interaction emits one transport-neutral action envelope:

  ```json
  {
    "action": "pog.change.open",
    "input": {"change_id": "chg-42"},
    "session_id": "01...",
    "frame_revision": 12
  }
  ```

- An action binds to an intent or exported handler. The runtime performs
  schema validation, stale-frame checks, authorization/effect checks,
  routing-policy enforcement, invocation, tracing, and frame regeneration.
- An action's target may be a concrete room/intent or a declared room
  interface. Interface dispatch resolves only among loader-verified
  implementors and records the selected implementor.
- Every custom component required on more than one surface declares a semantic
  fallback. A required surface with no component implementation or fallback is
  a load-time error, not a blank card.
- Story-owned CSS is scoped below the application root. Design tokens and
  theme slots are composable; global CSS and mutation of Kitsoki chrome are not.

## Exported handler and event contract

`operations:` already means the story's autonomy/run policy
([domain model §3.15](../stories/domain-model.md#315-operations-operationpolicy-typesgo918)).
Inbound callable APIs therefore use the distinct term **handler**. Stories may
export named handlers without hand-writing one adapter per transport. Stateful
handlers dispatch an intent against a session; functional handlers invoke a
declared Starlark script or bound host-interface operation.

```yaml
exports:
  handlers:
    pog.change.open:
      name: "Open change"
      description: "Open a change and return its application frame."
      semantic_ref: pog.handler.change-open
      input_schema: schemas/change-open.json
      output_schema: schemas/application-frame.json
      session: required          # none | required | create
      effect: read               # pure | read | write | external
      routing_mode: exact        # exact | synonym | semantic | llm | off
      outcomes: [ok, not_found, forbidden]
      dispatch:
        intent: open_change
        slots_from: input
      expose: [jsonrpc, mcp, cli]

    pog.catalog.search:
      name: "Search catalog"
      description: "Search the project catalog."
      semantic_ref: pog.handler.catalog-search
      input_schema: schemas/catalog-search.json
      output_schema: schemas/catalog-results.json
      session: none
      effect: read
      outcomes: [ok, invalid_query]
      starlark:
        script: scripts/catalog_search.star
        capabilities:
          fs: {read: ["catalog/**"]}
      expose: [jsonrpc, mcp, cli]

events:
  change-updated:
    source: pog.change.updated
    input_schema: schemas/change-updated.json
    session: required
    mode: background             # background | interrupt
    dispatch:
      handler: pog.catalog.search
```

Each declared outcome must map to a typed result or named state/edge; every bind
is checked against the world schema. `write`/`external` handlers additionally
declare idempotency policy, and retryable `external` handlers declare a
compensation handler or explicitly document why compensation is impossible.

One registry owns handler/event discovery, validation, invocation, and receipts.
Adapters are
mechanical:

| Adapter | Generated behavior |
|---|---|
| JSON-RPC | Discover and call the stable handler name with JSON input |
| MCP | Advertise a tool whose input schema and description come from the handler |
| CLI | `kitsoki app call <handler> --input <json-or-@file>` plus discovery/help |
| Web/VS Code/TUI | Dispatch the same handler from an action envelope |

The exact adapter spelling may differ where a protocol restricts names, but
discovery returns the canonical handler id. Every adapter produces the same
validated outcome envelope and a trace receipt keyed by handler id, session,
actor, effect class, routing mode/resolution, budget decision, idempotency key,
input digest, output digest, and transport. No adapter may embed business logic.
The same receipt shape is used when an event invokes the handler.

## Surface projections

| Surface | Default projection | Story-owned extension |
|---|---|---|
| Web | Default responsive card/wizard Vue shell over `application-frame/v1` | Vue components, page layout, scoped theme, or complete presentation entry |
| VS Code webview | The same built web presentation through `BridgeTransport` | Editor-aware component variants and placement metadata |
| VS Code native | Commands generated from actions; optional trees, quick picks, status items | Declarative native contributions bound to handlers, never duplicate handlers |
| TUI | Regions become sections, cards become panels, forms/actions use typed controls | Surface-specific typed-view overrides and key bindings |
| CLI | Handler discovery/call; structured JSON by default | Human-readable template for a handler result |
| MCP | One tool per exposed handler or a bounded generic call tool | Description/schema annotations only |
| JSON-RPC | Canonical discovery/call/session APIs | None; this is the transport-neutral wire contract |

A story may omit presentation for a surface while retaining its semantic
contract. `presentation: none` means “serve the frame and handlers,” not
“surface unsupported.” `surfaces.<name>.required: true` turns missing projection
coverage into a conformance failure. Semantic inspection and feedback attachment
remain available even when the surface uses only the default or headless
projection.

## Web development and build lifecycle

`kitsoki app dev <app.yaml>` owns the complete local loop:

1. load and validate the story plus application contract;
2. start the live Kitsoki session/RPC/SSE backend;
3. synthesize a Vite configuration rooted at the story's declared UI sources;
4. serve the Kitsoki shell and story modules with Vue hot module replacement;
5. watch YAML, Starlark, schemas, and presentation sources; and
6. report reload compatibility visibly to every connected surface.

Vue/CSS edits use normal HMR without restarting the story session. A compatible
story edit reloads definitions and regenerates the frame. An incompatible state,
world, handler, or schema edit marks existing sessions stale and requires an
explicit reload/fork; the dev server must not silently reinterpret durable
session state.

`kitsoki app build <app.yaml>` produces a content-addressed presentation bundle
and application manifest for the existing web and VS Code hosts. Generated
Vite config, caches, and bundles live under Kitsoki-managed `.temp`/artifact
locations, never inside imported story sources or checked-in `node_modules`.
Production hosting serves only built assets; Vite is a development dependency,
not part of the deployed runtime.

## Composition and extension

Application composition follows the current story import fold:

1. imported application pages, components, handlers, and tokens remain
   private and alias-qualified unless explicitly exported;
2. the base framework is folded first, imports follow declaration order, and
   the root story applies explicit overrides last;
3. an unqualified collision is a load-time error unless the root names the
   member under `overrides.application`;
4. component overrides must satisfy the base props/actions contract and retain
   fallbacks for every required surface;
5. handler overrides must be input/output/outcome compatible and may not weaken
   effect or capability policy;
6. UI actions may target only exported intents/handlers visible through the
   import boundary; and
7. a composed frame records provenance for each page/card/component/action so
   trace and developer tooling can explain which story supplied it; and
8. semantic refs remain stable across composition and placement overrides.
   Overrides may deliberately replace a ref only through an explicit alias that
   preserves the prior ref for report lookup and trace replay.

The default wizard framework is an ordinary versioned component package, not a
dummy story with an unwanted room graph. It does not receive privileged
business APIs. A presentation-free consumer can import the same package for
schemas, navigation, room interfaces, and actions without loading Vue.

## POG acceptance target

POG is the first external conformance target, not a source of Kitsoki-specific
special cases. “POG is 100% implemented as Kitsoki stories” means:

- every product page, card, form, navigation item, command, and product handler
  is declared by a POG story/application fragment;
- every reportable or interactive product node has a stable semantic ref,
  meaningful name and description, source provenance, relationships, and a
  deterministic projection on each required surface;
- every business interaction resolves to a story intent or exported handler;
- custom POG Vue components live with and are loaded from those story packages;
- web and VS Code reuse the same presentation modules and handler contracts;
- CLI, MCP, and JSON-RPC discovery expose the same eligible handlers;
- POG-owned server code is limited to deployment concerns such as auth,
  tenancy/config binding, static asset hosting, and health checks; and
- the program graph has no unexplained product route, API handler, command, UI
  entry, world mutation, or external effect outside the story manifests; and
- a feedback report created from web, VS Code, TUI, or a handler receipt resolves
  to the same POG semantic node and owning story without relying on visible text
  or a DOM selector; and
- totality, room-interface, event, routing-mode, component-fallback,
  idempotency/compensation, budget, and route-feedback conformance are green for
  every capability the POG application uses.

The acceptance proof must use a no-LLM POG fixture and scenario inventory. It
drives at least one end-to-end wizard workflow plus one background event through
web, VS Code reuse, TUI, CLI, MCP, and JSON-RPC; compares normalized
action/outcome/frame receipts; exercises the deterministic routing pin; and
fails if a required surface lacks a component fallback or a graph edge lacks
provenance. It also files fixture reports from each surface and proves that the
reviewed bundles resolve to the same semantic refs while excluding undeclared
world/prop values. Real POG migration work belongs in the POG repository under
its own managed workflow.

## Slices

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | story-contracts-and-program-graph | runtime | Outcome/bind/guard totality, room interfaces, parameterized rooms, and a queryable read/write/effect/UI graph with semantic identity and quality lints | — | partial: application/UI/handler/event graph shipped; totality and room contracts remain | — |
| 2 | application-contract | runtime | `application/v1`, canonical frame/action/semantic-node schemas, loader validation, composition, provenance, feedback policy, and legacy typed-view projection | 1 | partial: schemas, validation, include merging, provenance, and frame compiler shipped; full override and legacy-view projection remain | — |
| 3 | handler-and-event-registry | runtime + tracing | Typed intent/Starlark handlers, event binding, routing/budget/idempotency/compensation policy, discovery, outcome envelopes, child-run collections, and receipts | 1 | partial: registry, policy injection seams, discovery, handler-target events, outcomes, and receipt envelopes shipped; schema/policy/sink wiring, deduplication, functional Starlark, session-event modes, and runtime governor wiring remain | — |
| 4 | web-application-runtime | tui + runtime | Vue component registry, default frame renderer, semantic DOM projection/inspection, scoped theming, Vite dev/HMR ownership, stale-session rules, and production bundle | 2, 3 | partial: default renderer, component registry/fallbacks, semantic inspection, live runstatus host, and stale-frame rules shipped; managed dev/HMR/build lifecycle remains | — |
| 5 | wizard-application-framework | story | Reusable headless + default card presentation with navigation, forms, validation, errors, progress, events, and extension fixtures | 2, 4 | not cut | — |
| 6 | surface-adapters | tui + runtime | VS Code web/native, TUI, CLI, MCP, and JSON-RPC projections over shared frame/handler contracts, including semantic inspection, routing, and feedback anchors | 2, 3, 5 | partial: web/VS Code reuse, TUI projection, and initial CLI, MCP, and JSON-RPC adapters shipped; native VS Code/TUI host integration, non-required session modes, and cross-surface feedback fixture remain | — |
| 7 | versioned-component-packages | runtime | À-la-carte namespaced packages for story/application components, room templates, schemas, UI, and one lockfile shared with kits | 1, 2 | not cut | — |
| 8 | POG adoption and conformance | story | Migrate one vertical slice, generate the program-graph/semantic ownership report, prove cross-surface feedback resolution, then close every remaining non-story product surface | 4, 5, 6, 7 | external; not cut | — |

## Sequencing

```text
#1 contracts/program graph ──▶ #2 application contract ──▶ #4 web runtime
          │                         │                           │
          ├──────────────▶ #3 handler/event registry ──────────┤
          └──────────────▶ #7 component packages               ▼
                                      #5 wizard framework ──▶ #6 adapters
                                                                  │
                                                                  ▼
                                                            #8 POG adoption
```

Slices 2, 3, and 7 can proceed in parallel after the finite contracts and
program graph are pinned. The POG slice starts with one vertical workflow after
slices 1–7 have conformance fixtures; full migration follows only after that
slice proves web HMR, VS Code reuse, terminal fallbacks, transport parity, one
background event, and ownership/totality queries.

## Shared decisions

1. **The semantic frame is canonical; Vue is optional.** A story application
   remains usable by headless, TUI, replay, and future renderers without loading
   JavaScript.
2. **Semantic identity is story-owned and surface-independent.** Stable refs,
   names, descriptions, relationships, and provenance survive layout and copy
   changes; surface locators are evidence, not identity.
3. **Finite contracts run end to end.** Room intent alphabets, effect outcomes,
   exported handlers, events, and UI actions are loader-checked and appear in
   one program graph.
4. **Workflow and UI share one action boundary.** Components dispatch declared
   intents/handlers; they do not acquire a second application controller.
5. **One handler registry feeds every protocol.** JSON-RPC, MCP, CLI, web,
   VS Code, and TUI adapters contain no business behavior.
6. **Web is the reusable rich-presentation substrate.** VS Code reuses it by
   default and adds native projections only where editor-native UX is better.
7. **Custom presentation is trusted application code with explicit
   capabilities.** Starlark retains its capability sandbox; browser modules
   receive only injected frame/action/artifact services and are subject to CSP.
8. **Composition is explicit and fail-fast.** Namespace, compatibility,
   fallback, and effect-policy violations fail at load/conformance time.
9. **Feedback reuses the annotation/report substrate.** Application nodes emit
   `semantic_element` anchors and privacy-declared context; no parallel locator
   or application-specific bug format is introduced.
10. **Durable sessions are never silently hot-reloaded across incompatible
   schemas.** HMR convenience does not override runtime truth.
11. **Tests are deterministic and no-LLM.** Contract matrices, flow cassettes,
   fake handlers, and generated fixtures cover adapters; live model calls are
   not part of build or conformance gates.

## Cross-cutting open questions

1. **Does `application:` live directly in `app.yaml` or in a referenced
   `application.yaml`?** *Lean: both author forms compile through one loader;
   inline suits small stories, a referenced file suits full products.*
2. **How much native VS Code vocabulary belongs in `application/v1`?**
   *Lean: commands, trees, quick picks, status items, and editor/document
   actions only; arbitrary extension activation code remains outside v1.*
3. **Should MCP expose one tool per handler or one generic `application.call`
   tool?** *Lean: bounded per-handler tools when the registry fits client
   limits, with a generic discovery/call fallback for large applications.*
4. **How are external Vue dependencies resolved?** *Lean: a lockfile-backed,
   allowlisted application dependency manifest compiled by Kitsoki; no runtime
   CDN imports and no implicit access to Kitsoki's private SPA dependencies.*
5. **How is the default wizard framework distributed initially?**
   *Lean: as an embedded versioned component package with no room graph, using
   the same manifest shape required for later kit distribution.*
6. **What is the minimum frame diff protocol?** *Lean: ship full versioned
   frames first; add JSON Patch only after measurements show frame size or
   update latency is material.*

## Non-goals

- Replacing the story state machine, typed views, imports, host interfaces, or
  runstatus transport with a frontend-specific runtime.
- Requiring Vue for headless, TUI, CLI, MCP, JSON-RPC, replay, or test use.
- Allowing arbitrary browser modules to invoke host functions or filesystem/
  network capabilities outside exported handlers.
- Providing a hosted marketplace, npm registry, or remote module CDN.
- Making every web control native in VS Code; reuse through webviews is the
  default and native projection is selective.
- Hiding surface gaps with generic HTML or screenshots when a required semantic
  fallback is absent.
- Moving POG product logic into Kitsoki core.
