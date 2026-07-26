# Story applications

A story can declare an optional `application:` block when operators need a
multi-page application surface rather than only the room's typed turn view.
The declaration is renderer-neutral. It names pages, semantic regions, cards,
components, and actions; it does not contain Vue instances, HTML, terminal
coordinates, or transport callbacks.

The supported author schema is `application/v1`:

```yaml
app:
  id: review
  version: 1.0.0

world:
  pending_changes: {type: list, default: []}

application:
  schema: application/v1
  name: Review
  description: Inspect and resolve changes that need operator review.
  semantic_ref: review.application
  data:
    pending_changes:
      source: world.pending_changes
      sensitivity: internal
      policy: include
  shell:
    presentation: default
    entry: inbox
  navigation:
    - id: inbox
      name: Inbox
      description: Open changes waiting for review.
      semantic_ref: review.nav.inbox
      page: inbox
  pages:
    inbox:
      name: Review inbox
      description: Show the changes currently waiting for an operator.
      semantic_ref: review.page.inbox
      regions:
        main:
          name: Pending changes
          description: Present reviewable changes and their available actions.
          semantic_ref: review.region.pending
          items:
            - card:
                id: pending
                name: Changes
                description: List changes that can be opened for review.
                semantic_ref: review.card.pending
                component: review.change-list
                props:
                  source: pending_changes
                actions: [review.change.open]
  components:
    review.change-list:
      name: Change list
      description: Present a compact list of reviewable changes.
      semantic_ref: review.component.change-list
      web:
        module: ui/ChangeList.vue
        export: default
      props_schema: schemas/change-list.json
      fallback:
        element: list
  actions:
    review.change.open:
      name: Open change
      description: Open the selected change in the review workflow.
      semantic_ref: review.action.change-open
      handler: review.change.open
  surfaces:
    web:
      presentation: default
    vscode:
      reuse: web
      native:
        commands: [review.change.open]
    tui:
      projection: cards
```

Every application, navigation item, page, region, card, component, action, and
exported handler needs a stable application-qualified `semantic_ref`, a useful
name, and a purpose-oriented description. Refs are identity: changing visible
copy or moving a card must not change its ref. The loader rejects missing or
duplicate refs, dangling page/component/action targets, unsupported fallback
kinds, and actions that do not resolve to an intent or exported handler.

## Live application data

`application.data` is the only story-world path into
`application-frame/v1`. Each entry names exactly one declared `world` key and
assigns a sensitivity and exposure policy. Names must start with a lowercase
letter and contain only lowercase letters, digits, underscores, or hyphens.
Sources use the finite `world.<key>` form; nested paths, environment variables,
component props, credentials, and host paths are not valid sources.

`include` preserves JSON values classified as `public` or `internal`.
`redact` emits the literal `[redacted]`, `hash` emits a canonical SHA-256
fingerprint, and `exclude` emits no frame entry. `sensitive` and `secret`
values cannot use `include`. A live runstatus frame reads the current session
world through `WorldReader` for every refresh and projects only these entries:

```json
{
  "data": {
    "pending_changes": {
      "value": [{"id": "chg-42", "title": "Review navigation"}],
      "sensitivity": "internal",
      "policy": "include"
    }
  }
}
```

Custom components read this finite map from `frame.data`; static card `props`
remain static author input. Callers that compile a definition without a live
world remain compatible and receive no `data` field.

## Exported handlers

Handlers live under `exports.handlers`, outside the presentation declaration.
They specify JSON schemas, session policy, effect class, routing policy,
outcomes, and exposed transports. An application action names a handler or an
intent; it does not reimplement the handler's behavior.

```yaml
exports:
  handlers:
    review.change.open:
      name: Open change
      description: Open a change and return the refreshed review frame.
      semantic_ref: review.handler.change-open
      input_schema: schemas/change-open.json
      output_schema: schemas/change-result.json
      session: required
      effect: read
      routing_mode: exact
      outcomes: [ok, not_found]
      dispatch:
        intent: open_change
        slots_from: input
      expose: [jsonrpc, mcp, cli, web, vscode, tui]

    review.catalog.search:
      name: Search catalog
      description: Search the local review catalog without a session.
      semantic_ref: review.handler.catalog-search
      input_schema: schemas/search.json
      output_schema: schemas/search-results.json
      session: none
      effect: read
      routing_mode: off
      outcomes: [ok, invalid_query]
      starlark:
        script: scripts/catalog_search.star
      expose: [jsonrpc, mcp, cli]
```

