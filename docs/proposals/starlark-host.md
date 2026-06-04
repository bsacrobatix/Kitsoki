# Runtime: Starlark Host Capability

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

Any story logic more complex than expr-lang can express — HTTP API calls, JSON
wrangling, multi-step conditionals — currently escapes the deterministic border
into Python scripts, shell commands, or hand-rolled Go host plugins. These
are untraceable, unreplayable, and unverifiable without a live environment.

The gap forces story authors to choose between staying in expr (and contorting
logic into guarded transitions) or punting to an oracle (adding LLM cost and
non-determinism to what is really a deterministic task). Neither is right.

## What changes

A new `host.starlark.run` capability executes a bundled `.star` file in an
embedded, sandboxed Starlark interpreter. The script receives a typed `ctx`
object providing HTTP and world access; its named outputs are bound back into
world. Every input, output, and HTTP exchange is recorded in the trace,
extending the deterministic replay boundary to cover arbitrary scripted logic.

## Impact

- **Code seams:** new package `internal/host/starlark`; registered in
  `internal/host/registry.go`; HTTP client plumbed through the same
  cassette transport used by oracle calls
- **Vocabulary:** one new host call (see table)
- **Stories affected:** none — purely additive
- **Backward compat:** no existing stories change; opt-in per story

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| host call | `host.starlark.run` | `script: path, inputs: map<string,any>` → `outputs: map<string,any>` | runs the `.star` file; path is relative to story root |
| world key | _(operator-defined via `bind:`)_ | any | outputs bound by name, same as any other host call |

### Story-side usage

```yaml
- invoke: host.starlark.run
  with:
    script: scripts/enrich_user.star
    inputs:
      user_id: "{{ world.user_id }}"
  bind:
    enriched_user: outputs.enriched_user
  once: true
```

### Script contract

Each `.star` file is paired with a sidecar schema:

```
scripts/
  enrich_user.star
  enrich_user.star.yaml   ← typed I/O contract
```

`enrich_user.star.yaml`:
```yaml
inputs:
  user_id:
    type: string
    required: true
outputs:
  enriched_user:
    type: object
```

`enrich_user.star` (best-effort dict declarations mirror the sidecar):
```python
INPUTS = {"user_id": str}
OUTPUTS = {"enriched_user": dict}

def main(ctx):
    resp = ctx.http.get("https://api.example.com/users/" + ctx.inputs.user_id)
    return {"enriched_user": resp.json()}
```

The engine validates inputs against the sidecar schema at load time and
validates outputs at runtime before binding. The Starlark dict declarations
are a convention for tooling and human readers; the sidecar is authoritative.

## The model

```
on_enter effect
  host.starlark.run(script, inputs)
    │
    ├─▶ [schema validate inputs]       ← deterministic, load-time
    ├─▶ [Starlark eval main(ctx)]      ← deterministic sandbox
    │       ctx.inputs   — typed world values
    │       ctx.http     — HTTP client (cassette-intercepted)
    │       ctx.world    — read-only world snapshot
    ├─▶ [schema validate outputs]      ← deterministic, runtime
    └─▶ bind outputs → world          ← deterministic
```

All interpretive decisions remain with oracle calls; `host.starlark.run` is
entirely deterministic — the same inputs always produce the same outputs
(given the same HTTP cassette responses in tests).

## `ctx` API surface

```python
# Inputs
ctx.inputs.{name}        # typed input values declared in sidecar

# World (read-only snapshot at call time)
ctx.world.get("key")     # returns None if absent

# HTTP
resp = ctx.http.get(url, headers={})
resp = ctx.http.post(url, body={}, headers={})
resp.status              # int
resp.headers             # dict
resp.text()              # string body
resp.json()              # parsed object (fails if not JSON)
```

The `ctx.http` client is the only I/O surface. No filesystem, no subprocesses,
no environment variables — the sandbox is intentionally narrow.

## HTTP cassette integration

`ctx.http` is backed by the same transport abstraction used by oracle HTTP
calls. In flow tests, the cassette intercepts all `ctx.http` calls by URL
pattern. In live runs, the actual HTTP response is recorded in the trace
alongside inputs and outputs so replays are hermetic.

Cassette entries for Starlark HTTP calls use the same format as oracle
cassettes, keyed by `starlark:{script_path}:{url}:{method}`.

