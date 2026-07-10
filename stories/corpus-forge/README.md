# Corpus forge

Corpus Forge turns source-neutral `corpus-case.v1` proposals into a frozen,
reviewable corpus receipt. It is deliberately no-LLM: intake adapters provide
canonical candidates, Starlark validates and deterministically selects them,
and `host.corpus.prove` independently establishes RED at `baseline_ref` and
GREEN at `fix_ref`. A candidate's source `verified_red` or `verified_green`
field is never proof and is never read by this story.

The story has no Python or shell execution path. Its deterministic steps live
in `scripts/`: load canonical candidates, select an ID-sorted fixed-size
corpus, request typed proof, and build `corpus-receipt.v1`. A missing proof
executor, malformed proof, or proof rejection routes to the rejected room;
the story cannot produce a receipt from self-report.

For programmatic use, inject `host.CorpusProofHandler` with a
`corpusproof.Executor` configured with the repository's fixture opener and
oracle runner. Tests replace that host verb with a deterministic fixture stub
while still running the real Starlark scripts. Use a distinct heldout corpus
for promotion claims.
