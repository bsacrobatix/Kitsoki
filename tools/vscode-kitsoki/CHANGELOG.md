# Changelog

All notable changes to the Kitsoki VS Code extension.

## 0.2.0

- **Auto-discover an onboarded instance.** When `kitsoki.storiesDir` is left
  empty, the extension now walks up from the workspace root looking for
  `.kitsoki/stories/`, `.kitsoki.yaml`, or a bare `stories/` directory (in
  that order) — so opening a subdirectory of a larger checkout (a monorepo
  package, a nested worktree, …) still finds the project's stories instead of
  falling through to "no stories found." See `src/discover.ts` /
  `resolveStoriesDir` in `src/backend-resolve.ts`.
- **Clearer missing-binary error.** A spawn failure now shows a VS Code error
  notification naming the binary that wasn't found and pointing at both fixes
  — `make build-bin` or the `kitsoki.binaryPath` setting — instead of only a
  line in the Output channel. See `spawnErrorHint` in `src/backend-resolve.ts`.
- **`kitsoki.ticketRepo` setting.** The in-product "Report a bug" feature now
  files locally (`issues/bugs/<id>.md` under the workspace) by default,
  instead of inheriting `kitsoki web`'s own hardcoded
  `constructorfabric/Kitsoki` default (which requires `GH_TOKEN` and would
  file against kitsoki's own dogfood repo, wrong for any other project). Set
  `kitsoki.ticketRepo` to an `owner/repo` to file real GitHub issues instead.
- **Two new e2e specs**, both driven against a REAL VS Code window (the same
  proven pattern as `vscode-prd-demo.e2e.spec.ts` /
  `vscode-deliver-decompose-walk.e2e.spec.ts`):
  - `vscode-bugfix-walk.e2e.spec.ts` — the bugfix pipeline
    (`stories/bugfix/flows/happy_llm.yaml`) end to end, idle through
    `@exit:done`. This is also the real-socket capture for the two
    `TODO(schema)` IDE wire shapes (see below).
  - `vscode-file-a-bug-walk.e2e.spec.ts` — Meta launcher → "Report a bug" →
    the capture/review modal → describe → "File bug" → the filed-path toast.
- **Pinned the two `TODO(schema)` IDE wire shapes** (docs/proposals/ide-integration.md
  follow-up 1), captured from a real MCP-over-ws round trip against this
  extension's own `IdeServer`/`IdeTools` during the bugfix walk spec above:
  - `getDiagnostics`: the Go host was sending the narrowing arg as `uri`, but
    `IdeTools.getDiagnostics` (this extension) reads `args.path` — a real
    mismatch that silently dropped the narrowing on every call. Fixed on the
    Go side (`internal/host/ide_handlers.go`); confirmed by asserting the
    corrected wire shape against this extension's own log line.
  - `openDiff`: the `{path, new_text, new_text_path, title}` argument shape
    and the `{ok, verdict}` return shape were already exercised by
    `ide-bridge.e2e.test.ts`; the doc comments now say so plainly instead of
    carrying a stale `TODO(schema)`.

## 0.1.0

Initial release: embed the kitsoki web UI (chat + trace + graph) inside VS
Code as dockable webview surfaces, spawn a local `kitsoki web` backend, IDE
awareness (openFile / openDiff / diagnostics / selection / open editors) via
a `~/.claude/ide/<port>.lock`-compatible MCP server, and the onboarding tour.
