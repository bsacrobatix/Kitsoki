# Runtime: Local small-model oracle (llama.cpp sidecar)

**Status:** Draft v3 (adversarial review applied: grammar claim softened to
best-effort + fail-open, validation-reject fallback and load-time schema-subset
check added, model sizes corrected; binary/weight acquisition settled as
zero-touch fetch-on-first-use — no manual install). Nothing implemented yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

Every interpretive decision in kitsoki today routes through the local
`claude` CLI via the `oracle.Oracle.Ask` interface — `oracle.decide`
verdicts and the `ask`/`extract`/`task` verbs (`extract`'s LLM tier,
`internal/host/oracle_extract.go:71` `resolvedByLLM`). That is the right
default — it's high quality and already the injected `oracle.claude` plugin
(`docs/architecture/oracle-plugin.md:49`).

Note what is *not* on the oracle path: the **semantic router**
(`orchestrator.TrySemantic`, `internal/orchestrator/semantic.go:196`) is
purely deterministic — it runs only the synonyms / slot_template tiers via
`RunExtractForRouting` (`internal/host/oracle_extract_matcher.go:89`) and on
a miss *falls through to the main-turn LLM*, not to an oracle. So "use a
local model for routing" is a deliberate new wiring (see §"The model"), not
a flip of an existing tier.

The `claude` default has three costs that bite the small, high-frequency
decisions:

- **Online + rate-limited.** Routing and gate deciders fire on nearly
  every turn. They should work on a plane, in a CI box with no network,
  and without consuming a Claude session.
- **No structural guarantee.** The `claude` path returns free text that we
  re-validate against the call's JSON Schema after the fact
  (`internal/oracle/validate.go`, `ValidateSubmission`) and retry on
  failure. For a verdict that is *only* `{verdict, intent, reason,
  confidence}` (`stories/pr-refinement/schemas/judge_verdict.json`), a
  full agent turn plus a validate-and-retry loop is heavy.
- **Latency.** A subprocess agent turn is seconds; a 1.5B model
  constrained to a tiny schema is far less.

A small local model can't replace Claude for the hard reasoning verbs, but
for **routing** and **schema-bounded `decide`** it is a better tool: cheap,
offline, and — with grammar-constrained decoding — strongly biased toward
schema-valid JSON on the first attempt, which collapses the retry loop *for
schemas inside llama.cpp's supported grammar subset* (flat objects, scalar
fields, enums, simple arrays — `judge_verdict.json` is comfortably inside
it). This is a *best-effort* structural aid, not an absolute guarantee:
llama.cpp **fails open** (logs the grammar error, generates unconstrained,
returns HTTP 200) when a schema can't be translated, and its
json-schema-to-grammar omits `$ref`/`$defs`, `uniqueItems`, `not`,
`if/then/else`, and `anyOf` alongside sibling properties. So
`ValidateSubmission` (below) is not belt-and-suspenders — it is the real
guarantee, and the supported subset is a constraint we enforce, not a
freebie. See §"Engine seams & invariants" for the fail-open fallback and
the load-time subset check.

## What changes

Add a new oracle plugin type that speaks **OpenAI-compatible HTTP** to a
**local `llama-server` sidecar** (llama.cpp). It is one backend behind the
single `oracle.Oracle.Ask` interface, so the *same* plugin serves whichever
verb opts in — `decide` (gate verdicts) and `extract`'s LLM tier are the
same `Ask` call differing only by `Verb` + `SchemaJSON`. One
sentence: *kitsoki gains a `builtin.local_llm` oracle that POSTs the
rendered prompt + the call's JSON Schema to a managed llama.cpp server and
gets back grammar-constrained JSON, validated by `ValidateSubmission`
before it counts as a verdict.*

The runtime already has the seam. `oracle.BuildRegistry`
(`internal/oracle/build_registry.go:79`) switches on `plugin:` to construct
an `Oracle`; we add one `case "builtin.local_llm"`. The new `Oracle`
implementation reuses the exact contract everything else honours —
`Ask(ctx, AskRequest) (AskResponse, error)` with kitsoki validating
`Submission` against `SchemaJSON` afterwards (`internal/oracle/oracle.go:50`).

Runtime decision (settled with the user): **one runtime everywhere** —
llama.cpp with Metal on Apple Silicon, CPU on Linux VMs. This keeps
**uniform GBNF / JSON-schema-to-grammar enforcement on both platforms**
(see Non-goals for why not MLX).

## Reuse: what's already there vs. net-new

Almost all of the plumbing exists. The dispatch seam is shared by all five
verbs and already does per-call plugin selection *and* the "fall back only
when necessary" behavior:

