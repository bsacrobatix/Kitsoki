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
