make your worktrees in the project root folder .worktrees

Project skills live at `docs/skills/<name>/SKILL.md` and are exposed to Claude Code by symlinking into `~/.claude/skills/<name>` (Claude Code does not auto-discover skills under `docs/`). When adding a new skill under `docs/skills/`, also create the symlink so it appears in the available-skills list:

```
ln -s "$(pwd)/docs/skills/<name>" ~/.claude/skills/<name>
```

When we do an implementation based on a proposal, the goal is to complete the proposal implementation and move the content to proper narrative docs and delete the proposal - don't leave unfinished work unless specifically instructed, and if so, update the proposal to summarize the completed aspect and focus on the remaining work.

## Testing Multi-System Bugs

When debugging bugs that involve concurrent I/O, external systems, or interactions between components (e.g., slog logs + TUI rendering, file writes + API calls), **do not rely solely on unit tests of isolated functions**. These bugs hide in isolated tests because:

- Unit tests capture single function return values, not actual I/O
- Concurrent operations require actual concurrency to reproduce (timing-dependent)
- Multiple systems' outputs can corrupt each other in ways that don't show in function-level tests

**Pattern**: If a user's bug report shows output from multiple sources mixed together (e.g., "log line contains queue indicator"), write a test that:
1. Captures actual I/O (files, stderr, etc., not just function returns)
2. Introduces the concurrency that triggers the bug
3. Asserts on the **combined output** that reaches the user

See `docs/skills/rendering-tests/SKILL.md` "Critical Pitfall: When Unit Tests Aren't Enough" for examples and the `CapturedIO` helper in `internal/tui/rendering_test_utils.go` for patterns.

**Non-negotiable**: Before declaring a bug fixed, verify the test would FAIL without your fix. A passing test that passes even without the fix is not testing the fix.
