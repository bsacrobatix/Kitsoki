## Full Web UI for Kitsoki (Feature-Parity with TUI)

### Problem

Kitsoki's TUI is the only interactive runtime surface today. It works for terminal-native users but excludes anyone who isn't comfortable in a shell, makes demos awkward to share or record, and limits the surfaces where stories can be embedded or observed. The existing `runstatus` endpoint/view already serves partial runtime state — it's underused and disconnected from the interactive layer.

### Proposed Solution

Extend and repurpose `runstatus` into a full browser-based UI that reaches feature parity with the TUI:

- **Room rendering** — display the active room's rendered view (text, typed elements, images) as the TUI does
- **Input handling** — support all intent types the TUI handles: choice selection, free-form text, confirmations
- **Session state** — show phase, room, and oracle status in real time (the existing runstatus data)
- **Transcript/trace panel** — surface the decision trace the TUI already exposes
- **Meta/off-path access** — allow entering meta mode from the UI the same way the TUI does

The implementation should reuse the runstatus HTTP/SSE infrastructure rather than introducing a new server, and share view-rendering logic with the TUI rather than duplicating it.

### Success Criteria

1. A story can be run start-to-finish in a browser with no terminal interaction required.
2. Every input type the TUI supports (choice, text, confirmation) is exercisable from the UI.
3. The decision trace is visible and updates live alongside room transitions.
4. The existing TUI continues to work unchanged — the UI is additive, not a replacement.
5. The new web UI is extensively unit and e2e tested.

### Scope / Non-goals

- **Not** a redesign of the TUI or removal of terminal support.
- **Not** a hosted/multi-user deployment — localhost dev server only for now.
- **Not** a new server process — must reuse runstatus infrastructure.
- **Not** a polished product UI — PoC-quality, functional over beautiful.⁣⁢⁢⁣