- Every verb handler (`ask`/`decide`/`extract`/`task`/`converse`) calls
  `TryDispatchVerb` (`internal/host/oracle_decide.go:109`,
  `oracle_extract.go:216`). It resolves the per-call plugin, calls
  `plug.Ask`, validates the submission against `SchemaJSON`, writes the
  `oracle.call.*` events, and returns.
- When no plugin is named for the call, `TryDispatchVerb` returns
  `handled=false` and the handler **falls through to the legacy direct
  `claude_cli` path** (`internal/host/oracle_dispatch.go:194`). That
  fallthrough *is* the "keep the existing structure, fall back to the
  oracle interface when necessary" requirement — already built.
- Plugin targeting is **per-effect, not global**: an effect's `oracle:`
  field → `hc.OraclePlugin` → injected for that one call via
  `WithOraclePluginName` (`internal/orchestrator/host_dispatch.go:157`);
  `Registry.Resolve` returns that alias, defaulting to `oracle.claude`
  (`internal/oracle/registry.go:48`). The registry holds *all* declared
  `oracle_plugins:` at once, so different rooms/effects each target a
  different plugin simultaneously. There is no single global oracle.

So the **net-new work is small**:

1. One `case "builtin.local_llm"` in `BuildRegistry` (the backend). Every
   verb can then target it with zero handler changes — `decide` included.
2. **The router is the only special case.** `TrySemantic` →
   `RunExtractForRouting` runs only the deterministic tiers and returns on
   `no_match`; it never reaches the `extract` LLM tier. The change is to
   invoke that LLM tier on `no_match` (it already dispatches through the
   registry with per-call plugin selection) instead of falling straight
   through to the main-turn LLM. Wiring, not new infrastructure, and the
   deterministic tiers stay first.
3. The sidecar lifecycle + HTTP client + grammar mapping (genuinely new).

## Impact

- **Code seams:**
  - `internal/oracle/build_registry.go:89` — new `case "builtin.local_llm"`.
  - `internal/oracle/local_llm.go` *(new)* — the `Oracle` impl: an
    OpenAI-compatible `/v1/chat/completions` client that sends
    `response_format: {type: "json_schema", json_schema: …}` derived from
    `AskRequest.SchemaJSON`.
  - `internal/oracle/server/` *(new)* — sidecar lifecycle: ensure the
    pinned `llama-server` binary + model weights are present in the cache
    dir (fetch-on-first-use + checksum verify if absent — Open Questions
    1–2), start on first `Ask`, health-check, stop on `Close()`. Mirrors the
    lazy-spawn pattern in `internal/oracle/subprocess.go:244` (`spawn`).
  - `internal/host/oracle_extract.go:71` — the `extract` verb's LLM tier
    (`resolvedByLLM`) can name `oracle.local` instead of `oracle.claude`;
    optionally `orchestrator.TrySemantic` invokes it on `no_match`
    (`semantic.go:239`, the `return nil, false, nil`) rather than falling
    straight through to the main-turn LLM.
- **Vocabulary:** one new `oracle_plugins:` plugin type (table below). No
  new effects, host verbs, or world keys — the `host.oracle.*` verbs and
  the `oracle:` alias field are unchanged.
- **Stories affected:** none by default. `oracle.claude` stays the injected
  default; stories opt in per call via `oracle: oracle.local`.
- **Backward compat:** fully additive and **opt-in**. Existing stories and
  cassettes are untouched.
- **Docs on ship:** `docs/architecture/oracle-plugin.md` (new plugin row +
  § "Local model backend"), `docs/architecture/semantic-routing.md` (LLM
  tier backend choice), and the build/setup notes for the sidecar.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| oracle plugin | `builtin.local_llm` | `model:`, `endpoint:`/`port:`, `server_bin:`, `grammar: true\|false` | Constructed in `BuildRegistry`; starts/owns a llama-server sidecar and talks OpenAI HTTP. |

```yaml
oracle_plugins:
  oracle.local:
    plugin: builtin.local_llm
    model: qwen2.5-1.5b-instruct-q4_k_m.gguf   # resolved under a models dir
    grammar: true                              # force schema → GBNF
    # endpoint: http://127.0.0.1:8080/v1       # optional: attach to an external server
```

## The model

```
turn input ─▶ semantic router: synonyms/slot_template (deterministic, unchanged)
                 │ no_match
                 ▼
          NEW: extract LLM tier via oracle.local ──▶ Verdict{intent,slots,confidence}
                 (host.oracle.extract, Verb:"extract", schema = allowed intents+slots)

room gate  ─▶ host.oracle.decide(oracle.local) ─▶ POST /v1/chat/completions
                 (prompt + response_format:json_schema from SchemaJSON)
                 ◀─ grammar-constrained JSON ─▶ ValidateSubmission ─▶ Submission
                                                      │ reject (fail-open or
                                                      │ out-of-subset schema)
                                                      ▼
                                                 AskError → handler falls
                                                 back to oracle.claude
```

