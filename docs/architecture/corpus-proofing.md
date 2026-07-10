# Corpus Proofing

Corpus Forge admits a candidate only after `internal/corpusproof` independently
records its explicit oracle as RED at `baseline_ref` and GREEN at `fix_ref`.
This makes a corpus a measurement instrument rather than a collection of task
descriptions or worker assertions.

## Contract

The intake boundary is JSON `corpus-case.v1`:

```json
{
  "kind": "corpus-case.v1",
  "id": "example-1",
  "repo": "owner/project",
  "source": "local-git-history",
  "baseline_ref": "abc",
  "fix_ref": "def",
  "ticket": "observable bug report",
  "archetype": "bugfix",
  "oracle": {"command": ["go", "test", "./..."]},
  "provenance": {"adapter": "history", "locator": "commit:abc", "source_digest": "sha256:..."}
}
```

`oracle` is canonicalized and SHA-256 hashed before execution. Candidate input
contains no `verified_red` or `verified_green` fields: admission proof is a
separate receipt with baseline/fix command, exit code, output, and an environment
fingerprint.

## Isolation and rejections

The proof executor accepts injected fixture and oracle-runner dependencies. A
fixture adapter may bridge a synthetic [capsule](capsules.md) or a repo-history
manifest; the executor never performs a network fetch or asks a model. Both
legs must expose the same non-empty environment fingerprint.

The following typed rejections are durable exclusion evidence:

- `missing_oracle` — the candidate cannot be evaluated.
- `already_green` — the baseline oracle exits zero, so it is not a regression.
- `non_reproducible_environment` — fixture opening, execution, command capture,
  or environment equality cannot be established; a failing fix is also rejected.
- `invalid_candidate` — required corpus-case identity or provenance is absent.

Adapters may discover many candidates, but only the separate proof receipt can
admit one to calibration or heldout selection.
