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
      pages: [inbox]
  shell:
    presentation: default
    entry: inbox
  navigation:
    - id: inbox
      name: Inbox
      description: Open changes waiting for review.
      semantic_ref: review.nav.inbox
      page: inbox
      route: /reviews
      state: review
  pages:
    inbox:
      name: Review inbox
      description: Show the changes currently waiting for an operator.
      semantic_ref: review.page.inbox
      route: /reviews
      state: review
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
                  limit: 20
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
      target_page: inbox
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

## Canonical routes and story state

Pages may declare a canonical absolute `route` template. Placeholders must be
finite whole path segments such as `{change_id}`; parameter names are lowercase
identifiers. Navigation may repeat or supply the target page's route, but every
alias for one page must agree. The loader rejects trailing slashes, query or
fragment syntax, duplicate parameters, and any two templates that can match the
same path, including static/dynamic overlaps such as `/changes/new` and
`/changes/{change_id}`.

A page may map declared parameters into declared string world keys. The server
applies this finite mapping before it enters and renders the bound story state,
so a direct URL reload restores story-owned selection without consulting
browser globals:

```yaml
pages:
  change:
    route: /changes/{change_id}
    route_bindings:
      change_id: selected_change_id
    state: review
```

Every binding key must name a placeholder in that page's route and every target
must be a declared `world` key with `type: string`. Imported page bindings
rebase imported world keys with the same alias rules as other world references.

An optional page `state` binds presentation navigation to a story state.
Navigation may repeat or supply that binding. Opening a bound page through a web
or VS Code reused-web route synchronizes the live story state through the
runtime's guarded state boundary. A page with neither `state` nor
`route_bindings` never mutates story state; an unbound page with explicit route
bindings keeps the current state path while atomically applying those values.
Several informational pages may share one state. After an action transitions
the story, the current page is retained when it is still bound to that state,
or the unique page bound to the new state is selected. When several pages bind
the new state, the action must declare `target_page` to make the outcome
deterministic. Imported page states and target pages are alias-rebased with the
rest of the application fragment.

The frame exposes route descriptors, the resolved `route_path`, structured
`route_params`, and page state targets. Web and VS Code's reused web projection
use the browser History API for navigation and `popstate`; they do not install
a story-specific router. TUI, CLI, JSON-RPC, and MCP keep addressing pages by
page ID and receive the same route-parameter compile context when supplied.

## Live application data

`application.data` is the only story-world path into
`application-frame/v1`. Each entry names exactly one declared `world` key,
assigns a sensitivity and exposure policy, and may restrict exposure to named
pages with `pages`. An omitted `pages` list retains the compatibility behavior
of exposing the value on every page. Names must start with a lowercase letter
and contain only lowercase letters, digits, underscores, or hyphens. Sources
use the finite `world.<key>` form; nested paths, environment variables,
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

Page filtering happens before component props compile, so an internal value
scoped to `dashboard` is absent from a public page frame even when both pages
belong to one application. Imported data and page scopes are alias-qualified
together. Callers that compile a definition without a live world remain
compatible and receive no `data` field.

## Typed package component bindings

New data/event bindings apply only to components selected from a versioned
`application-component-package/v1` through the existing lock-verified
`application.packages` mechanism. A binding cannot introduce a module path.
Legacy local component declarations and static `props` remain compatible, but
they do not acquire the typed binding surface.

```yaml
application:
  packages:
    - package: kitsoki.review-ui
      select: [components.change-list]
  pages:
    inbox:
      # ...
      regions:
        main:
          # ...
          items:
            - card:
                id: pending
                name: Changes
                description: List changes waiting for review.
                semantic_ref: review.card.pending
                component: kitsoki.review-ui.change-list
                bindings:
                  props:
                    items: {source: data, key: pending_changes}
                    workflow_state: {source: frame, path: [workflow, state]}
                    change_id: {source: route, key: change_id}
                    compact: {source: literal, value: true}
                    empty_note: {source: literal, value: null}
                  events:
                    open:
                      action: review.change.open
                      input:
                        change_id: {source: event, path: [change, id]}
                        origin: {source: literal, value: inbox}
```

Prop sources are a finite union: `literal`, an exposed `data` key, one
allowlisted canonical `frame` path, or an explicit `route` parameter. Route
parameters are never read from browser globals; a caller must supply them via
`CompileFrameWithContext`. Missing data or route values omit that prop.
Resolved props are validated in full against the selected member's
`props_schema` before the frame is emitted. Explicit `value: null` remains a
JSON null and is distinct from an omitted literal value.

The package member declares emitted event names and portable payload schemas:

