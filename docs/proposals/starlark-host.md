# Runtime: Starlark Host Capability

**Status:** Draft v2. Nothing implemented yet.
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

A story bundles a Starlark module; **each `def` in it becomes a first-class host
call** addressed as `host.star.<fn>`. There is no `run` verb and no script-path
argument — the function *is* the call, its parameters *are* the `with:` block,
and its return dict *is* the bound `Result.Data`. The handler runs each function
in an embedded, sandboxed Starlark interpreter, recording inputs, outputs, and
any HTTP exchange in the trace, extending the deterministic replay boundary to
cover arbitrary scripted logic.

Every function declares a **typed contract** — a determinism level plus input
and output types — in a module-level `CONTRACT` dict. The host enforces both:

- The **determinism level** (`pure` / `query` / `mutate` / `side-effect`) is
  enforced by injecting only the builtins that level permits — a `pure` function
  that references `http` fails to compile. It is the host-side generalization of
  the oracle "blast radius" ladder (`docs/architecture/hosts.md` §"Oracle verb
  summary") and of host `external_side_effect`: it lets a reader trust a
  function's determinism without reading its body.
- The **types** are validated at the boundary in both directions: every resolved
  `with:` input is checked against its declared type before the function runs,
  and the returned dict is checked against the declared output shape before it
  binds to world. Type errors surface as the function's `Result.Error` /
  `on_error:` arc, never as a silent coercion.

## Impact

- **Code seams:** new package `internal/host/starlark`; module discovery wired
  into `internal/app/loader.go`; per-function registration into the host
  registry; HTTP client plumbed through the same cassette transport oracle
  calls use
- **Vocabulary:** a new `host.star.*` namespace and a top-level `starlark:`
  app-manifest block (see table)
- **Stories affected:** none — purely additive
- **Backward compat:** no existing stories change; opt-in per story

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| app block | `starlark:` | `module: path` | Declares the `.star` module (file or dir) the app bundles. Discovered + compiled at load. |
| host call | `host.star.<fn>` | `with: {param: value, …}` → `Result.Data = <return dict>` | One call per exported `def`. `with:` keys bind to function parameters by name; the returned dict becomes the bound data. |
| world key | _(operator-defined via `bind:`)_ | any | return-dict keys bound by name, same as any other host call |

`host.star.<fn>` resolves through the registry's existing longest-prefix
fallback (`docs/architecture/hosts.md` §"Registry dispatch"): the loader
registers one carrier under `host.star` per app and the `<fn>` suffix arrives as
`args["op"]`, **or** registers each discovered function explicitly so it is named
individually in the `hosts:` allow-list and the trace. Default is explicit
per-function registration — it keeps the allow-list and the trace auditable at
function granularity. Declaring the bare prefix `host.star` in `hosts:` is the
deliberate coarse-grain opt-out (allows every function in the module).

### Story-side usage

```yaml
# app.yaml
starlark:
  module: scripts/        # file or directory; compiled + frozen at load

hosts:
  - host.star.enrich_user

# in a room effect
- invoke: host.star.enrich_user
  with:
    user_id: "{{ world.user_id }}"   # with: keys ARE the function params
  bind:
    enriched_user: enriched_user     # return-dict keys ARE Result.Data
  once: true
```

### Module contract

No sidecar file. A single module-level `CONTRACT` dict is the authoritative,
co-located declaration for every exported function: its determinism `level`, its
typed `inputs`, and its typed `output`. Functions whose names begin with `_` are
private helpers and are neither registered nor required to appear in `CONTRACT`.

`scripts/enrich.star`:
```python
CONTRACT = {
    "enrich_user": {
        "level": "query",                                  # default "pure" if omitted
        "inputs": {
            "user_id": {"type": "string", "required": True},
        },
        "output": {
            "enriched_user": {"type": "object"},
        },
    },
}

def enrich_user(user_id):
    # `world`, `http`, `secret` are injected per the level — see below.
    resp = http.get(
        "https://api.example.com/users/" + user_id,
        headers = {"Authorization": secret("EXAMPLE_TOKEN")},
    )
    return {"enriched_user": resp.json()}
```

- **Names + arity** come from the signature (`*starlark.Function.NumParams()` /
  `Param(i)`). The loader cross-checks that `CONTRACT.inputs` keys match the
  function's parameters exactly — a typed input with no matching param, or a
  param with no type, fails the load. `required` defaults from the signature (a
  param with no Starlark default is required) and `CONTRACT` may not contradict
  it.
- **Input types** are validated at runtime, after `with:` templates resolve and
  before the function runs. A `with:` key that is missing-but-required, unknown,
  or the wrong type → error before any code executes.
- **Output types** are validated against `CONTRACT.output` after the function
  returns and before binding. The function must return a dict; a non-dict return,
  a missing required output key, or a wrong-typed value → error.

**Type vocabulary (v1):** `string`, `int`, `float`, `bool`, `list`, `object`,
`any`. Containers may narrow one level: `{"type": "list", "items": {"type":
"string"}}` and `{"type": "object", "fields": {...}}`. `any` opts a value out of
checking explicitly (never by omission — every declared input/output key needs a
`type`). The type vocabulary maps 1:1 onto Starlark/JSON values, so validation is
a structural walk with no coercion.

## Determinism levels

The level a function declares controls **which builtins the host injects** — and
nothing outside that set is in scope, so the Starlark resolver rejects any
reference to a builtin the level didn't grant *at compile time*. Enforcement is
structural, not advisory: this is what makes the level trustworthy.

| Level | `world` (read) | `http` | `secret` | Meaning | Replay |
|---|---|---|---|---|---|
| `pure` | – | – | – | Output is a function of declared inputs + deterministic stdlib (`json`, `math`, `re`). | Nothing to record. |
| `query` | ✓ snapshot | `get`, `head` | ✓ | Reads world + external state; idempotent; changes nothing. | Cassette replays the reads. |
| `mutate` | ✓ snapshot | all verbs | ✓ | May change external systems via HTTP writes; writes are repeatable/idempotent. | Cassette replays; live writes re-fire. |
| `side-effect` | ✓ snapshot | all verbs | ✓ | Non-idempotent or irreversible effects (sends, charges). | Under a cassette, **never** hits the network — strict replay only. |

The ladder mirrors the oracle blast-radius ordering and host
`external_side_effect`. A function never *writes* world directly under any level
— outputs flow only through the return value + `bind:` (the module globals are
frozen after compile; see below). `world` is always a read-only snapshot.

Default level is `pure`: the safest, most-restrictive rung. Authors opt up
explicitly via `CONTRACT[<fn>].level`. The level is recorded in the trace event
so a reviewer can audit blast radius without reading the function.

## The model

```
on_enter effect
  host.star.enrich_user(with: {user_id})
    │
    ├─▶ [resolve fn + CONTRACT]         ← load-time: discovered & compiled once, frozen
    ├─▶ [validate inputs vs types]      ← runtime: after with: resolves, before Call
    ├─▶ [set thread-locals for level]   ← world snapshot / http client / secrets
    ├─▶ [Starlark Call(thread, fn)]     ← deterministic sandbox; step-budgeted
    │       inputs   — typed, from with:
    │       world    — read-only snapshot   (query+)
    │       http     — cassette-backed       (query: GET/HEAD · mutate+: all)
    │       secret   — context secrets        (query+)
    ├─▶ [validate output vs types]      ← non-dict / missing / wrong-typed → error
    └─▶ bind Result.Data → world        ← deterministic
```

All interpretive decisions remain with oracle calls; `host.star.*` is
deterministic — same inputs + same cassette responses always produce the same
outputs.

## `ctx` is thread-locals, not an object

There is no `ctx` parameter. The environment is exposed as **predeclared
builtins backed by the running thread's locals**, so a function's data
parameters (from `with:`) stay clean and the per-call environment can be injected
without threading an object through every signature:

```python
# query+ : read-only world snapshot
world.get("key")          # None if absent

# query : read-only HTTP
resp = http.get(url, headers = {})
resp = http.head(url, headers = {})
# mutate+ : write HTTP
resp = http.post(url, body = {}, headers = {})
resp = http.put(url, body = {}, headers = {})
resp = http.patch(url, body = {}, headers = {})
resp = http.delete(url, headers = {})
resp.status               # int
resp.headers              # dict
resp.text()               # string body
resp.json()               # parsed object (fails if not JSON)

# query+ : secret lookup (redacted in trace)
secret("NAME")            # value from context secrets; error if absent
```

Mechanically: the module is compiled and its globals **frozen once at load**
(`starlark.ExecFile`); each invoke runs `starlark.Call` on a fresh `*Thread`
whose `Local("world"|"httpclient"|"secrets")` carry that call's environment. The
`world` / `http` / `secret` builtins are stateless shims that read the current
thread's locals — so concurrent invokes are safe and need no recompile. Builtins
not permitted by the declared level are simply absent from the predeclared
dict, so the resolver fails compilation if a function references them.

No filesystem, no subprocesses, no environment variables, no `time`/`random`
(non-deterministic). `http` and `secret` are the only I/O surfaces, gated by
level.

## HTTP cassette integration

`http` is backed by the same transport abstraction oracle HTTP calls use. In
flow tests, the cassette intercepts all `http` calls by URL pattern. In live
runs the actual response is recorded so replays are hermetic. Cassette entries
are keyed by `starlark:{fn}:{method}:{url}`. `side-effect` calls under a cassette
never reach the network — a missing entry is a hard error, not a passthrough.

`secret(...)` values are resolved from the context secrets map and are
**redacted** wherever a header carrying them is summarized in the trace.

## Decision recording

`host.star.<fn>` emits a `HostReturned` event (existing type) with:
- `call`: `host.star.<fn>` — the function name is in the trace natively
- `level`: the declared determinism level
- `inputs`: validated input map
- `outputs`: returned dict (or error envelope)
- `http_exchanges`: list of `{method, url, status}` summaries (bodies omitted;
  secret-bearing headers redacted)

HTTP bodies are not recorded in the trace by default (large/secret) but live in
the cassette file for test replay.

## Engine seams & invariants

**Load-time (`internal/app/loader.go`):**
- Read the `starlark:` block; compile the module (file or dir) once via
  `starlark.ExecFile`; freeze globals. A compile/resolve error fails the load.
- Enumerate exported `def`s (skip `_`-prefixed); read `CONTRACT` (level default
  `pure`). Validate each function's `CONTRACT` entry is well-formed:
  `inputs` keys match the signature's params exactly, every declared input/output
  key has a known `type`, and `required` does not contradict the signature.
- Register each as `host.star.<fn>` (or one `host.star` carrier) and verify every
  `invoke: host.star.*` resolves to a real function whose `with:` keys match its
  declared inputs. Unknown function or input mismatch → clear load error.
- Validate `module:` path does not escape the app root.

**Runtime (`internal/host/starlark/`):**
- `handler.go` — embed `go.starlark.net/starlark`; per invoke, build a `*Thread`,
  set locals per the function's level, `starlark.Call(thread, fn, …)`, convert the
  return dict to `Result.Data`.
- `builtins.go` — `world` / `http` / `secret` shims reading thread-locals; the
  predeclared set is **selected by level** so the resolver enforces it.
- `http.go` — `http` backed by the cassette transport from `internal/cassette`.
- `types.go` — the v1 type vocabulary + a structural validator used for both
  input (pre-Call) and output (post-Call) checking; no coercion.

**Invariants:**
1. **Determinism enforced by injection.** A function gets exactly the builtins
   its level grants; the resolver rejects any other reference at compile time.
2. **Execution budget.** Every `Call` runs under `thread.SetMaxExecutionSteps`
   and `thread.Cancel` wired to the effect's context deadline. Recursion stays
   off (`resolve.AllowRecursion` default).
3. **Frozen, compiled once.** Module globals are frozen after load; per-call
   state lives only in thread-locals. Safe for concurrent invokes.
4. **Error mapping.** Starlark `fail()` or an uncaught error → a Go `error`
   (infra failure; `on_error:` arc fires). A returned `{"error": "..."}` →
   `Result.Error` (expected domain error). Mirrors the `Handler` contract.
5. **Secret redaction.** `secret()` values never appear in trace events or
   bound world; only the lookup name may.
6. **Side-effect replay safety.** `side-effect` calls under a cassette never hit
   the network; a cassette miss is a hard error.
7. **Typed boundary.** Inputs are validated against `CONTRACT.inputs` before the
   function runs and outputs against `CONTRACT.output` before binding; a mismatch
   is an error, never a silent coercion. Every declared key carries a `type`;
   `any` is the only opt-out and must be explicit.

## Backward compatibility / migration

Purely additive. Existing stories and cassettes are unaffected. The capability
is opt-in per app via the `starlark:` block.

## Tasks

```
## 1. Module discovery & contract
- [ ] 1.1 `starlark:` app-manifest block + loader parse (file or dir module)
- [ ] 1.2 Compile + freeze module at load; enumerate exported defs; read CONTRACT (level default pure)
- [ ] 1.3 Validate CONTRACT well-formedness: inputs keys == params, every key has a known type, required ⊆ signature
- [ ] 1.4 Register host.star.<fn> per function (explicit) + check each invoke's with: keys vs declared inputs
- [ ] 1.5 Clear load errors: unknown fn, input mismatch, untyped key, compile error, path traversal

## 2. Starlark sandbox
- [ ] 2.1 `internal/host/starlark/handler.go` — embed go.starlark.net; per-call Thread + Call(fn)
- [ ] 2.2 `builtins.go` — world/http/secret shims over thread-locals; predeclared set SELECTED BY LEVEL
- [ ] 2.3 `http.go` — http builtin backed by cassette transport; GET/HEAD vs write-verb gating by level
- [ ] 2.4 `types.go` — v1 type vocabulary + structural validator (no coercion), shared by input & output checks
- [ ] 2.5 Input validation (pre-Call) + output validation (post-Call, dict required) → Result.Error on mismatch
- [ ] 2.6 Execution budget (max steps) + context-cancel wiring; recursion off

## 3. Trace, cassette & enforcement
- [ ] 3.1 HostReturned fields: call=host.star.<fn>, level, inputs, outputs, http_exchanges
- [ ] 3.2 Cassette key `starlark:{fn}:{method}:{url}`; intercept in flow tests
- [ ] 3.3 Secret redaction in http_exchanges + bound world
- [ ] 3.4 side-effect strict replay (cassette miss = hard error)
- [ ] 3.5 Opt-in live test: hit a public API, record cassette, replay deterministically

## 4. Verification
- [ ] 4.1 Unit: a pure fn referencing `http` fails to COMPILE (level enforcement)
- [ ] 4.2 Unit: error mapping — fail() → Go error; {"error":…} → Result.Error
- [ ] 4.3 Unit: wrong-typed input rejected pre-Call; wrong-typed/missing output rejected pre-bind
- [ ] 4.4 Unit: CONTRACT well-formedness — untyped key / input-param mismatch fails at LOAD
- [ ] 4.5 Flow fixture: query fn with cassette-backed GET binds typed output to world
- [ ] 4.6 Flow fixture: unknown fn / input mismatch fails at LOAD, not runtime
- [ ] 4.7 Flow fixture: HTTP error (non-2xx) surfaces via on_error: arc
- [ ] 4.8 Unit: step budget aborts a runaway loop

## 5. Adopt + document
- [ ] 5.1 Add one example module + leveled fns to a demo story (dev-story or bugfix)
- [ ] 5.2 Document host.star.* in `docs/architecture/hosts.md` (cheat sheet + section)
- [ ] 5.3 Document the determinism-level ladder in hosts.md alongside the oracle
        blast-radius table (single authoritative location; link from here)
- [ ] 5.4 Document `starlark:` block in `kitsoki docs app-schema` + state-machine doc
- [ ] 5.5 Trim/delete this proposal
```

## Verification

Flow fixtures cover the happy path (cassette-backed GET → typed output bound to
world), the error path (HTTP failure → `on_error:` arc), and the load-time
invariants (unknown function / input mismatch / untyped key → load error). The
teeth are two unit assertions: a `pure` function referencing `http` fails to
compile (level enforcement), and a wrong-typed input/output is rejected at the
boundary rather than silently coerced (type enforcement). No LLM required. The
opt-in live test (3.5) is explicitly gated and not run by default.

## Open questions

1. **HTTP body recording** — summaries in the trace, full bodies only in the
   cassette file. *Lean: confirmed — summaries only; redact secret headers.*

2. **Starlark stdlib** — allow `json`, `re`, `math` from `go.starlark.net/lib`.
   *Lean: yes — deterministic only; explicitly exclude `time`.*

3. **Type vocabulary depth** — v1 ships scalars + one-level container narrowing
   (`items` / `fields`). Deeper recursive schemas, enums, and unions are
   deferred. *Lean: confirmed — one level is enough for HTTP/JSON wrangling; revisit
   if a real story needs more.*

4. **Carrier vs per-function registration** — register each `host.star.<fn>`
   explicitly (auditable allow-list + trace), with bare-`host.star` as a
   coarse-grain opt-out. *Lean: confirmed — explicit by default.*

## Non-goals

- General-purpose plugin system (Python, WASM, etc.) — Starlark is the escape
  hatch, not a general extension point.
- LLM calls from within Starlark — that's what `host.oracle.*` is for.
- Mutable world access from builtins — outputs flow through the return value only
  (frozen globals enforce this structurally).
- Starlark-defined rooms replacing YAML — these are effects, not a room type.
- Filesystem / subprocess / env access from the sandbox.
