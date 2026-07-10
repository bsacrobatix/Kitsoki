# Capsule CI reference story

`capsule-ci` is the composition skeleton for story-native CI. Projects select a
story through `.kitsoki/ci.yaml`; they may import this story or supply an
equivalent story that accepts the normalized CI envelope and emits a validated
`capsule-ci-verdict/v1` artifact. It intentionally does not define shell steps
or a DAG.

The runtime, not this story, owns workspace materialization, environment locks,
executor selection, receipts, and promotion authorization. Optional LLM review
and writer phases are ordinary project story rooms with explicit policies.
