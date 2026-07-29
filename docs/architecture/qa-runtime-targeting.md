# QA runtime targeting

**Status:** substrate landed (`internal/capsule/runtime/qatarget`), not yet
wired. The Python QA harness is still the only producer in the live path.

This document explains what `internal/capsule/runtime/qatarget` is for, why it
exists as Go rather than as more Python under `tools/`, and what has to happen
for it to replace the harness's current targeting. It is written to be read by
whoever next wonders why an unimported package is sitting in the tree.

## The problem

Any demo, QA proof, deck, or video that claims coverage of a product-journey
scenario must start from the universal scenario mechanism
(`tools/product-journey/scenarios.json`, `tools/product-journey/run.py`,
`stories/scenario-qa/app.yaml`) — see AGENTS.md. That mechanism is Python, and
it does two safety-critical jobs with untyped strings.

### 1. It cannot prove the thing it captured was the thing it meant to capture

`tools/product-journey/run.py` threads a bare `public_base_url` string from
argv down through the marathon → matrix → driver layers. Nothing along that
path proves the URL still belongs to a live, healthy runtime of the generation
the evidence claims.

The failure mode is quiet and expensive: a driver captures perfectly valid
frames against a stale or half-torn-down deployment, the run completes, and the
result is filed as a **product finding**. Nothing in the evidence records that
the target was wrong, so the finding looks real.

### 2. It infers the infra-vs-product split instead of knowing it

`schemas/completion-state.schema.json` already draws the line that matters. Its
`health` field is constrained to
`^(infra:[a-z0-9_-]+|model:result|incomplete)$` and separates an
**infrastructure** failure (harness/environment broke — `infra:harness`,
`infra:worker-never-ran`, `infra:stall`) from a **genuine model/product
result** (`model:result`). The schema is explicit that

> A `blocked`/`failed` verdict with `health: infra:*` must never be scored
> against a model/candidate.

That contract is only as good as its producers, and today every producer is
Python inferring the distinction after the fact.
`tools/persona_qa/completion.py` hardcodes `health = "infra:harness"` for
anything that looks like a harness error, from a run state it received several
layers downstream of wherever the fault actually occurred. The other producers
(`tools/dev-workflow-matrix/*.py`, `tools/bugfix-bakeoff/external/bench.py`)
each re-derive it their own way.

An infrastructure fault misfiled as `model:result` is scored against a
model/candidate — silently corrupting a bakeoff or an arena cell.

## What qatarget provides

### Verified targets

`Resolver.Resolve` will not return a URL that did not come from a health-passed
endpoint lease on a `Ready` runtime. It checks, and refuses on mismatch:

- runtime state is `Ready` and not expired;
- `SourceManifestDigest` matches what the caller asked for;
- `Provider` and `Profile` match;
- the requested `BrowserEndpointRole` resolves to **exactly one** endpoint
  across health-passed services (ambiguity is a failure, not a pick-first);
- the endpoint's `RuntimeID` and `Generation` match the record, its lease has
  not expired, and its exposure is `review` or `public`;
- the lease URL parses as an http/https URL with a host.

Endpoint **roles**, not ports or URLs, are the stable identity. The resolved
`DispatchTarget` carries `RuntimeGeneration` and `SourceManifestDigest` into
terminal evidence, so a later reader can tell exactly what was under test.

### A typed failure taxonomy

`FailureKind` is the typed source for the split the completion-state schema
already requires:

| `FailureKind` | completion-state `health` |
|---|---|
| `product` | `model:result` |
| `access`, `worker`, `network`, `model`, `harness`, `runtime-provider`, `endpoint-broker` | `infra:<kind>` |

`FailureOf` derives the kind from the resolver's own error, so a broker or
runtime fault is **named at the point it happens** rather than being flattened
into a product failure three layers later.

Drivers must preserve the `FailureKind` verbatim in terminal evidence.
Flattening it back into a boolean pass/fail reintroduces precisely the
misattribution the schema was written to prevent.

## Why Go and not more Python

Three reasons, in order of weight:

1. **It is a correctness boundary, not a capture adapter.** Per AGENTS.md,
   bespoke Playwright/xterm.js/VS Code/rrweb recorders are *capture adapters
   only* — they consume a generated run bundle and must not carry private
   case lists. Deciding whether a target is legitimate, and classifying a
   fault as infra or product, is exactly the kind of shared contract that
   belongs below the adapters.
2. **The runtime state it reads is already Go.** `runtime.Record`,
   `runtime.EndpointLease`, generations, and leases live in
   `internal/capsule/runtime`. Re-modelling them in Python means a second
   source of truth for liveness.
3. **Repo policy.** New Python needs a tracked exception in
   `policy/python-exceptions.tsv` with repo-local justification; Go is the
   default for product/runtime code.

## Current state and what remains

Landed and tested (`go test ./internal/capsule/runtime/qatarget/...`):

- `Target` / `DispatchTarget` envelopes and `Target.Validate`;
- `Resolver.Resolve` with the full check list above;
- `FailureKind` + `FailureOf`.

Not done — this is the wiring gap:

1. **No caller.** No Go or story code constructs a `Target`. The host
   capability that would expose this to `stories/product-journey-qa` and
   `stories/scenario-qa` does not exist yet.
2. **No completion-state bridge.** The `FailureKind` → `health` mapping in the
   table above is documented here but not implemented; nothing emits a
   completion-state record from a `DispatchTarget`.
3. **Python still owns the live path.** `host.product_journey.run`,
   `host.session_mining.run`, and `host.ui_qa.run` shell out to `python3`
   against files under `tools/`, which are **not embedded in the binary**.
   Until that changes, targeting cannot move here for real.

Item 3 is the load-bearing one, and it is a release concern independent of
this package: those three host capabilities are registered in the default host
registry (`internal/host/handlers.go`) and the stories that call them ship
embedded in the binary, so they cannot work outside a kitsoki checkout.

## Related

- `internal/capsule/runtime` — the runtime records and endpoint leases this
  resolves against.
- `schemas/completion-state.schema.json` — the verdict/health contract this
  feeds.
- `tools/product-journey/README.md` — the harness being replaced.
- AGENTS.md — universal scenario mechanism and capture-adapter rules.
