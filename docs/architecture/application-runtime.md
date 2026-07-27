# Application runtime

The application runtime separates story-owned meaning and behavior from each
surface's presentation. Its canonical boundary is
`internal/application.Frame`, serialized as `application-frame/v1`.

## Runtime boundaries

The loader validates the author-facing `application/v1`, exported handlers, and
event bindings in `internal/app`. `internal/application.CompileFrame` then
projects one current page and session revision into a deterministic frame. The
frame carries application and page semantics, workflow state, explicitly
allowlisted application data, navigation,
page/component/handler descriptors, actions, regions, errors, and declared
capabilities. It contains only serializable values.

Canonical application routes are compiled into the same frame. The runstatus
boundary resolves an incoming absolute path to a declared page and decoded
string parameter map before calling `CompileFrameWithContext`. The default web
projection updates browser history and reloads frames on `popstate`; the VS
Code reused-web surface inherits that adapter inside its hosted bundle. Neither
surface owns a product router. Headless transports continue to use page IDs and
may pass the same structured route parameters explicitly.
Finite page `route_bindings` may copy declared parameters to declared string
world keys through the injected application navigation boundary. A bound-state
navigation applies those values in the same teleport that enters the state, so
the story renders the selected record on first load.

The runstatus frame provider reads a session world only through the optional
`WorldReader` interface. `CompileFrameWithData` then evaluates the story's
`application.data` declarations and copies no ambient world map. Public and
internal values may be included; sensitive and secret values must be excluded,
redacted, or hashed. Fixed-entry and legacy callers continue to use
`CompileFrame` and receive no frame data.

Provider data follows the same ownership boundary. A story may invoke a
read-only host interface such as `iface.catalog.snapshot` backed by
`host.graph.snapshot`, bind its bounded result into world, and derive finite
page models with capability-free Starlark. Only those derived world keys belong
in `application.data`; raw provider snapshots remain world-only. Public models
must originate from a provider call made with `audience: public`, not from
client-side filtering of an internal snapshot.

Graph-backed Story Applications receive an application-scoped replacement for
the generic `host.graph` handler when their exact application ID appears under
`.kitsoki.yaml` `application_graphs`. Session construction resolves the
configured project root, catalog, and optional overlay to server-owned real
paths and rejects absolute paths, traversal, symlink escapes, and non-regular
files. Unconfigured sessions retain the generic handler used by the bare graph
CLI and MCP surfaces.

The replacement accepts only operation data for `snapshot`, `get`,
`changeset`, `project`, `propose`, `authorize`, `withdraw`, `rebase`, and
`apply`. It recursively rejects path, URL, command, provider/profile,
actor/session, and transport authority, injects the resolved paths and
configured snapshot node bound, enforces request/result byte bounds and the
`read|propose|steward` write policy, stamps writes with a server-owned
application actor, then delegates to the existing graph handler. The graph
loader, linting, changeset guards, provenance rules, and transactional write
invariants therefore remain shared with the generic surface.

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

Page/story-state bindings participate only at the frame boundary. Explicit
navigation to a bound page uses the injected state synchronizer. Outcome
refresh preserves a page that remains bound to the resulting state, selects a
unique matching page, or uses the action's declared `target_page` when several
pages share that state. Unbound pages without route bindings never mutate
story state.

Stale revisions fail before invocation. A surface may display that failure as a
`STALE_FRAME` frame error while it obtains a new frame, but staleness never
causes the runtime to reinterpret the old envelope against new state.

Handlers enforce `session: none|required|create`. Write and external handlers
apply declared idempotency scope; replays return the stored outcome with a new
transport-aware receipt and never reinvoke behavior. Retryable external
handlers must declare compensation or an explicit impossibility reason.
Handler-backed application actions acquire a deterministic, actor-scoped
action-opportunity key from the current frame revision and normalized input
when the envelope does not already carry a key. This keeps renderer retries
mechanical and idempotency authority out of product components.

