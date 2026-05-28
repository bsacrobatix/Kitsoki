---
name: kitsoki-session-digest
description: Catch a fresh session up on recent/active work without polluting its context. Reads the per-session summaries the Stop hook writes to `.context/` in the current worktree and (optionally) sibling worktrees under `.worktrees/`, hands them to a Haiku subagent, and returns a tight digest of what's recently been worked on, decided, or left in-flight. Use when the user asks "what was I doing", "catch me up", "what's active across worktrees", or starts a session after a `/clear` and wants context restored.
---

# Kitsoki Session Digest

The Stop hook at `tools/summarize-session.py` writes one `.context/session-<id>.md` per Claude session, refreshed on each stop. Over time this accumulates a per-worktree log of what was done. This skill rolls those files into a short digest using Haiku, so the calling session gets the gist without reading every summary into its own context.

## Inputs

- `--worktrees` / `all` — include sibling worktrees under `<repo-root>/.worktrees/*/.context/` (default: current worktree only).
- `--since <duration>` — only include session files modified within the window (e.g. `24h`, `7d`). Default: all.
- A free-form focus hint (e.g. "story authoring", "host-cassettes worktree"). Passed to Haiku verbatim.

If the user's prompt is bare ("catch me up", "what was I doing"), assume current worktree + no time filter.

## Step 1 — find the session files

```sh
REPO_ROOT="$(git rev-parse --show-toplevel)"
find "$REPO_ROOT/.context" -maxdepth 1 -name 'session-*.md' 2>/dev/null | xargs -r ls -t
```

For `--worktrees` / `all`, also include (use `find`, not a bare glob — zsh's `nomatch` aborts the whole command when no worktree has `.context/session-*.md`, which is what bit the v1 of this skill):

```sh
find "$REPO_ROOT/.worktrees" -maxdepth 3 -path '*/.context/session-*.md' 2>/dev/null | xargs -r ls -t
```

For `--since`, filter with `find ... -mtime`:

```sh
find "$REPO_ROOT/.context" -name 'session-*.md' -mtime -1   # last 24h
```

If no files match, tell the user "no session summaries yet — hook hasn't fired" and stop. Don't fabricate.

## Step 2 — delegate to Haiku

**Do NOT read the session files into the main session's context.** Launch the `kitsoki-session-digest` subagent (defined at `.claude/agents/kitsoki-session-digest.md`, pinned to `model: haiku`) and have it do the reading + summarising. The whole point of the skill is to keep that bulk out of the calling context.

Prompt template:

> You are catching another Claude session up on recent work in the kitsoki repo. Read the following session-summary files and produce a digest. Each file is one prior Claude session's running summary, written by a Stop hook.
>
> Files to read (absolute paths):
> - `/abs/path/session-XXX.md`
> - ...
>
> Produce:
> 1. **Active threads** — work that looks in-progress or unfinished (3-6 bullets, most recent first). Tag each with the worktree name if multiple worktrees are in play.
> 2. **Recently landed** — completed work worth knowing about (2-4 bullets).
> 3. **Open questions / decisions pending** — anything the prior sessions explicitly left unresolved.
>
> Keep the whole digest under ~300 words. Cite the session id (the `XXX` from the filename) after each bullet so the caller can drill in if they want. If a focus hint is provided, lead with threads matching it. Do not invent details that aren't in the summaries.

Pass the focus hint (if any) inline.

## Step 3 — relay the digest

Print the Haiku agent's response to the user verbatim, prefixed with a one-line header noting scope, e.g.:

```
Digest: current worktree, 4 sessions, last 7 days
```

Then the bullets. Don't editorialise on top — the user wants the digest, not your reaction to it.

## Gotchas

- **Don't read `.context/*.md` files directly with Read** — defeats the purpose. Only the subagent should read them.
- **Cursor files** (`session-*.cursor`) are bookkeeping for the Stop hook; ignore them.
- **Empty / very short summaries** (a single bullet) usually mean the session got compressed or interrupted. Worth including but flag low confidence.
- **Stale worktrees**: a `.worktrees/foo/.context/` directory may outlive its branch. If a digest item looks orphaned, mention it but don't try to clean up — that's the user's call.
- The hook only fires past `MIN_TOKENS=75_000` or `MIN_TURNS=20`, so very short sessions won't appear at all. That's expected, not a bug.
