<!-- GENERATED FILE — DO NOT HAND-EDIT.
     Source of truth: tools/dev-workflow-matrix/manifest.yaml
     Regenerate with: make dev-workflow-matrix -->

# Dev-workflow surface matrix

Generated from `tools/dev-workflow-matrix/manifest.yaml` by `tools/dev-workflow-matrix/generate.py`.
Plan: `.context/dev-workflows-surface-matrix-plan.md` (§1 target matrix, WS-F F1).

Legend: ✅ works + no-LLM proven · 🟡 works but proof thin or reliability suspect · 🔴 gap · ⬜ intentionally out of scope for the surface.

Every cell carries two proof-class verdicts from `schemas/completion-state.schema.json`: **mechanical** (check_type `replay`; no-LLM, per-commit) and **experience** (check_type `docs-fidelity` / `ux-heuristic` / `journey-verdict`; persona-judged, budgeted — WS-G). "no standing verdict" is the honest state until the arena gate feeds this matrix.

## kitsoki self-instance (.kitsoki/stories/kitsoki-dev/)

| Workflow | TUI | Web | VS Code | gh-agent |
|---|---|---|---|---|
| **Onboard** | 🟡 proof-thin | 🔴 gap | 🔴 gap | ⬜ out-of-scope |
| **PRD / proposal** | ✅ works | 🟡 proof-thin | 🟡 proof-thin | 🔴 gap |
| **Decompose → implement** | ✅ works | 🟡 proof-thin | 🟡 proof-thin | 🔴 gap |
| **File a bug** | ✅ works | ✅ works | 🟡 proof-thin | ✅ works |
| **Fix a bug** | 🟡 proof-thin | 🟡 proof-thin | 🟡 proof-thin | 🟡 proof-thin |

### Cell detail

- **Onboard × TUI** — 🟡 proof-thin: works (init rooms flow-tested) but two divergent paths
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × Web** — 🔴 gap: no web onboarding entry
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × VS Code** — 🔴 gap: extension assumes an already-onboarded repo
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × gh-agent** — ⬜ out-of-scope: App install ≠ repo onboarding; needs a doc'd handoff
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × TUI** — ✅ works: prd story (33 flows) + design rooms (~20 flows). Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × Web** — 🟡 proof-thin: drivable (PRD demo exists) but free-text composer routing unfinished. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × VS Code** — 🟡 proof-thin: PRD demo spec exists (vscode-prd-demo.e2e.spec.ts); v0.1.0 extension. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × gh-agent** — 🔴 gap: no label→prd/design route. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × TUI** — ✅ works: deliver is the canonical chain (schema+lint absorption, budgeted refine loop, adversarial review, managed re-decompose; 11 flows) wired into dev-story (go_deliver on landing/design_done + design_to_decompose_to_impl e2e flow); fleet still thin (1 flow). Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × Web** — 🟡 proof-thin: Playwright walk of the dev-story decompose fixture via kitsoki web --flow (deliver-decompose-walk.spec.ts) — one happy-path proof; red paths unproven on web. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × VS Code** — 🟡 proof-thin: extension e2e spec of the same walk (vscode-deliver-decompose-walk.e2e.spec.ts, real VS Code via kitsoki.flow settings) — one happy-path proof. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × gh-agent** — 🔴 gap: no route. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × TUI** — ✅ works: /bug meta modes + CLI, host tests. Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × Web** — ✅ works: Report Bug modal shipped (proposal doc stale). Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × VS Code** — 🟡 proof-thin: rides web SPA in webview; no dedicated proof. Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × gh-agent** — ✅ works: bug-report deck auto-trigger shipped. Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × TUI** — 🟡 proof-thin: bugfix mature (80+ flows, triage mode) but silent-bounce reliability warranted its own debugging skill. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × Web** — 🟡 proof-thin: drivable; staged-gate & pace issues historically. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × VS Code** — 🟡 proof-thin: rides web SPA; untested for full pipeline. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × gh-agent** — 🟡 proof-thin: real dispatch of stories/bugfix landed (realDispatchPlan, per-job worktree, APP_DIR races scoped to the load span); no triage-only label route yet and end-to-end proof is thin. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict

## gears-rust thin instance (imports @kitsoki/dev-story)

| Workflow | TUI | Web | VS Code | gh-agent |
|---|---|---|---|---|
| **Onboard** | 🔴 gap | 🔴 gap | 🔴 gap | ⬜ out-of-scope |
| **PRD / proposal** | 🔴 gap | 🔴 gap | 🔴 gap | 🔴 gap |
| **Decompose → implement** | 🔴 gap | 🔴 gap | 🔴 gap | 🔴 gap |
| **File a bug** | 🔴 gap | 🔴 gap | 🔴 gap | 🔴 gap |
| **Fix a bug** | 🔴 gap | 🔴 gap | 🔴 gap | 🔴 gap |

### Cell detail

- **Onboard × TUI** — 🔴 gap: not yet exercised on gears-rust (WS-A exit: stranger-shaped onboard replayable no-LLM)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × Web** — 🔴 gap: not yet exercised on gears-rust; also no web onboarding entry at all
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × VS Code** — 🔴 gap: not yet exercised on gears-rust; extension assumes an already-onboarded repo
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Onboard × gh-agent** — ⬜ out-of-scope: App install ≠ repo onboarding; needs a doc'd handoff
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × TUI** — 🔴 gap: not yet exercised on gears-rust (thin @kitsoki/dev-story instance run pending). Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × Web** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × VS Code** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × gh-agent** — 🔴 gap: no label→prd/design route; nothing exercised on gears-rust. Canonical doc: docs/workflows/prd-and-design.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × TUI** — 🔴 gap: not yet exercised on gears-rust (WS-B exit: epic → briefs → branches with no-LLM replay). Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × Web** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × VS Code** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × gh-agent** — 🔴 gap: no route; nothing exercised on gears-rust. Canonical doc: docs/workflows/decompose-and-implement.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × TUI** — 🔴 gap: not yet exercised on gears-rust (needs WS-A A2 external ticket-repo passthrough). Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × Web** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × VS Code** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × gh-agent** — 🔴 gap: not yet exercised on gears-rust (WS-E E3: labeled gears-rust issue → honest triage verdict). Canonical doc: docs/workflows/file-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × TUI** — 🔴 gap: not yet exercised on gears-rust (needs WS-A A2 external ticket-repo passthrough). Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × Web** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × VS Code** — 🔴 gap: not yet exercised on gears-rust. Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × gh-agent** — 🔴 gap: real dispatch exists but nothing exercised on gears-rust (WS-E exit: real triage/fix on a gears-rust issue). Canonical doc: docs/workflows/fix-a-bug.md
  - mechanical: no standing verdict
  - experience: no standing verdict
