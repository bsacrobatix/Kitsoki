
- add bug report mode
- generate test from trace
- in proposal review mode, make the input and proposal different colors from the rest of the text
- when user presses enter, immediately add their input into the chat window and show thinking there, block input until resolved (can keep some spinner in the input area too)
- check that we're really doing the mcp validation method - i think we're maybe not based on some bugs
- use specific claude agents for each room/intent where oracle/claude is invoked
- extensible stories - reusable dev story w/ company and project-specific aspects (rooms, intents, etc... as reusable building blocks - extended and composed)
- live discovery of story aspects as the user navigates to different projects, projects can define their own story aspects
- visually distinguish between user commands that were interpreted deterministically vs those that use the LLM, when the LLM is used show the actual filled intent that was selected w/ confidence level
- cache of natural language to intents to avoid calling claude again
- expose oracle API so scripts can funnel their LLM usage via the standard interface instead of invoking claude -p individually, possibly bypassing configuration.  this would mean that scripts can use a generic API, and the interface can choose codex vs claude (in the future), and handle the tracing, playback and testing with a standardized mechanism.  scripts would then never use claude directly.
- background jobs on VMs, dispatch and track, survive intermittent connectivity (dev laptop to VM w/ VPN and closed lid issues)
- react UI
- meta mode: ask questions or improve the story itself (replace edit mode).
- self mode: ask questions or improve kitsoki itself.
- better testing for proposal mode - should work like conversation (w/ peristent convo)
- remote job mode: monitor and control sessions on VMs
- pass request id to downstream CLI and API calls so that the session/trace can be correlated, so for instance mcp validator can log directly against the right session (is this racy?)
- open a VS Code over Remote Tunnel or to local folder for a given project/PR/etc...
- conversation/session info/context mcp for LLM to use for clarification
- provide context/guidance/prompt to off_path based on current room + provide history/context/etc...
- multi-intent - when actions/intents are non-navigational they can be stacked within a single input - on Oregon Trail it's like name the party, define the profession and start month in a single statement
- single LLM chat across rooms, manage the scope to determine which rooms are contained within the chat, provides better context and richer history without hacks like the history mcp
- meta mode works like off path so it's a full convo not just a one-shot, can use all normal conventions like proposal
- meta mode chats are persistent and listed like oracle sessions
- meta mode as a generic concept, off-path, self-fix/improvement/extend.  each different meta mode has different agent and prompts
- add world display to TUI - apps can specify what state is shown in the panel, make it like the actions panel
- actions panel is too narrow
- --continue to resume existing session
- voice/speaker transport
- can't use numbers as start of chat text
- live vs local mode for transports - sometimes we just want to work locally and don't need jira/bitbucket comments
- json-rpc, mcp, rest support w/ auth
- intent synonyms and caching
- remove world from top status bar it crowds everything
- add reload so that external app changes can be picked up without quit/restart (remove last error and retry failed operation (?)) - keep the world as-is so state is preserved
- recording captures git commits and LLM interactions so it's possible to do a deterministic replay, graceful call to LLM if it's a new/changed call that needs a real response to be recorded
- pure in-memory apps that are mutable in-flight, fully dynamic apps, export to yaml
- generate precursor recording/state so we can continue right at the point where a new feature is to be demoed or a bug is reproduced
- trace includes atomic state updates in some json-diff format so that there can be checkpoints + event stream for balance between size and speed of replay (reprocessing events) - event sourcing model for consistency
- vs code integration like claude, see what the user has open and propose changes using VS code diff
- voice/setting theming and localization - different languages and oregon trail could be in space
- ticket/pr/etc... providers that support bitbucket jira github or file mode for testing/dev match our existing bugfix artifact write pattern
- these providers are behind mcp for use in sessions, pluggable backends with the same interface so a single prompt works across different implementations
- input history via up/down arrow

## Tech debt

### View rendering: unify structured + prose content

**Problem.** The TUI runs every state's `view:` through Glamour with
`glamour.WithPreservedNewLines()`. That's necessary for structured views
(e.g. the Terminal Room's `propose "…"` examples, menu-like bullet lists)
where each authored line must stay on its own line. But the same setting
caps pure-prose views at their hand-wrapped width — `cloak` foyer is
authored at ~65 chars/line, so it sits in a narrow column even on a
150-col terminal. Shrinking works (Glamour re-wraps longer-than-panel
lines); growing past the authored wrap is a no-op.

Stopgap picked 2026-04-23: leave `WithPreservedNewLines` on; document
that prose views expand only up to the author's wrap width. See the
transcript.go comment near `renderMarkdown`.

**Real fix direction.** Introduce a typed "view element" system so the
state can declare *what kind of content* each block is, and the renderer
styles accordingly. Sketch:

```yaml
view:
  - prose: |
      You are in a spacious hall, splendidly decorated in red and gold,
      with glittering chandeliers overhead. ...
  - list:
      title: "Available areas"
      items:
        - { key: "Start a new task", value: "jira search" }
        - { key: "Check my inbox",   value: "notifications" }
  - code: |
      propose "list files in /tmp"
  - kv:
      Workspace: "{{ world.current_workspace }}"
      Last result: "{{ world.proposal_result }}"
  - template: |
      Legacy free-form view; renders via current Glamour pipeline.
```

Benefits:

- **Prose** reflows to the full panel width. No more hand-wrap cap.
- **List** renders as an aligned two-column layout that the renderer
  sizes to the viewport (no more fragile hardcoded spaces between
  "terminal" and "(run commands)" being collapsed by Markdown).
- **Code** preserves layout exactly (monospace, whitespace intact).
- **KV** handles the "Workspace: x / Last result: y" pattern that
  Markdown today either merges into one line or renders awkwardly.
- **Template** is the escape hatch for apps that need raw Glamour.

Author-side migration would be opt-in: the existing `view: "<string>"`
form keeps today's Glamour-rendered behaviour (mapped to `- template:
<string>` internally). New apps can use the typed elements from day 1.

Runtime-side: a small `internal/tui/elements/` package with a renderer
per element kind. `transcriptModel` asks each element to render at
(viewportWidth), concatenates results, word-wraps prose internally.

Also likely swaps Glamour for direct lipgloss rendering in most cases —
Glamour is overkill when we control the structure. Keep Glamour only
inside the `template` escape hatch.

**Adjacent issues this would also solve.**

- Column-aligned lists (dev-story's main-room menu) currently rely on
  Markdown-collapsed multi-space runs, which look wrong at non-default
  widths.
- The Terminal Room's "Propose a command to run, e.g.: / indented
  examples" structure is a list dressed as prose.
- Off-path banner, proposal diff, trace embeds — all currently shoved
  into the same Glamour pipe and styled implicitly.
- LLM-driven proposals (per the apply-proposal doc) would be easier to
  author as element arrays than as opaque Markdown blobs.

**Blocking?** No current user is blocked — cloak's prose-narrow issue is
cosmetic and devstory's structured views already render correctly. Pick
this up when either (a) a new app needs column-aligned lists that don't
break on resize, or (b) someone tries to add richer UI (diffs, inline
images, panels).