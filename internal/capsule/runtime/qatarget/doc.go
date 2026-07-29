// Package qatarget resolves an immutable, healthy Capsule runtime into a
// browser target for Persona QA, Scenario QA, and Product Journey drivers.
// It deliberately has no driver or product dependency.
//
// # Why this exists: replacing the Python QA harness's untyped targeting
//
// NOT YET WIRED. Nothing imports this package today. It is the Go landing
// zone for two jobs the Python QA harness under tools/product-journey and
// tools/persona_qa currently does with strings, and it is retained
// deliberately — see docs/architecture/qa-runtime-targeting.md for the full
// migration story and the current status.
//
// (1) Target resolution. The Python harness threads a bare `public_base_url`
// string from argv down through the marathon/matrix/driver layers
// (tools/product-journey/run.py). Nothing proves that URL still belongs to a
// live, healthy runtime of the generation the evidence claims, so a driver
// can capture perfectly good frames against a stale or half-torn-down
// deployment and file the result as a product finding. Resolver.Resolve
// refuses to hand back a URL that did not come from a health-passed endpoint
// lease on a Ready runtime whose generation, source-manifest digest,
// provider, and profile all match what the caller asked for. Endpoint ROLES,
// not ports or URLs, are the stable identity.
//
// (2) The infrastructure-vs-product failure split.
// schemas/completion-state.schema.json already encodes this: its `health`
// field separates `infra:*` (harness/environment broke) from `model:result`
// (a genuine product/model outcome), and states that a blocked/failed verdict
// with `health: infra:*` must never be scored against a model/candidate.
// Today the only producers of that distinction are Python, and they build it
// from stringly-typed guesses — tools/persona_qa/completion.py hardcodes
// health = "infra:harness" for anything that looks like a harness error.
// FailureKind is the typed source for the same split: FailureProduct maps to
// `model:result`, and every other kind maps to a specific `infra:<kind>`.
// FailureOf derives it from the resolver's own error, so a broker or runtime
// fault is named at the point it happens instead of being flattened into a
// product failure three layers later.
//
// Drivers should preserve the FailureKind verbatim in their terminal
// evidence. Flattening it back into a boolean pass/fail reintroduces exactly
// the misattribution the completion-state schema was written to prevent.
package qatarget
