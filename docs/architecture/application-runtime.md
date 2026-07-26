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

The registry separately injects JSON Schema validation, authorization, effect
policy, budget governance, session creation, event scheduling, receipt storage,
and idempotent replay. The runstatus host supplies durable JSONL receipt/replay
journals and its job scheduler; tests can replace every dependency without
starting a model or external service. This keeps transport adapters mechanical
and keeps application business behavior out of the web renderer, CLI, MCP, and
JSON-RPC layers.

## Frame and action lifecycle

An action envelope identifies an action, JSON input, session, and the revision
of the frame that exposed it. Dispatch follows this order:

1. Load the current frame and verify session and revision.
2. Find the declared action and verify it is enabled.
3. Validate Draft 2020-12 input schemas and routing constraints.
4. Dispatch to the exported handler registry or injected intent dispatcher.
5. Apply injected authorization, effect, budget, session, and replay policy.
6. Invoke the intent, Starlark function, or host-interface implementation.
7. Validate the declared output and outcome, then durably record its receipt.
8. Attach the refreshed frame when the handler did not return one.

Stale revisions fail before invocation. A surface may display that failure as a
`STALE_FRAME` frame error while it obtains a new frame, but staleness never
causes the runtime to reinterpret the old envelope against new state.

Handlers enforce `session: none|required|create`. Write and external handlers
apply declared idempotency scope; replays return the stored outcome with a new
transport-aware receipt and never reinvoke behavior. Retryable external
handlers must declare compensation or an explicit impossibility reason.

Events enter the same registry through `DispatchEvent`. A target may be an
exported handler or a story intent. Background events are submitted to the
runstatus job scheduler and expose durable child/join state. Interrupt events
cancel the active session turn before dispatch and fail closed when the host
cannot provide cancellation. Event receipts retain the source event, mode,
session, routing pin, and handler semantic ref.

## Story compilation

`internal/app` treats `application:`, exported handlers, events, typed views,
room interfaces, effect outcomes, bindings, and guards as one finite program.
The loader validates references and totality before a session starts. The
program graph records workflow nodes, reads/writes/effects, application
semantics, handler/event edges, and provenance for impact and ownership queries.

Imports fold application fragments under the existing story ownership rules.
Child pages, navigation, components, actions, schemas, and tokens remain private
unless named under `exports.application`; exported actions cannot target private
intents. The root may make explicit whole-member replacements under
`overrides.application`; replacements retain compatibility and semantic alias
checks. Stories without an application contract receive a deterministic
generated application projection from their typed views.

Versioned `application-component-package/v1` manifests can supply selected
components, schemas, tokens, room templates, intents, agents, toolboxes,
providers, and host interfaces without importing a room graph. Selection is
resolved through the project kit repository and the same `.kitsoki/kits.lock`
pin used by kits; an unpinned version, digest mismatch, collision, or missing
portable fallback fails loading.

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

`ApplicationWizard.vue` is the default multi-page shell. It projects progress,
navigation, forms, field validation, frame errors, workflow/budget state, and
background-event activity without product-specific behavior. Story component
modules are loaded from the generated presentation manifest, receive only the
frame/body/action dependencies, and use scoped application theme tokens.

## Development and build lifecycle

`kitsoki app dev <app.yaml>` owns the local application loop. It validates that
all watched sources remain under the story root, creates generated Vite inputs
under `.temp/application/<app>/<digest>`, starts the Kitsoki backend unless an
existing one was requested, and launches the repository-pinned Vite executable.
The generated shell follows a current session for that application or creates
one from the backend's canonical story catalog.

The watcher classifies edits as presentation-only, compatible
frame-definition refreshes, or reload-required durable-schema changes.
Existing sessions are never silently reinterpreted after an incompatible edit.

`kitsoki app build <app.yaml>` uses the same generated inputs and publishes a
content-addressed manifest and static bundle under
`.artifacts/application-builds/<app>/<digest>`. Story sources and checked-in
`node_modules` remain untouched. Identical output reuses the published digest
directory and original timestamp; a mismatch between stored assets, manifest,
and digest fails instead of replacing immutable content. Production hosts serve
only manifest-declared regular files from that bundle at
`/application/<app>/<asset>`, redact host-local paths from the public manifest,
and do not embed a Vite development server.

Selected component-package token JSON is reduced to a finite scoped theme
vocabulary during preparation. The generated entry installs those tokens before
mounting the application, making development and production presentation
equivalent without allowing arbitrary CSS-variable injection.

## Native projections

VS Code webviews reuse the web application. The extension also registers the
current frame's declared native commands, retaining semantic ref, session, and
revision in each command envelope. Registrations are replaced and disposed when
the current session changes.

The interactive TUI mounts the canonical terminal projection and routes
focusable actions and form choices through the same `application.Service`.
Fresh and resumed sessions therefore share stale-frame checks, schema
validation, actor attribution, policy dependencies, and receipts. Native
projections do not acquire a second business-logic path.

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

`internal/applicationfeedback` produces the same host
`AnnotationAnchor.semantic_element` contract for non-DOM surfaces and receipt
reports. Feedback context is evaluated from a finite allowlist of frame
metadata and applies the story's include, exclude, redact, or hash policy.
Component props, field values, world snapshots, credentials, and local paths
are not input to the builder.

JSON-RPC, CLI, web, VS Code, and TUI submit the reviewed
`kitsoki.feedback.report.v1` bundle through the existing durable local feedback
sink. Studio MCP returns the identical sink-compatible bundle because it does
not own a runstatus intake instance. Idempotency is carried through the sink,
and adapters add surface evidence without changing the canonical semantic ref.

## Conformance

Deterministic tests compare discovery, action/call outcomes, refreshed frames,
routing pins, semantic refs, and normalized receipts across web, VS Code, TUI,
CLI, MCP, and JSON-RPC transports. The fixture includes a wizard action and a
background event and never invokes an LLM. Adapter packages separately prove
their protocol encoding, native projections, lifecycle classification, and
durable journal recovery.

POG remains an external adoption target. Kitsoki contains no POG product logic;
the POG repository can consume this contract and run the same conformance
boundary through its own managed workflow.

See [Story applications](../stories/applications.md) for the authoring surface.
