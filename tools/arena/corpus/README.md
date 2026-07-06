# Arena Cost Corpus

This directory is reserved for the frozen cost-efficiency corpus from
`docs/goals/generalized-usage/decomposition.yaml` (`WB.1`).

The real corpus must not be hand-curated to flatter Kitsoki. It is created only
after the evidence-based archetype mining step described in
`docs/research/cost-efficiency-benchmark.md`, then frozen as:

- `archetypes.yaml`
- `cost-bench.manifest.yaml`
- `sources.yaml`

Before any live benchmark spend, validate the frozen manifest shape with:

```bash
python3 tools/arena/tests/validate_corpus.py tools/arena/corpus/cost-bench.manifest.yaml
```

That command is intentionally red until the corpus exists and every task records
its deterministic RED/GREEN oracle proof and train/held-out split.

## Reusable Sources

`sources.yaml` records corpus families separately from any one frozen manifest.
The current active source is the pre-registered OSS oracle corpus in
`cost-bench.manifest.yaml`. BugSwarm is also registered as an adapter-ready
source: exported BugSwarm artifact metadata can be converted into arena-shaped
tasks with:

```bash
python3 tools/arena/scripts/bugswarm_to_arena.py \
  --in .artifacts/bugswarm/artifacts.json \
  --out .artifacts/bugswarm/arena-source.yaml
```

The converter is offline and does not pull Docker images. The generated
BugSwarm tasks start with `verified_red: false` and `verified_green: false`;
those flags become true only after an explicit Docker verification proves the
failed job still fails and the passed job still passes inside the artifact.

Plan verification without pulling images:

```bash
python3 tools/arena/scripts/bugswarm_verify_source.py \
  --source .artifacts/bugswarm/arena-source.yaml \
  --out .artifacts/bugswarm/verification.json \
  --dry-run
```

Execute verification only when Docker pulls and long CI jobs are acceptable:

```bash
python3 tools/arena/scripts/bugswarm_verify_source.py \
  --source .artifacts/bugswarm/arena-source.yaml \
  --out .artifacts/bugswarm/verification.json \
  --execute
```

The verifier follows BugSwarm's own artifact contract: `run_failed.sh` must
exit non-zero and `run_passed.sh` must exit zero, each in a fresh container so
the failed script cannot pollute the passed run.

After an `--execute` verification pass, write a benchmark-ready source without
hand-editing YAML:

```bash
python3 tools/arena/scripts/bugswarm_apply_verification.py \
  --source .artifacts/bugswarm/arena-source.yaml \
  --verification .artifacts/bugswarm/verification.json \
  --out .artifacts/bugswarm/arena-source.verified.yaml
```

Dry-run reports are rejected by default because they do not prove RED/GREEN.
Pass `--allow-dry-run` only when you want to carry command-plan metadata forward
without setting `verified_red` or `verified_green`.

## GLM-5.2 + BugSwarm Report

The interim research report is generated, not hand-maintained:

```bash
python3 tools/arena/scripts/glm52_bugswarm_report.py \
  --generated-at 2026-07-06T00:00:00Z \
  --json-out docs/case-studies/bugswarm-glm52-bugfix-report.data.json \
  --markdown-out docs/case-studies/bugswarm-glm52-bugfix-report.md
```

Pass `--bugswarm-source .artifacts/bugswarm/arena-source.verified.yaml` after
applying an execute-mode verification report, and
`--bugswarm-verification .artifacts/bugswarm/verification.json` so the report
records the verification evidence. The generator keeps unavailable GLM-5.2
cells as `pending`, so missing raw-prompt or BugSwarm results cannot
accidentally become zero-cost failures.
