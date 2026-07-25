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

The reusable integration-train story can validate sealed provenance, checkpoints, gates, queue receipts, and deployment attestations, but Kitsoki has no generic host that materializes durable worker bundles, isolates conflicts, submits exact-SHA staging and main candidates, and produces deployment attestations. Until that host is implemented and rebound, the story correctly exits needs-input instead of claiming effects.

## Steps to reproduce

1. Run stories/integration-train/flows/authority_unavailable_needs_input.yaml.
2. Observe the terminal needs-input exit with resume_phase=validate and the authority error preserved.
