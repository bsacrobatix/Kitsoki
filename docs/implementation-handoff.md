# Implementation Handoff

An implementation handoff records the boundary between graph-approved intent and
target-repository work. It is durable and non-executing: generating the record
must not launch an agent or mutate the target repo.

The record captures:

- the applied or proposed graph changeset,
- the target repository,
- acceptance criteria,
- evidence or context paths,
- the sanctioned `codex superagent` command to run inside the target repo's
  Capsule workflow.

Schema: [`pog/schemas/implementation-handoff-v0.md`](../pog/schemas/implementation-handoff-v0.md).

Generate a starter record:

```sh
scripts/generate-implementation-handoff.sh \
  --changeset change-example \
  --target-repo /path/to/repo \
  --acceptance "focused validation command passes" \
  --out .context/implementation-handoff.md
```

Validate the docs/schema/generator contract:

```sh
scripts/implementation-handoff-gate.sh
```