Events enter the same registry through `DispatchEvent`. A target may be an
exported handler or a story intent. Background events are submitted to the
runstatus job scheduler and expose durable child/join state. Interrupt events
cancel the active session turn before dispatch and fail closed when the host
cannot provide cancellation. Event receipts retain the source event, mode,
session, routing pin, and handler semantic ref.

The daemon-only [`host.application_job`](application-jobs.md) provider lets a
registered caller submit one deployment-configured background event. The
public boundary accepts only a template and JSON input, then exposes a stable
artifact-job reference and configured opaque output handles. Target
application, event, route, session, and scheduler child identities remain in a
private durable mapping.

The daemon-only `host.campaign` provider applies the same boundary to recurring
work declared in a generic project graph. Its daemon configuration fixes the
catalog and node type; the calling application fixes `application_id`, so story
input cannot widen discovery. Campaign definitions may dispatch only an exact
story and intent. Their durable schedule records enforce enabled and paused
state, cadence, UTC-day budget, concurrency, and idempotency before a dispatcher
creates an artifact-job-backed session.

Campaign watcher references are process-bound scheduler jobs. Dispatched
artifact-job references, definitions, next due times, claims, and outcomes are
durable. At daemon restart, running claims are recorded as interrupted and
watchers are recreated only by a later `host.campaign.watch` call; the runtime
does not claim that arbitrary in-flight behavior resumed.

The daemon may also bind three application-scoped operational read models:
`host.streams.snapshot`, `host.federation.snapshot`, and
`host.materialization.snapshot`. Their sources are runtime services, not paths
or providers selected by story input:

- streams project receipt-bound Capsule merge-queue candidates for one fixed
  project scope;
- federation projects the canonical worker registry, daemon health, and
  effective placement policy without endpoints, tunnels, or credentials; and
- materialization projects the authoritative application-owned
  `graph.materialize` lifecycle from durable storage.

Bindings are keyed by exact application ID under daemon web configuration.
Unbound applications and non-daemon processes receive fail-closed builtin
handlers. Every request supplies explicit item and byte bounds; results are
strict, deduplicated, invalid-counted snapshots containing only opaque
identities and artifact handles. Story input cannot choose a filesystem root,
queue file, worker endpoint, credential, or alternate application.

The materialization producer writes its terminal projection synchronously
before publishing the terminal scheduler transition. Completed records survive
server reconstruction. The projection uses the daemon session backend's
dialect, with dedicated SQLite tables or a dedicated Postgres schema. On
daemon restart, stale `running` or
`awaiting_input` records become `interrupted` with a canonical receipt; the
runtime never reports that process-bound work resumed.

## Story compilation

`internal/app` treats `application:`, exported handlers, events, typed views,
room interfaces, effect outcomes, bindings, and guards as one finite program.
The loader validates references and totality before a session starts. The
program graph records workflow nodes, reads/writes/effects, application
semantics, handler/event edges, and provenance for impact and ownership queries.

Imports fold application fragments under the existing story ownership rules.
Child pages, navigation, data, components, actions, schemas, and tokens remain
private unless named under `exports.application`; exported data page scopes and
binding keys are rebased with imported page aliases, and exported actions cannot
target private intents. The root may make explicit whole-member replacements under
`overrides.application`; replacements retain compatibility and semantic alias
checks. Stories without an application contract receive a deterministic
generated application projection from their typed views.

Versioned `application-component-package/v1` manifests can supply selected
components, schemas, tokens, room templates, intents, agents, toolboxes,
providers, and host interfaces without importing a room graph. Selection is
resolved through the project kit repository and the same `.kitsoki/kits.lock`
pin used by kits; an unpinned version, digest mismatch, collision, or missing
portable fallback fails loading.

Lock-selected component members may additionally declare a `props_schema`,
finite emitted-event payload schemas, and a portable fallback `value_prop`.
Story cards bind those members through structured `literal|data|frame|route`
prop sources and named event-to-action mappings. The compiler resolves every
non-event source from explicit inputs, validates the complete props object, and
serializes the result into the canonical frame. It never carries an expression,
module path, world snapshot, or ambient route object into a renderer.

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