```yaml
components:
  change-list:
    name: Change list
    description: Present reviewable changes.
    semantic_ref: kitsoki.review-ui.component.change-list
    props_schema: schemas/change-list.json
    events:
      open: schemas/change-open-event.json
    web: {module: ui/ChangeList.js, export: default}
    fallback: {element: table, value_prop: items}
```

Only declared events can bind. Event inputs use structured payload paths or
literals and target one named application action, which already resolves to a
story intent or handler. The web renderer validates emitted payloads against
the package schema before mapping them; the application service then validates
the mapped input against the action schema before dispatch. Portable event
schemas deliberately reject network references and unsupported keywords.
Native surfaces retain the same component and action semantic refs and project
the fallback's selected resolved prop instead of dumping the full prop object.

Use provider-to-world-to-frame composition for graph-backed applications. The
deployment binds graph authority to the exact calling application:

```yaml
application_graphs:
  pog:
    project_root: .
    catalog: pog/catalog.yaml
    overlay: pog/catalog.overlay.yaml # optional, read operations only
    max_nodes: 2000
    max_bytes: 1048576
    write_policy: propose
```

`project_root` is resolved beneath the discovered project root. `catalog` and
`overlay` are resolved beneath that configured root. Absolute paths, traversal,
symlink escapes, missing paths, and non-regular catalog files fail session
construction. `max_bytes` bounds the configured catalog entry files and every
application request/result. `max_nodes` is injected into `snapshot`.
`write_policy` is `read`, `propose`, or `steward`.

The story declares operation data only:

```yaml
host_interfaces:
  catalog:
    operations:
      snapshot:
        input: {audience: string, fields: list}
        output: {snapshot: object}
    default: host.graph

# In a room effect:
- invoke: iface.catalog.snapshot
  with:
    audience: public
    fields: [title, status, visibility]
  bind:
    public_snapshot: snapshot
```

For configured Story Applications the public input shapes are:

| Operation | Public input |
|---|---|
| `snapshot` | `audience`, `fields` |
| `get` | `ids`, `fields` |
| `changeset` | `action`, `changeset_id`, `node_id` |
| `project` | `graph_id` |
| `propose` | `title`, `operations`, `visibility`, `validate_only` |
| `authorize` | `changeset_id` |
| `withdraw` | `changeset_id` |
| `rebase` | `changeset_id` |
| `apply` | `changeset_id`, `dry_run` |

`read` permits the four read operations. `propose` additionally permits
`propose`, `withdraw`, `rebase`, and dry-run `apply`. `steward` permits the
complete table, including `authorize` and live `apply`. Graph writes are
stamped with the server-owned `kitsoki.application:<application-id>` actor.
Caller-supplied catalog/overlay paths, URLs, commands, providers, profiles,
actors, sessions, or transports are rejected recursively.

Keep `public_snapshot` world-only, derive a finite public page projection with
capability-free Starlark, and expose only that derived key through
page-scoped `application.data`. Never expose an internal/raw snapshot and rely
on a renderer or component to remove private fields.

Daemon-owned operational providers use the same provider-to-world-to-frame
composition, but their authority is fixed outside the story:

```yaml
host_interfaces:
  streams:
    operations:
      snapshot:
        input: {scope: string, max_streams: int, max_bytes: int}
        output: {snapshot: object}
    default: host.streams
  federation:
    operations:
      snapshot:
        input: {max_workers: int, max_bytes: int}
        output: {snapshot: object}
    default: host.federation
  materialization:
    operations:
      snapshot:
        input: {application_id: string, max_jobs: int, max_bytes: int}
        output: {snapshot: object}
    default: host.materialization
```

The daemon configuration binds these interfaces to an exact application and,
for streams, an exact project scope. The values passed by the story must match
that binding; they are assertions, not selectors. Each call must supply finite
item and byte limits. Results contain opaque queue, worker, session, job,
receipt, and artifact identities only. They never contain source paths,
artifact paths, worker endpoints, tunnels, or credentials.

Bind the returned snapshot into world, derive the exact finite page model in
Starlark, and expose only that derived key through `application.data`. This is
the complete Story Application path for operational products: no product-owned
HTTP server, browser data client, or separate frontend state store is required.

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

When a current frame offers a write/external handler as an application action,
the service derives a stable action-opportunity key from the application,
session, actor, action, handler, frame revision, and normalized input. Web,
VS Code, and TUI renderers therefore do not invent product-specific request
IDs, and retrying the same offered action through another surface returns a
replay receipt instead of repeating the effect. An explicit envelope
`idempotency_key` still takes precedence for headless callers. A handler input
schema should not require its configured idempotency field solely for an
interactive action; direct calls can supply either that field or the envelope
key.

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
    data: [pending_changes]
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
