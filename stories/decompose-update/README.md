# decompose-update

Deterministic story wrapper for the managed decomposition-update transaction.

The story does not call a live LLM. Its release gate runs:

```bash
go run ./cmd/kitsoki test flows stories/decompose-update/app.yaml --flows stories/decompose-update/flows/managed_delta.yaml
```

The wrapped transaction lives in `tools/decomposition-update/apply_delta.py`.
