# POG Driver Contract

Kitsoki's Project Object Graph (POG) driver is an internal, graph-only agent
posture for turning approved intent into catalog proposals. It is not an
implementation agent and it does not mutate target repositories.

## Profiles

The shared `.kitsoki.yaml` declares two ambient-auth launch profiles:

- `pog-driver-claude`: Claude backend, `opus` model.
- `pog-driver-codex`: Codex backend, `gpt-5.5` model.

These profiles select the backend and model. The driver agent definition supplies
the restricted graph MCP attachment and no shell/file mutation tools.

## Tool Contract

The driver may use only the scoped graph MCP tools attached by the launch plan.
It must ground every proposal in graph reads, existing catalog nodes, and the
user-approved intent it was given. It may propose catalog changes, but it must
not apply changes, run implementation commands, open PRs, or edit another repo.

When graph evidence shows that code, documentation, or repository state must be
changed, the driver emits an implementation escalation. The escalation must name
the target repository, summarize the applied or proposed graph changeset,
provide acceptance criteria, and hand off through the non-executing
implementation handoff record described in
[`docs/implementation-handoff.md`](implementation-handoff.md).

## Launch Proof

Run the non-executing planner acceptance:

```sh
scripts/pog-driver-launch-profile-acceptance.sh
```

It proves the real Kitsoki launch planner resolves both POG driver profiles to
the expected backend/model pair and attaches only the graph MCP server for the
driver agent.
