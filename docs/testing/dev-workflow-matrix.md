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
| **Decompose → implement** | 🔴 gap | 🔴 gap | 🔴 gap | 🔴 gap |
| **File a bug** | ✅ works | ✅ works | 🟡 proof-thin | ✅ works |
| **Fix a bug** | 🟡 proof-thin | 🟡 proof-thin | 🟡 proof-thin | 🔴 gap |

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
- **PRD / proposal × TUI** — ✅ works: prd story (33 flows) + design rooms (~20 flows)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × Web** — 🟡 proof-thin: drivable (PRD demo exists) but free-text composer routing unfinished
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × VS Code** — 🟡 proof-thin: PRD demo spec exists (vscode-prd-demo.e2e.spec.ts); v0.1.0 extension
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × gh-agent** — 🔴 gap: no label→prd/design route
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × TUI** — 🔴 gap: fragmented: skill vs deliver (5 flows, no README) vs decompose-update (1) vs fleet (1); impl story thin (9 flows)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × Web** — 🔴 gap: same fragmentation as TUI + no web proof
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × VS Code** — 🔴 gap: same fragmentation as TUI + no VS Code proof
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × gh-agent** — 🔴 gap: no route
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × TUI** — ✅ works: /bug meta modes + CLI, host tests
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × Web** — ✅ works: Report Bug modal shipped (proposal doc stale)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × VS Code** — 🟡 proof-thin: rides web SPA in webview; no dedicated proof
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × gh-agent** — ✅ works: bug-report deck auto-trigger shipped
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × TUI** — 🟡 proof-thin: bugfix mature (63 flows, triage mode) but silent-bounce reliability warranted its own debugging skill
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × Web** — 🟡 proof-thin: drivable; staged-gate & pace issues historically
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × VS Code** — 🟡 proof-thin: rides web SPA; untested for full pipeline
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × gh-agent** — 🔴 gap: dispatch is stubbed beats — honesty Task 0 shipped, real dispatch (Tasks 1–4) unstarted; per-job KITSOKI_APP_DIR isolation blocks concurrency
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
- **PRD / proposal × TUI** — 🔴 gap: not yet exercised on gears-rust (thin @kitsoki/dev-story instance run pending)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × Web** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × VS Code** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **PRD / proposal × gh-agent** — 🔴 gap: no label→prd/design route; nothing exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × TUI** — 🔴 gap: not yet exercised on gears-rust (WS-B exit: epic → briefs → branches with no-LLM replay)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × Web** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × VS Code** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Decompose → implement × gh-agent** — 🔴 gap: no route; nothing exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × TUI** — 🔴 gap: not yet exercised on gears-rust (needs WS-A A2 external ticket-repo passthrough)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × Web** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × VS Code** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **File a bug × gh-agent** — 🔴 gap: not yet exercised on gears-rust (WS-E E3: labeled gears-rust issue → honest triage verdict)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × TUI** — 🔴 gap: not yet exercised on gears-rust (needs WS-A A2 external ticket-repo passthrough)
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × Web** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × VS Code** — 🔴 gap: not yet exercised on gears-rust
  - mechanical: no standing verdict
  - experience: no standing verdict
- **Fix a bug × gh-agent** — 🔴 gap: stubbed beats + nothing exercised on gears-rust (WS-E exit: real triage/fix on a gears-rust issue)
  - mechanical: no standing verdict
  - experience: no standing verdict
