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

application:
  schema: application/v1
  name: Review
  description: Inspect and resolve changes that need operator review.
  semantic_ref: review.application
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
```

Every application, navigation item, page, region, card, component, action, and
exported handler needs a stable application-qualified `semantic_ref`, a useful
name, and a purpose-oriented description. Refs are identity: changing visible
copy or moving a card must not change its ref. The loader rejects missing or
duplicate refs, dangling page/component/action targets, unsupported fallback
kinds, and actions that do not resolve to an intent or exported handler.

## Exported handlers

Handlers live under `exports.handlers`, outside the presentation declaration.
They specify JSON schemas, session policy, effect class, routing policy,
outcomes, and exposed transports. An application action names a handler or an
intent; it does not reimplement the handler's behavior.

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
disabled action, malformed JSON input, and disallowed routing mode before
invoking business behavior. Registry hosts may inject JSON-schema validation,
authorization, budget governance, and durable receipt storage; the initial live
runstatus adapter does not yet wire those policy services. Successful handler
and handler-target event invocations produce an outcome envelope and receipt.

## Default web presentation

`tools/runstatus/src/application` contains the reusable Vue default renderer.
It renders frame navigation, regions, cards, built-in body elements, and
actions. Custom components are supplied through an injected component registry.
Each component receives its serializable props plus `frame`, `body`, and the
injected `dispatch` function. It does not receive story internals.

When a custom component is unavailable, the renderer uses the component's
declared finite fallback. If neither implementation nor fallback exists, it
shows an explicit unsupported-content error rather than an empty card.
Disabled actions remain disabled. A `STALE_FRAME` frame error or the renderer's
explicit `stale` input disables all navigation and action dispatch until the
host refreshes the frame.

Rendered semantic roots carry `data-semantic-ref`, `data-semantic-kind`,
`data-semantic-name`, `data-semantic-description`, and provenance attributes.
`inspectApplicationSemanticElement` resolves the closest root back to canonical
frame metadata and returns a `kitsoki.application` semantic-element anchor.
Product code should use that ref, not a CSS selector or visible text, for
inspection and feedback ownership.

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
```

`describe` prints the validated `application/v1` declaration, `handlers`
reports exported handlers (optionally filtered by transport), and `graph`
prints the story program graph. `call` invokes a handler on an existing live
web/daemon session through `runstatus.application.call`; it does not simulate a
session or instantiate transport-specific business behavior.

The live JSON-RPC methods are `runstatus.application.frame`, `.discover`,
`.inspect`, `.call`, `.action`, and `.event`. The legacy full Studio MCP
toolbox exposes the same registry as `application.frame`, `.discover`,
`.inspect`, `.call`, `.action`, and `.event`. Strict MCP profiles do not acquire
these mutating session tools.

See [Application runtime](../architecture/application-runtime.md) for the frame,
registry, semantic inspection, dispatch, and receipt boundaries.
