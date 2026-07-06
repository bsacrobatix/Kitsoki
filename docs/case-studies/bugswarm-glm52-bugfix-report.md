# BugSwarm + GLM-5.2 bugfix comparison report

Status: interim, evidence-backed where cells exist. No new live LLM runs were
performed for this report.

## Question

Compare the Kitsoki `bugfix` pipeline with raw prompts on GLM-5.2, using token
usage as the primary cost axis, and extend the success-rate comparison to a
BugSwarm corpus alongside the existing OSS oracle corpus.

## Current Evidence

The only committed GLM-5.2 bugfix cell with real token accounting is the Kitsoki
pipeline run for `bug9`:

| corpus | task | treatment | model | verdict | tokens | cost | evidence |
|---|---:|---|---|---|---:|---:|---|
| kitsoki hidden-oracle | bug9 | kitsoki | GLM-5.2 | partial | 2,890,980 | $15.57545 | `tools/bugfix-bakeoff/results/cells/bug9-glm-5.2-kitsoki.json` |

Important interpretation: the automated hidden oracle failed, but the cell was
adjudicated `partial` because GLM closed the destructive data-loss path through
per-session worktree keying while missing the host-layer defense-in-depth the
oracle asserted. The result is useful evidence about Kitsoki+GLM behavior, but
it is not a solved cell.

There is no committed raw-prompt GLM-5.2 cell with trustworthy token usage yet.
Therefore a numeric Kitsoki-vs-raw GLM token ratio would be fabricated. The
reporting contract should display that arm as `pending`, not zero-cost and not a
failure.

## Existing OSS Oracle Corpus

The current frozen arena cost corpus is
`tools/arena/corpus/cost-bench.manifest.yaml`:

- 26 tasks total.
- 12 repositories represented.
- 20 repo-history tasks across the pre-registered public OSS targets.
- 6 existing hidden-oracle bugfix fixtures from `query-string` and `kitsoki`.
- Every committed task records deterministic RED/GREEN proof fields and a
  training/heldout split.

This corpus remains the default internal source for paired Kitsoki-vs-raw
comparisons. It is intentionally not replaced by BugSwarm.

## BugSwarm As A Reusable Source

BugSwarm is now represented as a separate arena source in
`tools/arena/corpus/sources.yaml`. The adapter is
`tools/arena/scripts/bugswarm_to_arena.py`.

BugSwarm contributes a different external validity axis than our current
repo-history and hidden-oracle fixtures:

- Unit: one fail/pass CI artifact.
- Required source fields: `image_tag`, `repo`, `failed_job_id`,
  `passed_job_id`.
- Oracle kind: `bugswarm_fail_pass_pair`.
- RED rule: the failed job script exits non-zero inside the artifact.
- GREEN rule: the passed job script exits zero inside the same artifact.
- Isolation: BugSwarm supplies the Docker image containing both versions and CI
  scripts.

Generated BugSwarm tasks intentionally start as unverified:

```bash
python3 tools/arena/scripts/bugswarm_to_arena.py \
  --in .artifacts/bugswarm/artifacts.json \
  --out .artifacts/bugswarm/arena-source.yaml
```

The generated manifest is a candidate source list. A task becomes benchmarkable
only after Docker verification flips `verified_red` and `verified_green` to
true. This mirrors the existing RED@baseline/GREEN@fix discipline and prevents
spending model tokens on degenerate artifacts.

## Planned Blend

The combined study should keep two denominators:

| denominator | included tasks | why |
|---|---:|---|
| OSS oracle corpus | 26 current tasks | continuity with existing Kitsoki cost-efficiency work |
| BugSwarm verified subset | N verified imported artifacts | external CI fail/pass corpus; N must be reported after verification |

Report the blended result as a stratified rollup, not a single undifferentiated
average. BugSwarm and the OSS oracle corpus have different sampling processes,
different oracle semantics, and different setup costs.

## Metrics

Primary:

- success rate: `solved / (solved + partial + failed)` with `pending` and
  `blocked` excluded from the model-quality denominator.
- partial rate: reported separately because hidden oracles can be
  implementation-coupled.
- total tokens: input + output + cache-read + cache-write where available.
- cache-adjusted USD: secondary, never the primary comparison axis.

Required cells for the GLM-5.2 headline:

| corpus | treatment | candidate | status |
|---|---|---|---|
| OSS oracle corpus | kitsoki | glm-5.2 | partially populated by the bug9 evidence only |
| OSS oracle corpus | raw prompt | glm-5.2 | pending |
| BugSwarm verified subset | kitsoki | glm-5.2 | pending |
| BugSwarm verified subset | raw prompt | glm-5.2 | pending |

## Current Conclusion

Current evidence is insufficient for the requested final claim. We can say only:

1. Kitsoki+GLM-5.2 has one committed, real-token bugfix cell: `bug9`, `partial`,
   2.89M total tokens.
2. No committed raw-prompt GLM-5.2 cell exists, so the token comparison cannot
   yet be computed.
3. BugSwarm is now wired as a reusable source family and can be blended into the
   arena corpus after explicit artifact selection and no-LLM Docker
   verification.
