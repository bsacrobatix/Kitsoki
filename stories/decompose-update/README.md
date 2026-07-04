# decompose-update

Story wrapper for the managed decomposition-update transaction.

The story has an adversarial review room before the deterministic transaction
gate. Tests stub `host.agent.decide`, so the release gate does not call a live
LLM or incur model cost.

```bash
go run ./cmd/kitsoki test flows stories/decompose-update/app.yaml --flows stories/decompose-update/flows/managed_delta.yaml
```

The wrapped transaction lives in `tools/decomposition-update/apply_delta.py`.
The deterministic tool versions the prior graph, validates candidate deltas
before writing, rejects corrupted variants, and appends `plan-evolution.jsonl`
events that goal-seeker reports project into PM artifacts.