An interactive surface sends the same envelope used by every adapter:

```json
{
  "action": "review.change.open",
  "input": {"change_id": "chg-42"},
  "session_id": "01...",
  "frame_revision": 12
}
```

The runtime rejects a session mismatch, stale frame revision, unknown or
disabled action, malformed JSON input, schema violation, authorization/effect
denial, budget denial, and disallowed routing mode before invoking behavior.
Relative schema references resolve under the story/package root; network,
parent, and symlink escapes are denied.

`session: none` clears ambient session identity, `required` rejects an absent
session, and `create` obtains one through the injected session manager.
Write/external handlers declare `idempotency.key` and `scope`; retryable
external handlers additionally declare `compensation` or
`compensation_impossible`. Durable replay returns the stored result with a new
transport-aware receipt and does not repeat the side effect.

## Events

Events dispatch the same handler or intent contracts without fabricating a
user turn:

```yaml
events:
  review-index-updated:
    source: review.index.updated
    input_schema: schemas/index-updated.json
    session: required
    mode: background
    routing_mode: exact
    dispatch:
      handler: review.catalog.search
```

`background` submits durable work to the host scheduler and returns child/join
state. `interrupt` first cancels the active session turn and then dispatches;
hosts without cancellation support fail closed. Daemon-backed sessions
reacquire durable job state after restart, while process-bound non-idempotent
work is not claimed as resumed.

## Composition and packages

Imported application pages, components, actions, handlers, schemas, and tokens
fold with the story import graph. Explicit root replacements live under
`overrides.application`; compatibility checks prevent a replacement from
weakening schemas, outcomes, effects, or required fallbacks. Semantic aliases
preserve report and replay lookup when a ref is deliberately replaced.

Application fragments are private unless the child explicitly exports them:

```yaml
exports:
  intents: [open_change]
  application:
    navigation: [inbox]
    pages: [inbox]
    components: [review.change-list]
    actions: [review.change.open]
    schemas: [change-list]
    tokens: [default]
```

An exported page may reference only exported components and actions, and an
exported action may target only an exported intent or handler. Import folding
keeps exported members alias-qualified and rejects missing members or private
dependencies instead of widening the child boundary.

Reusable UI/runtime fragments can use
`application-component-package/v1` in `component-package.yaml`. A package may
offer components, schemas, tokens, room templates, intents, agents, toolboxes,
providers, and host interfaces without adding a room graph. Consumers select
only required members:

```yaml
application:
  packages:
    - package: kitsoki.wizard-ui
      select:
        - components.form
        - schemas.form-input
        - tokens.default
```

Package versions and tree hashes must be pinned in the project
`.kitsoki/kits.lock`. Selection collisions, missing files, digest/version
mismatches, or a custom component without a fallback for a required non-web
surface fail loading.

## Default web presentation

`tools/runstatus/src/application` contains the reusable Vue default renderer.
It renders frame navigation, regions, cards, built-in body elements, and
actions. Custom components are supplied through an injected component registry.
Each component receives its serializable props plus `frame`, `body`, and the
injected `dispatch` function. It does not receive story internals.
The dispatcher resolves to the canonical application outcome, including
`outcome`, `output`, `receipt`, and the refreshed `frame`, so a component can
consume a typed result without calling a transport directly.

When a custom component is unavailable, the renderer uses the component's
declared finite fallback. If neither implementation nor fallback exists, it
shows an explicit unsupported-content error rather than an empty card.
Disabled actions remain disabled. A `STALE_FRAME` frame error or the renderer's
explicit `stale` input disables all navigation and action dispatch until the
host refreshes the frame.

The default `ApplicationWizard` adds page progress, navigation, form input and
validation, errors, workflow/budget state, and event activity. Story Vue
components are loaded from the generated application manifest. They receive
only serializable props, the frame/body descriptors, and the injected
dispatcher. Theme tokens are scoped below the application root.