The router itself stays deterministic. The new piece is letting the
**`extract` verb's LLM tier** resolve via `oracle.local` (and, optionally,
having `TrySemantic` invoke that tier on `no_match` instead of falling
straight through to the main-turn LLM — the no-match fall-through is the
`return nil, false, nil` at `internal/orchestrator/semantic.go:239`). Both
arrows above are the *same* `oracle.Oracle.Ask` backend.

What is **interpretive** (recorded): the model's verdict/extraction — same
status as a `claude` verdict, written through the existing oracle trace
events. What is **deterministic** (replayable): the grammar that constrains
the token stream, the schema validation, and the routing confidence
mapping. The moat holds — this is a new *pluggable backend behind an
existing decision point*, not a new decision point, and every call is
recorded exactly as oracle calls already are.

**Models (commercial-clean first):**

| Role | Model | 4-bit GGUF | License |
|---|---|---|---|
| Default routing + small `decide` | **Qwen2.5-1.5B-Instruct** | ~1.12 GB (Q4_K_M) | Apache-2.0 |
| Stronger decider (optional) | **Phi-3.5-mini-instruct** | ~2.39 GB | MIT |
| Alt 3B | Llama-3.2-3B-Instruct | ~2.02 GB (Q4_K_M) | Llama 3.2 community |

Note: Qwen2.5 launched Apache-2.0 **except** the 3B and 72B sizes (separate
Qwen license) — so the 1.5B is the clean default and Phi-3.5-mini (MIT) the
clean step-up. (The 3B was *later* relicensed to Apache-2.0, but the 1.5B
stays the default regardless.) **NuExtract-2.0 is explicitly not the pick** for these jobs:
it is a multimodal Qwen-VL model trained only for template-driven document
extraction, with no routing/intent task, and its mid 4B is non-commercial
(only 2B/8B are MIT). Reserve it for a future genuine document-extraction
verb, not routing/decide.

## Decision recording

No new event type. A local-model call is an oracle call and rides the
existing `oracle.call.*` trace machinery; `AskResponse.Meta`
(`internal/oracle/oracle.go`, `map[string]any`) carries `{model,
prompt_tokens, completion_tokens, grammar}` where `grammar` records whether
the constraint was *actually* applied (false on a fail-open response), so a
run is reconstructable and the backend that produced a verdict — and
whether it was grammar-constrained — is auditable in runstatus. The
grammar/schema used is deterministic and derivable from the call's
`SchemaJSON`, so it need not be stored per-call beyond the schema already
recorded.

## Engine seams & invariants

- **Registry:** add the `case` in `BuildRegistry`
  (`internal/oracle/build_registry.go:89`). Load-time invariant: a
  `builtin.local_llm` decl with neither a resolvable `model:` nor an
  external `endpoint:` fails fast at story load with a clear message
  (mirrors the existing `subprocess` "missing command" check at
  `build_registry.go:110`).
- **Sidecar lifecycle:** lazy-start on first `Ask`, like
  `SubprocessOracle.spawn` (`internal/oracle/subprocess.go:244`); one
  server per model shared across calls; `Close()` terminates it with the
  same `SubprocessTerminateTimeout` grace already defined in
  `oracle.go`. If `endpoint:` is set, attach to an already-running server
  and never spawn/kill. **Concurrency:** `llama-server` serves a single
  decode slot sequentially by default, so concurrent oracle calls (routing
  fires on nearly every turn) serialize and their latencies stack. Launch
  with `--parallel N` and document the chosen slot count; an over-subscribed
  single slot is a likely source of the latency the spike (0.2) must measure.
