make your worktrees in the project root folder .worktrees

Project skills live at `docs/skills/<name>/SKILL.md` and are exposed to Claude Code by symlinking into `~/.claude/skills/<name>` (Claude Code does not auto-discover skills under `docs/`). When adding a new skill under `docs/skills/`, also create the symlink so it appears in the available-skills list:

```
ln -s "$(pwd)/docs/skills/<name>" ~/.claude/skills/<name>
```

When we do an implementation based on a proposal, the goal is to complete the proposal implementation and move the content to proper narrative docs and delete the proposal - don't leave unfinished work unless specifically instructed, and if so, update the proposal to summarize the completed aspect and focus on the remaining work.
