make your worktrees in the project root folder .worktrees

The primary checkout is protected as read-mostly. Do implementation work in a
branch worktree under `.worktrees`, commit it there, then land it onto local
`main` from the primary checkout with:

```
scripts/merge-to-main.sh <branch>
```

That helper only accepts fast-forward merges. If it refuses because `main`
advanced, rebase the branch in its worktree onto local `main`, rerun focused
validation, and then run the helper again. Do not manually chmod the primary
checkout except to repair the guard itself.

Prefer GitHub PRs for review and landing, but do not burn CI on every
work-in-progress agent branch. The CI workflow is configured so `pull_request`
runs target `main`; GitHub branch filters on `pull_request` match the PR base
branch, not the source branch. Open draft or staging PRs against a non-main
base branch prefix such as `agent/*`, `integration/*`, or `staging/*` when the
PR is not ready for CI. Retarget or promote the finished PR to `main` only when
it is ready for the required CI gate and GitHub merge.

When local `main` must be reconciled with `origin/main` or `upstream/main`, do
not run `git pull` in the protected primary checkout. From the primary checkout,
run:

```
scripts/sync-main-from-remote.sh --remote origin
```

or `--remote upstream`. The sync helper fetches, creates an integration worktree
under `.worktrees`, and attempts the remote merge there. If conflicts occur,
prefer:

```
scripts/sync-main-from-remote.sh --remote origin --auto-resolve
```

with `KITSOKI_SYNC_RESOLVE_CMD` set to the LLM/agent command that resolves and
stages conflicts, and `KITSOKI_SYNC_REVIEW_CMD` set to a separate read/check
agent command that verifies no local or remote work was dropped. For manual
resolution, resolve conflicts inside that integration worktree, rerun the helper
with `--continue <branch>` and `KITSOKI_SYNC_REVIEW_CMD` set, validate there,
and only then land the integration branch with `scripts/merge-to-main.sh
<branch>`.

Project skills live in the Codex-standard `.agents/skills/<name>/SKILL.md` location. Claude Code does not auto-discover that directory, so `make setup` links every `.agents/skills/*/SKILL.md` into `.claude/skills/` (relative symlinks; `.claude/` is gitignored). After adding a new skill, re-run:

```
make setup
```

To link a single skill by hand (e.g. without a full setup run):

```
ln -s "../../.agents/skills/<name>" .claude/skills/<name>
```

After creating a new worktree with `git worktree add`, run `make bootstrap-worktree`
from inside it before running `go run ./cmd/kitsoki` or any Playwright spec — it
stages the embed-only stories/SPA dirs, installs `tools/runstatus` node_modules,
and warms the Go build cache, all of which are otherwise empty/cold in a fresh
worktree.

When we do an implementation based on a proposal, the goal is to complete the proposal implementation and move the content to proper narrative docs and delete the proposal - don't leave unfinished work unless specifically instructed, and if so, update the proposal to summarize the completed aspect and focus on the remaining work.

use the `.context` folder for transient markdown files like proposals, summaries, etc... and use the `.artifacts` folder (with subfolders as necessary) for any generated artifact for review that shouldn't be committed.  following these guidelines will help to avoid bot pollution and cruft in the repo.

Automated testing should never use a real LLM or incur costs - mock agents via cassettes should be used in all cases.  Tests which require real LLM must be gated and only done when specifically requested and required - never automatically or without checking first.

use dependency injection patterns wherever relevant.

principle of least surprise.

`AskUserQuestion` is hard-denied in every dispatched `claude -p` agent (it auto-resolves with empty answers when headless — a silent landmine). When a live operator surface is attached, agent questions are instead forwarded into kitsoki via the operator-ask bridge (the `mcp__operator__ask` tool) and surfaced on web + TUI; when no operator is attached (cassettes/flows/headless) no replacement tool is added and the agent proceeds on its own. See `docs/architecture/operator-ask.md`.

when in doubt always save a markdown into .context for review later - much easier to check/review than staying in the conversation and requiring an extra turn.

commit when you're done with your work and commit only your work - this helps to avoid a mess in the repo.  There may be parallel agents working.  Keep a minimum number of commits and amend as you go where there's no value in separate commits.  Separate key decisions or aspects in clean commits to enable bisect and reverts to work well and not create a mess.

avoid generating a binary of kitsoki for testing - just use go run unless there's some very specific reason that won't work (I think there's issues related to file embedding... not sure)

the kitsoki architecture provides extensive flow testing and mocking capabilities - both synthetic and recorded - to enable thorough testing and demonstration without LLM usage.  make sure to de-risk all cases with flow tests and mocked interactions - and when doing live integration tested if mocks/flows change ensure the tests accurately reflect it.
