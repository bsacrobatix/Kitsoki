# TUI: a free-text input floor on every web room

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   tui (web operator surface) — the fix is in the runstatus
frontend (`tools/runstatus/`); no Go/runtime seam changes (both RPCs the
fix needs already exist).
**Epic:**   — standalone
**Blocks:** [`oracle-off-ramp.md`](oracle-off-ramp.md) (the off-ramp is
unreachable on the web until this lands)

## Why

The [text-only contract](../architecture/transports.md#7-every-story-must-work-text-only)
says every story must be drivable by typing. The web UI breaks it: when
a room renders a `choice:` element the composer hides the free-text
input entirely, with no way to get it back.

`tools/runstatus/src/components/InputBar.vue` renders three
*mutually-exclusive* branches:

```
<template v-if="choiceItems.length">      … single-mode choice buttons   (line 6)
<template v-else-if="formElement">        … form-mode fields             (line 55)
<template v-else-if="isSemanticRoom">     … free-text textarea           (line 89)
```

and the slot-composer below them is gated the same way:

```
v-if="textIntents.length && !choiceItems.length && !formElement"   (line 135)
```

So the free-text path (`isSemanticRoom`, which submits via
`runstatus.session.turn` → semantic router) renders **only when the room
has no choice/form element at all**. The moment a room declares a
`choice:`, the web user can submit *nothing but* the enumerated options.
There is no Tab/Esc/"chat" escape.

The TUI does not have this gap. Its choice widget treats **Tab as an
explicit off-ramp** — `ChoiceCommit{Cancel: true, ToChat: true}`
(`internal/tui/choice_widget.go:396, 522`) dismisses the picker and
returns focus to the prompt textarea, and `/input` restores a prior
draft (`internal/tui/tui.go`, `ModeChoosing`). So this is a **web-only**
input deficiency.

### Why it matters more than it looks

1. **It makes the oracle off-ramp impossible on the web.** The off-ramp
   ([`oracle-off-ramp.md`](oracle-off-ramp.md)) fires when the user
   submits free text that no routing tier and no LLM can map to a
   declared intent. A `choice:` button calls `SubmitDirect(intent)`
   (`runstatus.session.submit`, `server.go:636`) and routes
   *deterministically* to a pre-wired intent — it can never produce a
   no-match. The only path to a no-match is arbitrary typed text through
   `session.turn` (`server.go:620`). The rooms that most want an
   off-ramp — intake, discovery, "describe your idea", an exploratory
   menu — are exactly the rooms most likely to show a `choice:` widget,
   which is exactly where the web hides the text box. The feature would
   ship working in the TUI and dead on the web.

2. **It is the only thing forcing a room to be text-drivable today.**
   Story-side, text-only safety currently holds (every shipped `choice:`
   pairs labels that match intent names; no `mode: form` in production;
   `media:` is always supplementary). But that is **author discipline,
   not an enforced invariant** — see Non-goals. The web composer is the
   one place where the runtime *actively* removes the text affordance, so
   it is the highest-leverage fix.

## What changes

A **free-text floor**: every room's composer always offers a way to
submit arbitrary text via `runstatus.session.turn`, regardless of what
typed-view elements the room declares.

Mirror the TUI's model rather than inventing a new one:

- When a `choice:`/`form` element is present, keep rendering the widget
  (it is the nice affordance) **but also** surface a free-text escape —
  a persistent "type a message instead" composer, or a Tab/▾ toggle that
  reveals it. The submitted text goes through `session.turn` (semantic
  router → off-ramp), not `session.submit`.
- Preserve the draft across the widget↔text toggle (the TUI's `/input`
  semantics).
- Default visible vs. behind-a-toggle is the open question below.

No backend change: `session.turn` and `session.submit` both already
exist (`server.go:620, 636`) and the router/off-ramp consume raw text.

## Non-goals (deficiencies noted, not fixed here)

These are real gaps against the text-only contract, surfaced by the same
audit, but out of scope for this slice:

- **The static `choice:` footer misleads on plain-text transports.**
  `Choice.Render` (the body the Jira/Bitbucket transports emit —
  `internal/render/elements/choice.go:21-24`) appends
  `[↑/↓ move • Enter pick • Tab chat • Esc cancel]`
  (`choice.go:88-90`). On a Jira comment those keystrokes don't exist;
  the labels are shown (so a user *could* type one) but the footer
  advertises affordances the surface can't honor. Form-mode renders bare
  `____` underlines (`choice.go:476`) with no "reply with `intent
  field=value`" instruction. A transport-aware footer/serialization is
  its own change.
- **No load-time lint enforces text-only safety.** Nothing rejects a
  room whose only affordance is a `choice:`/`media:` with no typeable
  intent path. The invariant is documented
  ([story-style §3.8](../stories/story-style.md#38-the-view-must-read-as-plain-text))
  but unchecked. A `kitsoki lint` rule (every advancing intent reachable
  by text; `media:` essential meaning mirrored in prose) would make the
  contract enforceable rather than aspirational.

## Open questions

1. **Default visibility.** Is the free-text composer always visible
   alongside the widget, or revealed by a toggle (keeping the widget the
   primary affordance)? Always-visible is the safest read of "text is the
   floor"; a toggle keeps the structured surface clean. Lean:
   always-visible, de-emphasized, below the widget.
2. **Keybinding parity.** Should the web bind Tab to focus-the-composer
   to match the TUI's muscle memory, or is that a footgun in a browser
   (Tab = focus traversal)? Lean: a visible affordance, not Tab.
3. **Discoverability of the off-ramp.** Once text is submittable, should
   a room that declared `oracle_off_ramp:` hint it in the composer
   placeholder ("ask anything…")? Defer to the off-ramp proposal.

## References

- [`../architecture/transports.md` §7](../architecture/transports.md#7-every-story-must-work-text-only) — the contract this restores.
- [`oracle-off-ramp.md`](oracle-off-ramp.md) — the feature this unblocks.
- `tools/runstatus/src/components/InputBar.vue` — the component to change.
- `internal/runstatus/server/server.go:620,636` — `session.turn` (text)
  vs `session.submit` (intent).
- `internal/tui/choice_widget.go:396,522` — the TUI escape-hatch to copy.