- **Grammar (best-effort, not a guarantee):** when `grammar: true`,
  translate `SchemaJSON` to the server's `json_schema` response-format
  (llama.cpp's json-schema-to-grammar). Two failure modes the design must
  handle, *not* assume away:
  - **Fail-open.** If llama.cpp can't parse the grammar it logs the error,
    generates **unconstrained**, and returns **HTTP 200** — there is no
    server-side rejection. `ValidateSubmission` is therefore the *only*
    structural guarantee, not a backstop.
  - **Out-of-subset schemas.** json-schema-to-grammar omits `$ref`/`$defs`
    (effectively broken — can silently exceed repetition limits),
    `uniqueItems`, `not`, `if/then/else`/`dependentSchemas`, `contains`, and
    `anyOf`/`oneOf` alongside sibling properties; `pattern` only works
    anchored `^…$`. **Load-time subset check:** a schema targeted at an
    `builtin.local_llm` plugin that uses an unsupported construct fails fast
    at story load with a clear message (same fail-fast spirit as the
    missing-`model:` invariant), so authors never hit a silent fail-open at
    runtime. `judge_verdict.json` is well inside the subset.
- **Validation-reject fallback:** when `ValidateSubmission` rejects a
  local-model `Submission` (fail-open output, or a schema the grammar
  couldn't fully express), the call returns an `AskError` and the verb
  handler **falls back to `oracle.claude`** for that one call rather than
  retrying the local model (which would reproduce the same failure). The
  fallback and its reason ride the existing `oracle.call.*` trace so the
  substitution is auditable. Retrying the *same* local backend is pointless
  for a deterministic grammar failure, so the collapse-the-retry-loop claim
  holds only inside the supported subset; outside it, the cost is one
  `claude` call, not a retry storm.

## Backward compatibility / migration

Default-off. `oracle.claude` (`builtin.claude_cli`) stays the injected
default and the backend for every existing call. No story, cassette, or
flow fixture changes unless it explicitly adds `oracle: oracle.local` to a
call. The sidecar binary and model weights are **not committed** (see
memory: no committed build artifacts) and **not bundled** into the release
— they are fetched-on-first-use into a gitignored cache dir
(`~/.cache/kitsoki/{bin,models}/`), checksum-verified against version+sha
pins baked into the build, so the user installs/manages nothing by hand
(see Open Questions 1–2). A `make fetch-models` / `fetch-llama-server`
pre-warm alias runs the same downloader ahead of time for offline/CI use.
The repo embeds only a `.gitkeep`'d models dir so `go build` still compiles.

## Tasks

```
## 0. Spike (de-risk before committing)
- [ ] 0.1 Stand up llama-server with Qwen2.5-1.5B-Instruct-Q4_K_M; confirm
          /v1/chat/completions + response_format json_schema returns
          schema-valid JSON for judge_verdict.json with zero retries
- [ ] 0.2 Measure tokens/sec + first-token latency on an M-series Mac AND a
          CPU-only Linux VM, with --parallel slot count noted (the unmeasured
          gap; record numbers in this proposal)
- [ ] 0.3 Confirm the fail-open behavior firsthand: feed llama-server a schema
          it can't translate ($ref) and verify it returns HTTP 200 with
          unconstrained output (so the fallback path below is justified)

## 1. Engine
- [ ] 1.1 internal/oracle/local_llm.go — OpenAI-compatible Oracle (chat
          completions + json_schema response_format from AskRequest.SchemaJSON);
          Meta.grammar reflects whether the constraint was actually applied
- [ ] 1.2 internal/oracle/server/ — sidecar locate/start/health/stop;
          attach-to-endpoint mode; --parallel slot config
- [ ] 1.3 BuildRegistry case "builtin.local_llm" + load-time invariant & error
- [ ] 1.4 Load-time schema-subset check: a schema using $ref/anyOf-with-siblings/
          uniqueItems/not/conditionals targeted at a local_llm plugin fails fast
          at story load with a clear message
- [ ] 1.5 Validation-reject fallback: on ValidateSubmission failure for a
          local-model call, fall back to oracle.claude for that one call and
          record the substitution + reason in the oracle.call.* trace
- [ ] 1.6 Meta{model, tokens, grammar} populated; oracle.call.* trace unchanged

## 2. Verification
- [ ] 2.1 Cassette transport: record a local-model decide/route turn so the
          conformance suite (internal/testrunner) covers it without a live model
- [ ] 2.2 Stateless probe: a decide gate resolves via oracle.local against a
          fixed cassette; ValidateSubmission exercised
- [ ] 2.3 Grammar test: malformed-schema input still fails closed (verify the
          test FAILS without the grammar/validation path — CLAUDE.md)
- [ ] 2.4 Fallback test: a fail-open / invalid local response triggers the
          oracle.claude fallback, the call still succeeds, and the trace records
          the substitution (verify it FAILS without the fallback path)

## 3. Adopt + document
- [ ] 3.1 Point the extract verb's LLM tier (oracle_extract.go resolvedByLLM)
          at oracle.local; optionally extend TrySemantic to invoke it on
          no_match instead of falling through to the main-turn LLM
- [ ] 3.2 Point one real story's decide gate (e.g. pr-refinement merge judge)
          at oracle.local behind a flag; A/B against oracle.claude
- [ ] 3.3 Fetch-on-first-use: sidecar ensures pinned llama-server binary +
          model weights are present in the cache dir (checksum-verified
          against baked version+sha pins), downloading if absent before
          spawn; first fetch logs an explicit "downloading ~N GB to <dir>"
          disclosure. Keep `make fetch-models` / `fetch-llama-server` as a
          pre-warm/offline alias over the same downloader (gitignored,
          .gitkeep). endpoint: mode skips all fetching.
- [ ] 3.4 Update oracle-plugin.md + semantic-routing.md; trim/delete this proposal
```

## Verification

A reviewer confirms it without spending Claude tokens via the **cassette
transport**: record one `decide` and one routing turn produced by the local
model, then replay them through the four-transport conformance path
(`internal/testrunner/oracle_conformance_test.go`) and a stateless
`kitsoki turn --state … --intent …` probe. The structural behavior is
tested by asserting that (a) constrained output validates with zero retries,
(b) `ValidateSubmission` still rejects a crafted bad submission, and (c) a
rejected local-model response falls back to `oracle.claude` and is recorded
as such — and that these tests **fail without** the grammar/validation/
fallback paths. A live-model end-to-end test exists but is **not run by default**
(see memory: no LLM tests by default); run it only when `local_llm.go` or
the grammar mapping changes.

## Open questions

1. **`llama-server` binary acquisition — zero-touch fetch-on-first-use.**
   Settled: the user installs/manages nothing by hand. On the first
   `oracle.local` call the sidecar layer ensures the pinned `llama-server`
   build for the host platform (mac arm64 / linux amd64) is present in the
   cache dir (`~/.cache/kitsoki/bin/`, overridable via env), downloading and
   **checksum-verifying** it against a version+sha pin baked into the build
   if absent, then spawns it. Subsequent runs and fully-offline use need no
   network. Nothing is committed (honors the no-committed-artifacts rule)
   and nothing is bundled into the release (no per-platform build matrix).
   A respected `endpoint:` still bypasses all of this. *Bundling the binary
   into the release is rejected: it adds the build matrix and weights are
   too large to bundle anyway, so it does not reach zero-touch.*
2. **Model distribution — same fetch-on-first-use path.** Weights are
   ~1.1–2.4 GB. Settled: fetched-on-first-use into a gitignored,
   `.gitkeep`'d cache dir (`~/.cache/kitsoki/models/`), checksum-verified
   against a pinned sha, shared across runs. Because the download is large,
   the **first** fetch logs an explicit "downloading <model> (~N GB) to
   <dir>" disclosure before it starts. A `make fetch-models` (and a
   `fetch-llama-server`) target is kept as a **pre-warm / offline escape
   hatch** — it runs the *same* downloader ahead of time so CI boxes and
   air-gapped machines never hit a runtime fetch — but it is no longer a
   required setup step for the common path.
3. **Throughput is unmeasured.** No tokens/sec was verified in research for
   1.5B/3B at 4-bit on M1 Pro or a CPU VM — task 0.2 must close this before
   we trust interactive-latency claims, especially CPU-only Linux.
4. **Routing verdict mapping.** How a free-form model verdict maps onto the
   `semroute.Verdict` confidence bands (0.90/0.80/0.65/0.50) — likely a
   constrained `{intent, slots, confidence}` schema with a fixed confidence
   ceiling for LLM-tier matches. Needs calibration like the Oregon Trail
   work in `semantic-routing-proposal.md`.

## Non-goals

- **MLX.** Considered and deliberately *not* used despite the appeal of
  Apple acceleration: `mlx_lm.server` is OpenAI-compatible but has **no
  native `response_format`/`json_schema`/grammar** parameter at all, so on
  Mac it can't even *attempt* constrained decoding without an extra
  Outlines-style sidecar — two runtimes, two structured-output stories.
  llama.cpp with Metal gives uniform (best-effort, fail-open) GBNF
  enforcement on both platforms for far less surface; the `ValidateSubmission`
  guarantee is then identical everywhere. (Re-open only if Mac throughput proves inadequate in 0.2 *and*
  MLX gains native constrained decoding upstream.)
- **cgo / embedded inference.** No `go-llama.cpp`-style in-binary engine;
  it breaks cross-compilation and there is no production-quality pure-Go
  GGUF runtime. The sidecar is the simpler seam.
- **Replacing `claude` for the hard verbs.** `ask`/`task`/`converse` and
  any genuinely reasoning-heavy `decide` stay on `oracle.claude`.
- **NuExtract / multimodal document extraction.** Out of scope here;
  noted above as a future, separate document-extraction verb.
```