Rendered semantic roots carry `data-semantic-ref`, `data-semantic-kind`,
`data-semantic-name`, `data-semantic-description`, and provenance attributes.
`inspectApplicationSemanticElement` resolves the closest root back to canonical
frame metadata and returns a `kitsoki.application` semantic-element anchor.
Product code should use that ref, not a CSS selector or visible text, for
inspection and feedback ownership.

Feedback context is an explicit allowlist:

```yaml
application:
  feedback:
    context:
      workflow_state:
        source: frame.workflow.state
        sensitivity: internal
        policy: include
```

Supported policies are `include`, `exclude`, `redact`, and `hash`; sensitive or
secret values cannot use `include`. Runtime anchor construction reads only
allowlisted frame metadata. It does not read component props, field values,
world snapshots, credentials, or local paths.

## Development and production bundles

Run the managed development loop from the project:

```sh
go run ./cmd/kitsoki app dev stories/review/app.yaml
```

This starts the Kitsoki backend and repository-pinned Vite server, creates or
follows the application session, watches story/Starlark/schema/presentation
sources, and writes generated inputs only below `.temp/application`. Vue/CSS
changes use HMR; compatible definition changes refresh the frame; durable shape
changes show a reload-required state.

Build a content-addressed production presentation with:

```sh
go run ./cmd/kitsoki app build stories/review/app.yaml
```

The manifest and assets are published below
`.artifacts/application-builds/<app>/<digest>`. Production hosts serve these
assets at `/application/<app>/<asset>` and reuse the same JSON-RPC/session
runtime; Vite is not deployed. The host serves only files named by a validated
manifest and strips local module and artifact paths from the public manifest.
Rebuilding identical inputs reuses the existing digest directory and its
original `created_at`; published digest directories are never rewritten.

Selected package token JSON is validated against the finite scoped theme
vocabulary during preparation. The generated entry installs those tokens below
the application root before Vue mounts, so development and production builds
use the same theme without exposing arbitrary CSS variables.

VS Code webviews reuse this presentation. Declared
`surfaces.vscode.native.commands` are registered from canonical actions and
retain their semantic ref and revision. The TUI mounts its typed projection and
routes form choices and actions through the same application service and
receipts.

## Inspecting an application

The current CLI exposes static inspection without starting a live session:

```sh
go run ./cmd/kitsoki app describe stories/review/app.yaml
go run ./cmd/kitsoki app handlers stories/review/app.yaml
go run ./cmd/kitsoki app handlers --transport mcp stories/review/app.yaml
go run ./cmd/kitsoki app graph stories/review/app.yaml
go run ./cmd/kitsoki app call review.change.open \
  --url http://127.0.0.1:8080 \
  --session-id 01... \
  --input '{"change_id":"chg-42"}'
go run ./cmd/kitsoki app feedback review.card.pending \
  --url http://127.0.0.1:8080 \
  --session-id 01... \
  --instruction "Opening this change returned the wrong revision."
```

`describe` prints the validated `application/v1` declaration, `handlers`
reports exported handlers (optionally filtered by transport), and `graph`
prints the story program graph. `call` invokes a handler on an existing live
web/daemon session through the server-bound `runstatus.application.cli_call`
adapter; it does not simulate a
session or instantiate transport-specific business behavior. `--session-id` is
required only for `session: required` handlers. `feedback` resolves the
semantic ref against the current frame, builds the privacy-safe reviewed report,
and persists it through the shared local feedback sink.

The live JSON-RPC methods are `runstatus.application.frame`, `.discover`,
`.inspect`, `.feedback`, `.call`, `.cli_call`, `.action`, `.web_action`,
`.vscode_action`, and `.event`.
The `.call` method is bound to `jsonrpc`; the reserved `.cli_call` adapter is
bound to `cli`. Likewise, `.action` is bound to `jsonrpc`, while the reserved
`.web_action` and `.vscode_action` adapters are bound to their host transports.
None accepts a client-selected transport. The legacy full Studio MCP
toolbox exposes the same registry as `application.frame`, `.discover`,
`.inspect`, `.feedback`, `.call`, `.action`, and `.event`. The MCP feedback tool
returns the same reviewed sink-compatible bundle for the client to submit.
Strict MCP profiles do not acquire these mutating session tools.

See [Application runtime](../architecture/application-runtime.md) for the frame,
registry, semantic inspection, dispatch, and receipt boundaries.
