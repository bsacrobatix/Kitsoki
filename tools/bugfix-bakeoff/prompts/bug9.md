# Bugfix task — staged, in-scope

You are fixing a single bug in the kitsoki repository (Go + TypeScript). You are
working in a hermetic worktree checked out at the commit BEFORE the fix, so the
bug is present. Follow the stages below in order. Do not edit unrelated code.

## Bug context

**Component:** host · **Severity:** P1

**Symptom:** Concurrent dogfood/agent sessions operate on the SAME git worktree
checkout and clobber each other's uncommitted work. The engine's worktree
host establishes per-session scratch state by story convention only — when two
sessions resolve the same worktree path (e.g. `.worktrees/bf-<ticket>`), the
second session's worktree-create **silently succeeds against the tree the first
session already owns**, so both drive the same checkout and one session's WIP is
destroyed (observed across 7 sessions, with unrecoverable data loss).

**Root cause area (`internal/host/git_worktree.go`):** `worktreeCreate` (and the
surrounding worktree host) does not record or check an OWNER for an existing
worktree. A second session pointed at an already-in-use worktree path is allowed
to adopt it instead of being refused. There is no per-session ownership marker
and no orchestrator plumbing of a stable session id into the worktree host.

**Expected:** a worktree already owned by one session is **refused** to a
different session (a clear, named error), so concurrent sessions cannot share a
checkout and clobber WIP. Each session gets an isolated, ownership-fenced
working folder.

**Actual:** the second session's create succeeds and silently shares the
first session's checkout → destructive git churn → unrecoverable WIP loss.

**Investigation hints:**
- `internal/host/git_worktree.go` — `worktreeCreate`: where an existing tree is
  reused; add an ownership marker written at create time and checked on reuse.
- `internal/orchestrator/orchestrator.go` — how a stable per-session identifier
  reaches the host so the owner can be recorded/compared.

## Stages — do these IN ORDER

1. **REPRODUCE (RED first).** Write a focused failing test: session A creates a
   worktree at a path; session B (a DIFFERENT session id) then attempts to create
   at the same path, and you assert session B's create is REFUSED (not silently
   shared). Run it and CONFIRM IT IS RED (today B's create succeeds). Do not
   proceed until the red test fails for the right reason.
2. **IMPLEMENT (minimal fix).** Make the smallest change that fences cross-session
   sharing — e.g. a per-session ownership marker written into the worktree and
   checked when a create targets an existing tree. Stay in scope.
3. **VERIFY (GREEN + no regressions).** Re-run your test and confirm it is GREEN.
   Then run the surrounding suite and confirm nothing regressed.

## Build / test commands

```
go build ./...
go test ./internal/host/...
go test ./internal/host/ -run <YourTestName>   # your repro test
```

## Rules

- Write your OWN reproduction test; do not look for or rely on any pre-existing
  hidden regression test. Do NOT make a real LLM call.
- Keep the change minimal and in-scope; do not refactor or touch unrelated code.
- Honor the stage order: reproduce (red) → implement → verify (green).
