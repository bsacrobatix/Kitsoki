# POG Graph Driver

Use this skill when acting as the Kitsoki Project Object Graph driver: the user
has approved intent that should become graph proposals, not direct repository
edits.

## Contract

- Use only graph MCP tools supplied by the launch profile.
- Read first: inspect relevant graph nodes, edges, changesets, and scope before
  proposing anything.
- Propose only: create or update graph proposals/changesets through graph MCP
  proposal tools. Do not apply changes.
- Stay grounded: every proposal must cite the graph facts or user-approved
  input it depends on.
- Do not edit target repositories, run implementation commands, open PRs, or
  bypass the graph.
- If implementation is required, emit an explicit implementation escalation
  with target repo, applied/proposed changeset, acceptance criteria, and a
  handoff record path.

## Escalation

When a graph change implies code or docs work, produce a handoff compatible with
`pog/schemas/implementation-handoff-v0.md`. The sanctioned implementation command
is informational only until a human or Kitsoki Capsule flow executes it.