For typed package components, Vue listeners are synthesized only for events
declared by the locked package member and bound by the story card. An emitted
payload must satisfy the package's portable schema before its structured input
paths are read. Invalid payloads fail closed on the frame error surface; valid
mapped inputs still pass through the server-side action JSON Schema validator.

Built-in frame elements render without story code. A component element resolves
its name through `ApplicationComponentRegistry`. The registered Vue component
receives schema-owned props, the frame, and its element descriptor. Legacy
components retain the direct dispatcher prop; typed package components dispatch
only through their declared emitted-event listeners. If no component exists,
the registry fallback is tried, followed by the frame component descriptor's
finite fallback. Unknown elements without a fallback produce a visible error.

The accepted fallback vocabulary is implemented on both web and TUI:
`prose`, `form`, `list`, `table`, `artifact`, `status`, `heading`, `code`,
`template`, `kv`, `banner`, `choice`, and `media`. A package may name
`fallback.value_prop` to project one fully resolved prop as the portable value,
which keeps rich list/table components useful outside their native web renderer.

The dispatcher returns the canonical outcome envelope to the caller while the
surface still applies its refreshed frame and the renderer still owns local
pending/error state. Components can therefore consume declared outcomes,
outputs, receipts, and refreshed frames without bypassing the application
handler boundary.

The TypeScript wire types in `tools/runstatus/src/application/types.ts` mirror
the Go JSON field names. In particular, provenance lives under
`semantic.source`, component data remains JSON under `props`, element content
remains JSON under `value`, and action envelopes retain `frame_revision`.
Allowlisted world values live only under `frame.data.<name>.value` with their
declared `sensitivity` and applied `policy`.

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

## Application-scoped maintenance

Long-running Story Applications can opt into three daemon-owned maintenance
providers. Their story-facing operations all accept exactly `{}`; application
identity, durable stores, worker registry, health pool, bounds, and remediation
policy come from daemon configuration:

```yaml
application_maintenance:
  portfolio-application:
    session_reconciliation:
      max_sessions: 200
      max_jobs: 1000
    worker_fleet:
      max_workers: 200
      max_bytes: 262144
    campaign_supervision:
      max_campaigns: 200
      max_bytes: 262144
      remediation:
        mode: propose
        max_proposals: 50
        statuses: [failed, interrupted]
```

`host.session_reconciliation.reconcile` joins the configured application to its
durable sessions, then asks the typed jobs store to interrupt only non-terminal
SQLite rows whose recorded local process owner is absent. Postgres ownership
may span hosts, so those rows are reported as deferred until a host-independent
lease exists; local PID guesses never overwrite cross-host truth. The receipt
is `kitsoki/session-reconciliation-receipt/v1` and exposes aggregate counts
only.

`host.worker_fleet.reconcile` is observe-only. Its
`kitsoki/worker-fleet-receipt/v1` projection contains worker semantic refs,
placement, health, enabled state, bounded job counts, and declared capability
classes. Its type cannot carry endpoints, tunnels, credentials, URLs, poll
errors, or transport configuration.

`host.campaign_supervision.reconcile` evaluates the configured application's
durable campaign schedules and latest scheduler outcomes. The only supported
remediation mode is `propose`: a bounded
`kitsoki/campaign-supervision-receipt/v1` names typed issues and
recommendations without executing commands, changing definitions, replaying
ticks, or exposing stored error text. A separate existing typed service must
own any later mutation.

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

### Assurance Providers

Applications opt into deterministic compliance and flow proof through
`story_application_assurance.<application-id>`. Both story-facing operations
accept only the semantic catalog identity `{node_id}`. Repository roots,
catalog and suite paths, authenticated actor, deterministic runner, durable
SQLite/Postgres stores, application identity, and all execution bounds are
server-owned. The canonical configuration and operational guarantees are
documented in [Flow Evidence Host](flow-evidence-host.md) and
[`host.compliance.run`](hosts.md#hostcompliancerun).