## Decision recording

`host.starlark.run` emits a `HostReturned` event (existing event type) with:
- `call`: `host.starlark.run`
- `inputs`: validated input map
- `outputs`: validated output map (or error)
- `http_exchanges`: list of `{method, url, status}` summaries

HTTP bodies are not recorded in the trace by default (potentially large/secret)
but are available in the cassette file for test replay.

## Engine seams & invariants

**Load-time:**
- `internal/app/loader.go` — verify every `host.starlark.run` effect has a
  `script:` path; verify the `.star` file and `.star.yaml` sidecar both exist
  relative to the story root; parse and validate the sidecar schema
- Fail fast with a clear error if the `.star` file references `ctx.` methods
  not in the approved surface (static analysis pass over the AST)

**Runtime:**
- `internal/host/starlark/handler.go` — embed `go.starlark.net/starlark`;
  construct the `ctx` struct; eval `main(ctx)`; validate outputs against sidecar
- `internal/host/starlark/http.go` — `ctx.http` backed by the cassette
  transport from `internal/cassette` (same package oracle calls use)

## Backward compatibility / migration

Purely additive. Existing stories and cassettes are unaffected. The capability
is opt-in per effect.

## Tasks

```
## 1. Schema & sidecar
- [ ] 1.1 Define `.star.yaml` schema struct in `internal/host/starlark/schema.go`
- [ ] 1.2 Load-time loader validation (file existence, schema parse, input/output types)
- [ ] 1.3 Clear error messages for missing sidecar, unknown ctx methods

## 2. Starlark sandbox
- [ ] 2.1 `internal/host/starlark/handler.go` — embed go.starlark.net, wire ctx, eval main(ctx)
- [ ] 2.2 `ctx.inputs`, `ctx.world` (read-only) bindings
- [ ] 2.3 `ctx.http` backed by cassette transport
- [ ] 2.4 Output validation against sidecar schema before bind

## 3. Trace & cassette
- [ ] 3.1 HostReturned event fields: inputs, outputs, http_exchanges
- [ ] 3.2 Cassette key format `starlark:{script}:{method}:{url}`; intercept in flow tests
- [ ] 3.3 Opt-in live test: download a public API, record cassette, replay deterministically

## 4. Verification
- [ ] 4.1 Unit: schema validation rejects bad inputs/outputs
- [ ] 4.2 Flow fixture: `host.starlark.run` with a cassette-backed HTTP call binds output to world
- [ ] 4.3 Flow fixture: missing sidecar fails at load, not at runtime
- [ ] 4.4 Flow fixture: HTTP error (non-2xx) surfaces as world.last_error via on_error arc

## 5. Adopt + document
- [ ] 5.1 Add one example script + sidecar to a demo story (dev-story or bugfix)
- [ ] 5.2 Document in `docs/stories/state-machine.md` §host-capabilities
- [ ] 5.3 Trim/delete this proposal
```

## Verification

Flow fixtures cover the happy path (cassette-backed HTTP → output bound to
world), the error path (HTTP failure → `on_error:` arc fires), and the
load-time invariant (missing sidecar → load error with clear message). No LLM
required. The opt-in live test in task 3.3 is explicitly gated and not run by
default.

## Open questions

1. **HTTP body recording** — record full request/response bodies in the trace
   event, or only summaries? Bodies could be large or contain secrets.
   *Lean: summaries in the trace, full bodies only in the cassette file.*

2. **Starlark stdlib** — allow `json`, `re`, `math` from `go.starlark.net/lib`?
   They're deterministic and broadly useful.
   *Lean: yes, include all deterministic stdlib modules.*

3. **ctx.world writability** — allow `ctx.world.set(key, value)` as an
   alternative to the `bind:` return path?
   *Lean: no — keep outputs explicit via return value + sidecar schema.*

4. **Script co-location enforcement** — require `.star` files to live under the
   story root, or allow a shared `scripts/` at the app level?
   *Lean: allow both; validate path traversal doesn't escape app root.*

## Non-goals

- General-purpose plugin system (Python, WASM, etc.) — Starlark is the escape
  hatch, not a general extension point.
- LLM calls from within Starlark — that's what `host.oracle.*` is for.
- Mutable world access from `ctx` — outputs flow through the return value only.
- Starlark-defined rooms replacing YAML — this is an effect, not a room type.
