# pr-split — concern-grouped PRs from a branch's commits

Takes the commits on a feature branch that are ahead of integration, groups them
into **concern buckets**, and opens **one PR per concern** — each independently
reviewable and revertable.

## Why a story (not a script)
All the logic lives inside kitsoki as a story; the external harness only invokes
it. The split honors progressive determinism:

- **Deterministic (no LLM):** `idle` lists `integration..HEAD`, reads each
  commit's changed files, and maps paths → concern (`internal/**`→core,
  `tools/bugfix-bakeoff/**`→harness, `docs/**`→docs, `stories/**`→stories, …).
  `splitting` does every git/gh operation (branch, cherry-pick, push, `gh pr
  create`) in a throwaway worktree so the caller's tree is never disturbed.
- **LLM (one step):** `planning` runs a single fenced `bucketer` agent that
  finalizes mixed-concern commits and authors each PR's title/body. It has no
  Bash — the story drives every command. A `host.starlark.run` step then
  serializes the plan to JSON for the deterministic splitter.

## Flow
`idle` (classify) → `planning` (agent buckets + serialize) → `splitting`
(branch/cherry-pick/push/PR per bucket) → `done` (summary). `toggle_dry_run`
plans without pushing.

## Run it
Drive through the studio MCP (no external logic needed):

```
session.new {
  story: "stories/pr-split/app.yaml",
  harness: "live",
  initial_world: { working_dir: "<checkout>", integration_branch: "main" }
}
```

Then `session.drive {intent: proceed}` → review the proposed PRs →
`session.drive {intent: accept}`. Or `make pr-split WORKDIR=<checkout>`.

## Tests (no LLM)
`kitsoki test flows stories/pr-split/app.yaml` — `happy_path_two_prs` stubs the
agent + every host call and asserts the full idle→…→done path opens two PRs.
