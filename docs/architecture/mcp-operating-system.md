# MCP operating system — controlled rollout architecture

The MCP operating system is a candidate control plane for agents that need a
scoped change, proof, and durable evidence. It is not a replacement for the
current Studio MCP toolbox yet, and it does not change the default
`kitsoki-mcp-driver` profile.

## Rollout state

**HOLD.** The only promotion candidate is `strict`, but its recorded replay
matrix has a correctness failure for `trace-stalled-turn`. A safety pass is
necessary but insufficient: every safety and correctness cell must pass before
cost or latency can be compared. This is not permission to make strict default.

The decision is generated from
[`tools/arena/specs/mcp-operating-system-replay.yaml`](../../tools/arena/specs/mcp-operating-system-replay.yaml)
and enforced by
[`internal/agentbench/mcp_operating_system_decision.go`](../../internal/agentbench/mcp_operating_system_decision.go).

| Candidate | Safety | Correctness | Disposition |
|---|---:|---:|---|
| `strict` | pass | fail (`trace-stalled-turn`) | hold |
| `escape` | fail | pass | not promotable |
| `legacy` | fail | pass | not promotable |

The existing Studio MCP architecture remains documented in [MCP studio](mcp-studio.md).
This page describes only the candidate and its rollout boundary.

## Proposed control plane

The strict profile makes a mutation an evidence-bearing lifecycle:

1. `objective.open` records the bounded outcome.
2. `workspace.create` creates a server-held managed workspace; reads, search,
   patch/write, CodeAct, commit, merge, and teardown stay on that plane.
3. `policy.authorize_mutation` checks the requested mutation belongs to the
   objective.
4. `gate.catalog` and `gate.run` execute a named, typed validation gate.
5. `evidence.record` and `receipt.list` retain validation proof.
6. `objective.close` requires retained evidence; `objective.reopen` makes a
   changed or unproven outcome explicit.

`studio.diagnose`, `session.explain`, and `trace.explain` are the bounded
diagnostic path. They are preferred over changing an attachment, workspace, or
policy assumption based on an opaque failure.

The strict profile deliberately omits `host.run` and `host.patch`, and it does
not create raw git worktrees. Those are containment decisions, not aliases: a
tool absent from the profile cannot be reached by changing the prompt.

## Profiles and migration boundary

`strict` is the only profile that could ever become a default. `escape` is a
separate, receipt-bearing exception path; `legacy` names the current toolbox
surface for comparison and compatibility. Neither can be promoted because their
replay safety failures remain visible.

While the decision is HOLD:

- existing clients continue using the existing Studio MCP registration and
  `kitsoki-mcp-driver` unchanged;
- `kitsoki-mcp-driver-operating-system` is a preview contract, not a default
  attachment or production migration instruction;
- strict profile work is limited to deterministic replay, schema, and
  diagnostic work; and
- no caller may silently fall back to `host.run`, `host.patch`, raw worktree
  tools, or legacy when strict rejects an operation.

After an eligible replay decision, final integration may register the profile,
publish the canonical tool/default documentation, and offer an opt-in migration.
An opt-in must open an objective, use a managed workspace, run named gates, and
retain receipts. It is not a bulk conversion of existing MCP clients.

## Escape and rollback

An escape is not an automatic fallback. The caller must state why strict cannot
proceed, use the separately authorized escape profile, and record the exception
and resulting evidence. The escape result cannot become strict promotion
evidence.

The rollout is reversible: stop new opt-ins or registration, preserve managed
workspace receipts and traces, return affected callers to the explicitly
recorded legacy/default attachment, then repair the failing strict replay case
and review the full hard-gate matrix again. Because the current decision is
HOLD, there is no default change to roll back.

## Decision and live-calibration boundary

The replay matrix is deterministic and provider-free. Regenerate it with:

```sh
python3 tools/arena/arena/mcp_operating_system_report.py report \
  --spec tools/arena/specs/mcp-operating-system-replay.yaml \
  --out .artifacts/mcp-operating-system/replay
```

The result must remain HOLD until `trace-stalled-turn` and every other strict
safety/correctness cell pass. The derived report, JSON decision, and Slidey deck
are evidence; none dispatches a provider call.

Live calibration is outside automated tests. It needs an operator to supply the
exact authorization token `I_UNDERSTAND_LIVE_CALIBRATION`, a recorded budget of
at least USD 1.00, and a separately approved live execution path. The current
`live-calibration` command records only `authorized-not-dispatched`; it does not
call a provider. Tests must never pass a live-calibration flag or treat
authorization as a promotion.

## Ownership

This document and the preview profiles describe rollout behavior. Final
integration owns Studio tool registration, canonical `mcp-studio.md` and agent
guide wording, and default driver profiles. Keeping ownership separate prevents
a HOLD decision from becoming a default merely because a profile file exists.
