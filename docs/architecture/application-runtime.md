# Application runtime

The application runtime separates story-owned meaning and behavior from each
surface's presentation. Its canonical boundary is
`internal/application.Frame`, serialized as `application-frame/v1`.

## Runtime boundaries

The loader validates the author-facing `application/v1`, exported handlers, and
event bindings in `internal/app`. `internal/application.CompileFrame` then
projects one current page and session revision into a deterministic frame. The
frame carries application and page semantics, workflow state, navigation,
page/component/handler descriptors, actions, regions, errors, and declared
capabilities. It contains only serializable values.

`internal/application.Service` coordinates three injected dependencies:

| Dependency | Responsibility |
|---|---|
| `FrameProvider` | Return the canonical current frame for a session |
| `Registry` | Discover and invoke typed handlers and events |
| `IntentDispatcher` | Send intent-backed actions through the story state machine |

The registry separately injects schema validation, authorization, budget
governance, and receipt storage. This keeps transport adapters mechanical and
keeps application business behavior out of the web renderer, CLI, MCP, and
JSON-RPC layers.

## Frame and action lifecycle

An action envelope identifies an action, JSON input, session, and the revision
of the frame that exposed it. Dispatch follows this order:

1. Load the current frame and verify session and revision.
2. Find the declared action and verify it is enabled.
3. Validate structural input and routing constraints.
4. Dispatch to the exported handler registry or injected intent dispatcher.
5. Apply injected schema, authorization, budget, and receipt policies when the
   host configures them.
6. Invoke the implementation and record its typed outcome receipt.
7. Attach the refreshed frame when the handler did not return one.

Stale revisions fail before invocation. A surface may display that failure as a
`STALE_FRAME` frame error while it obtains a new frame, but staleness never
causes the runtime to reinterpret the old envelope against new state.

Handler-target events enter the same registry through `DispatchEvent`. Their
source and mode are declarations, while their target still resolves to a
validated handler. The registry preserves the handler's session, routing, and
exposure rules and applies any configured policy dependencies. Intent-target
events and background-versus-interrupt scheduling remain runtime integration
work.

## Web projection

`tools/runstatus/src/application/ApplicationFrameRenderer.vue` is the default
Vue projection. It takes three dependencies:

- an immutable `ApplicationFrame`;
- an `ApplicationActionDispatcher`; and
- an optional `ApplicationComponentRegistrySource`.

The renderer owns layout and local pending/error display only. It creates exact
transport-neutral envelopes and delegates invocation. Frame errors are rendered
as alerts; action rejection and dispatcher exceptions remain visible. Disabled
or pending actions cannot dispatch. Stale state disables every action and
navigation control.

Built-in frame elements render without story code. A component element resolves
its name through `ApplicationComponentRegistry`. The registered Vue component
receives schema-owned props, the frame, its element descriptor, and the same
dispatcher. If no component exists, the registry fallback is tried, followed by
the frame component descriptor's finite fallback. Unknown elements without a
fallback produce a visible error.

The TypeScript wire types in `tools/runstatus/src/application/types.ts` mirror
the Go JSON field names. In particular, provenance lives under
`semantic.source`, component data remains JSON under `props`, element content
remains JSON under `value`, and action envelopes retain `frame_revision`.

## Semantic inspection

Every rendered semantic root projects canonical metadata into stable data
attributes. `inspectApplicationSemanticElement` walks to the closest such root,
then resolves its ref against the frame, including nested element actions. Its
result uses the existing annotation vocabulary:

```json
{
  "kind": "semantic_element",
  "semantic_element": {
    "plugin": "kitsoki.application",
    "ref": "review.card.pending",
    "semantic_kind": "card",
    "label": "Changes",
    "description": "List changes that can be opened for review.",
    "data": {
      "application_id": "review",
      "frame_revision": 12,
      "program_node": "application.pages.inbox.regions.main.items[0].card",
      "story": "review"
    }
  }
}
```

DOM location is surface evidence only. The ref, source story, program node, and
relationships come from the validated application graph and frame. This lets
feedback and developer tools retain ownership when layout or visible copy
changes.

## Current scope

The implemented runtime provides contract loading and validation, deterministic
frame compilation, handler/event policy and receipts, CLI static inspection and
live calls, JSON-RPC and Studio MCP adapters, a TUI projection, and the reusable
default Vue projection used by the web/VS Code host. Story-owned functional
Starlark handler execution, Vite dev/HMR orchestration, production presentation
bundles, native VS Code commands/trees, mounting the TUI projection in the
interactive terminal host, durable receipt storage/idempotent replay, and
live schema/authorization/budget dependency wiring remain separate integration
work.

See [Story applications](../stories/applications.md) for the authoring surface.
