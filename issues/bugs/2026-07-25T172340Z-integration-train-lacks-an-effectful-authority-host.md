---
# --- identity ------------------------------------------------
id: "2026-07-25T172340Z-integration-train-lacks-an-effectful-authority-host"
title: "Integration train lacks an effectful authority host"
target: "kitsoki"
filed_at: 2026-07-25T17:23:40Z
filed_by: "brad"

# --- target context ------------------------------------------
component: "integration-train"
kitsoki_rev: "341497334"

# --- runtime -------------------------------------------------
engine_version: "0.0.1-scaffold"
engine_revision: "3414973349ccd397f12c68ec01c087f271918627"
engine_revision_short: "3414973349cc"
engine_dirty: "true"
engine_checksum_sha256: "sha256:f40b9551b6721eadb38cdb5c7f5e429dfa308c9b92557d20d48418c291e84db2"

# --- classification ------------------------------------------
severity: "high"
status: "open"
labels: []

# --- evidence ------------------------------------------------
related: []
---

# Integration train lacks an effectful authority host

The reusable integration-train story can validate sealed provenance, checkpoints, gates, queue receipts, and deployment attestations, but Kitsoki had no registered host boundary at all. That made an unbound runtime host error look like an implementation accident rather than durable delivery state.

## Closed slice

`host.integration_train` is now a native, restart-safe carrier. Its authority
configuration is operator-owned and out of story input; phase commands receive
the sealed request, must return matching authority evidence, and are cached by
the content digest of the exact request. Missing configuration, a missing
phase, a command error, malformed output, or cross-train evidence returns
observable `needs_input` evidence. It never manufactures a ship.

## Remaining scope

The carrier intentionally does not impersonate the concrete POG materializer,
Capsule-CI receipt issuer, queue submitter, or deploy verifier. That adapter
still needs to materialize durable worker bundles and drive real effects. Its
staging-to-main blocker is split into
`issues/bugs/2026-07-25T195758Z-promote-exact-staging-sha-with-receipt.md`.

## Steps to reproduce

1. Run `go run ./cmd/kitsoki test flows stories/integration-train/app.yaml`.
2. With no authority configuration, observe a terminal `needs-input` exit with
   an evidence-backed checkpoint rather than an unregistered-host failure.
