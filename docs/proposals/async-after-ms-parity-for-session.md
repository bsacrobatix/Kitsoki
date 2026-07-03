### async_after_ms parity for session.submit / session.continue / session.answer

# Runtime: `async_after_ms` parity for `session.submit` / `session.continue` / `session.answer`

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

Only `session.drive` can return early and hand the client back to polling. Its three sibling turn tools — `session.submit`, `session.continue`, `session.answer` — block the calling MCP client for the entire turn, and that turn can dispatch a long `host.agent.decide` / `host.agent.task` LLM call (30s–2min for a bugfix-pipeline phase acceptance).

The bugfix pipeline is **menu-driven**: advancing a phase is a `session.submit {intent}`, not free text. So the tool a driving agent uses most is exactly the one that blocks — it eats the whole 30s–2min inside one MCP call and risks a client-side tool-call timeout with no recovery. Top ergonomics gap found dogfooding the pipeline through the studio MCP (issue #44's sibling finding).

The fix is mechanical parity, not a new async architecture. `session.drive` already has all the machinery (`driveAsyncAfter` at `session_tools.go:61`, `driveSuspendable` at `session_runtime.go:492`, the `rt.inFlight` broker, the `{running}` wire shape + `session.status` poll path). The three siblings just route through it.

## What changes

Each of the three tools gains an optional `async_after_ms` field (a `*int` pointer, `omitempty`), and the turn runs through the same suspend broker `session.drive` already uses.

- **submit/continue** — today `handleSessionSubmit`/`handleSessionContinue` call `rt.submit`/`rt.cont`, which run **inline** via `SubmitDirect`/`ContinueTurn` and never touch the broker. They move onto the shared suspendable path, parameterized by the turn function.
- **answer** — `resumeSuspendable` already runs on the broker but its `waitNext(ctx)` has **no deadline**; it honors `async_after_ms` with the same bounded-wait ⇒ `{running:true}`.
- Async bookkeeping is extracted from `driveSuspendable` into a shared helper parameterized by the turn closure (rejecting the "bolt a flag onto the inline call" alternative that would let poll-state drift across verbs).

**Pointer semantics, independent of drive's `0 → 25s`:** `nil`/absent → block (byte-identical to today); explicit `0` → immediate `{running}`; `N>0` → wait N ms then `{running}`. Drive's own `0 → 25s` default is untouched.

**No new wire fields:** reuse `RunningDrive` / `{running:true}` verbatim; `session.status` contract gains no keys; because `runningDriveSnapshot` reads `rt.inFlight`, the poll path serves the three verbs with no edits.

**Secondary in-scope correctness fix:** route the blocking default through the broker too (unbounded wait). Required for the shared path, and it closes a latent race — today submit/continue run inline off the broker and can race a concurrent drive/submit on the same handle. After this, all four verbs are serialized by the single-in-flight guard.

Out of scope: pipeline-aware dashboard, handle labeling, `drive_to`/`auto_advance`.

## Impact

- **Code seams:** `session_tools.go:201-225` (add `*int AsyncAfterMS`), `:547-588` + `:481-515` (handlers); `session_runtime.go:492-547` + `:582-630` (extract helper, retire inline submit/cont, bound resume wait).
- **Backward compat:** opt-in / default-off; every existing flow fixture & caller with no field is byte-identical.
- **Docs on ship:** `docs/architecture/mcp-studio.md` session-tool table + bounded-call prose; `docs/recipes/studio-mcp-async-smoke.md` extended to the three verbs.

Full document (runtime template spine: Why / What changes / Impact / Vocabulary / The model / Engine seams & invariants / Backward compatibility / Tasks / Verification / Open questions / Non-goals) written to `004-proposal.md`. Open questions: none — the two brief decisions (pointer semantics; broker extraction incl. blocking default) are resolved.
