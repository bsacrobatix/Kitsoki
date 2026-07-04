# Arena Cost Corpus

This directory is reserved for the frozen cost-efficiency corpus from
`docs/goals/generalized-usage/decomposition.yaml` (`WB.1`).

The real corpus must not be hand-curated to flatter Kitsoki. It is created only
after the evidence-based archetype mining step described in
`docs/research/cost-efficiency-benchmark.md`, then frozen as:

- `archetypes.yaml`
- `cost-bench.manifest.yaml`

Before any live benchmark spend, validate the frozen manifest shape with:

```bash
python3 tools/arena/tests/validate_corpus.py tools/arena/corpus/cost-bench.manifest.yaml
```

That command is intentionally red until the corpus exists and every task records
its deterministic RED/GREEN oracle proof and train/held-out split.
